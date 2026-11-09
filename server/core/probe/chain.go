package probe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"

	// 生产 builder 注册（init）：让 Registry 走与生产一致的 Build*Role 路径。
	_ "github.com/wangle201210/go-rag/server/core/providers/bailian"
	_ "github.com/wangle201210/go-rag/server/core/providers/openai"
)

// Chain 是探针实际调用链：生产 embedding 入口 + 生产角色构造 + 单一预算 owner。
type Chain struct {
	plan      Plan
	budget    *providers.ProcessBudget
	registry  providers.ProviderRegistry
	embedder  embedding.Embedder
	transport *providers.Transport
	// remoteSend 是构造期判定：本链是否具备向非回环端点发送的能力（构造后只读）。
	remoteSend bool
	// gate 是所有模型 HTTP 请求的发送闸门（见 sendgate.go）：它把“已准入”与
	// “已经在途”区分开，并让停止标记与真实发送之间具有可验证的全序。
	gate *sendGate

	// mu 的写侧发布停止标记，读侧（commitSend）观察它：二者构成发送准入的全序。
	mu          sync.RWMutex
	stopped     bool
	stopSummary string
	// inflight 是已准入但尚未结束的发送尝试；stop 会在同一把锁下完成停止标记，
	// 并在锁外取消这些尝试。已准入 ≠ 已在途：它们之间还隔着预算预留、guard、
	// 请求编码，真正的“在途”由 sendGate 在 RoundTrip 之前判定。
	inflight map[*attemptHandle]struct{}
	// beforeCancel 仅供包内测试把“停止标记已发布”与“取消在途句柄”确定性地
	// 分开（复核第 3 轮 P1 的残余窗口特征化，见 sendgate_test.go）。
	// 生产路径为 nil：不改变停止语义，也不引入等待。
	beforeCancel func()
}

// attemptHandle 代表一次已准入的发送尝试（携带取消句柄）。
type attemptHandle struct {
	cancel context.CancelFunc
}

// NewChain 组装探针链：
//  1. Plan 防御性深拷贝（复核 P2：构造后修改原 Plan 不得与运行期读取竞争）；
//  2. Plan 校验（固定目标/参数/预算/重试/授权）；
//  3. 端点隔离策略（默认仅回环；online 远端必须是 HTTPS）；
//  4. 角色身份预检（与 Preflight/Readiness 共用 ValidateRoles）；
//  5. 构造期授权/凭据边界（online 远端必须 S-1 授权 + 环境变量凭据）；
//  6. 进程预算 owner（一次构造，不可重置）；
//  7. Registry（生产 Build*Role + 预算包装）+ config.Config；
//  8. 本机 fake ES 的真实 TargetBinding 注入 → common.NewEmbedding 生产链
//     （Registry/身份/guard 校验在构建期即真实执行一次）。
//
// Chain 只能经本函数获得：Run/RunStep 因此不可能绕过这里的授权与凭据边界。
func NewChain(ctx context.Context, plan Plan, target *LocalFakeESTarget, opts ChainOptions) (*Chain, error) {
	plan = plan.Clone()
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if target == nil {
		return nil, ErrTargetRequired
	}
	if err := CheckEndpoints(plan); err != nil {
		return nil, err
	}
	// 非法模型身份（含不支持的 @version）在构造期失败，而不是先发一步再拒绝
	// 后续角色（复核 P2）。
	if err := ValidateRoles(plan); err != nil {
		return nil, err
	}
	// 构造期授权/凭据边界：默认构建只要求构造期密钥；online 构建的远端端点
	// 必须经过 S-1 授权且凭据只能来自环境变量（复核 P1）。
	apiKey, err := resolveConstructionCredential(plan, opts)
	if err != nil {
		return nil, err
	}
	budget, err := plan.Budget.NewProcessBudget()
	if err != nil {
		return nil, err
	}

	transport := providers.NewTransport()
	chatProfile, embedProfile, rerankProfile, err := roleProfiles(plan)
	if err != nil {
		return nil, err
	}

	chain := &Chain{
		plan:       plan,
		budget:     budget,
		remoteSend: plan.HasRemoteEndpoints(),
		inflight:   make(map[*attemptHandle]struct{}),
	}
	// 实际发送准入闸门：三个角色共用这一份传输层，因此所有模型请求都在
	// RoundTrip 之前观察停止标记（见 sendgate.go）。
	gated, gate := newGatedTransport(transport, chain)
	chain.transport = gated
	chain.gate = gate

	registry, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:      chatProfile,
		Embed:     embedProfile,
		Rerank:    rerankProfile,
		Budget:    budget,
		Transport: gated,
		Credentials: func(context.Context, *providers.ProviderProfile) (string, error) {
			return apiKey, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("probe: build registry: %w", err)
	}
	chain.registry = registry

	conf := &config.Config{
		ManagedResourceLifecycle: true,
		Provider: &config.ProviderConfig{
			Name:                plan.Target.ProviderName,
			BaseURL:             plan.Embed.Endpoint,
			EmbeddingModel:      plan.Embed.Model,
			EmbeddingDimensions: plan.Embed.Dimensions,
			ChatModel:           plan.Chat.Model,
		},
		Registry: registry,
	}
	// 真实物理绑定（本机 fake ES 承载）：名称/资源/fingerprint 均已由具体目标读取校验。
	target.ApplyTo(conf)

	embedder, err := common.NewEmbedding(ctx, conf)
	if err != nil {
		return nil, fmt.Errorf("probe: build production embedding chain: %w", err)
	}
	chain.embedder = embedder
	return chain, nil
}

// ProcessSnapshot 返回进程级预算的只读快照（用于并发/deadline 观测）。
func (c *Chain) ProcessSnapshot() BudgetSnapshotView {
	return snapshotView(c.budget.Snapshot())
}

// Close 释放共享传输层的空闲连接。
func (c *Chain) Close() {
	if c == nil || c.transport == nil {
		return
	}
	c.transport.CloseIdleConnections()
}

// attemptOutcome 记录一次成功尝试的响应形状（不含向量内容与模型输出正文）。
type attemptOutcome struct {
	usage    string
	vecCount int
	vecDims  []int
	rerank   int
}

// Run 顺序执行探针步骤：任一步失败即停止，后续步骤不启动（W-5）。
//
// 发送前先做一次完整的 Preflight（计划 + 端点策略 + 角色身份 + 全部步骤）：
// 只要任何一步无效，本次运行零发送（复核 P2：不得先发第一步再拒绝第二步）。
// 运行期校验仍保留在 RunStep 中，两者共用同一组校验函数。
func (c *Chain) Run(ctx context.Context, steps []Step) (Report, error) {
	report := Report{}
	if err := c.checkSendBoundary(); err != nil {
		report.ProcessBudget = c.ProcessSnapshot()
		return report, err
	}
	// 入口克隆本次步骤矩阵（复核 P2）：调用方随后修改自己的切片/文档
	// 不会与本次运行的读取竞争。
	steps = cloneSteps(steps)
	if err := Preflight(c.plan, steps); err != nil {
		c.stop(err)
		report.ProcessBudget = c.ProcessSnapshot()
		c.markStopped(&report)
		return report, err
	}
	for _, step := range steps {
		stepReport, err := c.RunStep(ctx, step)
		if stepReport != nil {
			report.Steps = append(report.Steps, *stepReport)
		}
		report.ProcessBudget = c.ProcessSnapshot()
		if err != nil {
			c.markStopped(&report)
			return report, err
		}
	}
	return report, nil
}

// RunStep 执行单步探针：
//   - 每步一个请求级 owner（同一步的每次尝试共用该 owner，W-1）；
//   - 发送使用 owner 固定 deadline 派生的 ctx（W-3）；
//   - 预留失败/非预期错误立即停止，不再发起任何发送（W-2/W-5）。
func (c *Chain) RunStep(ctx context.Context, step Step) (*StepReport, error) {
	if err := c.checkSendBoundary(); err != nil {
		return nil, err
	}
	// 入口克隆：运行期只读这份私有副本（复核 P2）。
	step = step.clone()
	if err := step.validate(); err != nil {
		c.stop(err)
		return nil, err
	}
	// 运行期保留跨字段校验（防止绕过 Run 直接调用 RunStep 时发送自相矛盾的请求）。
	if err := step.validateAgainst(c.plan); err != nil {
		c.stop(err)
		return &StepReport{Step: step.Kind, Usage: "n/a"}, err
	}
	if err := c.enter(); err != nil {
		return &StepReport{Step: step.Kind, Skipped: true, Usage: "n/a"}, err
	}

	requestBudget, err := c.budget.NewRequestBudget(ctx, c.plan.Budget.StepOptions()...)
	if err != nil {
		c.stop(err)
		// 预留失败发生在发送之前：Attempts 保持 0，与模型对端命中数一致（W-2）。
		return &StepReport{Step: step.Kind, Usage: "n/a"}, err
	}
	stepCtx := providers.ContextWithRequestBudget(ctx, requestBudget)
	sendCtx, cancel := requestBudget.ContextWithDeadline(stepCtx)
	defer cancel()

	report := &StepReport{Step: step.Kind, Usage: "n/a"}
	start := time.Now()
	var lastErr error
	for attempt := 1; attempt <= c.plan.Retry.MaxAttempts; attempt++ {
		// 入口准入与全局停止在同一把锁下协调：停止之后不再启动新的尝试（含重试）。
		// 真正“字节是否发出”由 sendGate 在 RoundTrip 之前做第二次、且权威的判定：
		// 入口准入之后还可能阻塞在预算预留/guard/编码，那段窗口不再能产生发送。
		attemptCtx, done, admitErr := c.beginAttempt(sendCtx)
		if admitErr != nil {
			if attempt == 1 {
				report.Skipped = true
			}
			lastErr = admitErr
			break
		}
		// Attempts 只统计"已被预算承诺"的尝试：预算承诺在发送闸门之前，
		// 因此它可能包含"已承诺、随后被闸门拒绝、对端零命中"的尝试（W-2/W-3）。
		// "实际是否交给 transport"由下面的 GateAdmissions / TransportCalls 分别记录。
		before := requestBudget.Snapshot().CommittedAttempts
		trace := &sendTrace{}
		attemptCtx = withSendTrace(attemptCtx, trace)
		outcome, callErr := c.attempt(attemptCtx, step)
		done()
		if after := requestBudget.Snapshot().CommittedAttempts; after > before {
			report.Attempts++
		}
		report.GateAdmissions += int(trace.gateAdmissions.Load())
		report.TransportCalls += int(trace.transportCalls.Load())
		if callErr == nil {
			// 重试成功必须清除前一次尝试的错误，否则会把已恢复的传输错误当成失败。
			lastErr = nil
			report.Usage = outcome.usage
			report.VectorCount = outcome.vecCount
			report.VectorDims = outcome.vecDims
			report.RerankResults = outcome.rerank
			break
		}
		lastErr = callErr
		// 全局停止（由另一个步骤或本步骤之前的错误触发）导致的取消：不再重试。
		// 错误链同时保留 ErrAlreadyStopped 与原始原因，调用方仍可 errors.Is 识别。
		if c.isStopped() {
			lastErr = fmt.Errorf("%w: attempt cancelled by the global stop: %w", ErrAlreadyStopped, callErr)
			break
		}
		if !c.shouldRetry(callErr, attempt) {
			break
		}
	}

	snapshot := requestBudget.Snapshot()
	report.CommittedAttempts = snapshot.CommittedAttempts
	report.InputUsed = snapshot.InputUsed
	report.OutputUsed = snapshot.OutputUsed
	report.InFlight = snapshot.InFlight
	report.Duration = time.Since(start)

	if lastErr != nil {
		// 停止原因只写入脱敏摘要；原始错误直接返回给调用方（errors.Is 仍可用）。
		c.stop(lastErr)
		return report, lastErr
	}
	return report, nil
}

// attempt 发起一次真实模型调用（每次调用都经过角色上的预算包装）。
func (c *Chain) attempt(ctx context.Context, step Step) (attemptOutcome, error) {
	switch step.Kind {
	case StepChat:
		role, err := c.registry.GetChat(ctx)
		if err != nil {
			return attemptOutcome{}, err
		}
		msg, err := role.Generate(ctx,
			[]*schema.Message{schema.UserMessage(step.Prompt)},
			model.WithMaxTokens(c.plan.Chat.MaxOutputTokens),
		)
		if err != nil {
			return attemptOutcome{}, err
		}
		outcome := attemptOutcome{usage: "unknown"}
		if msg != nil && msg.ResponseMeta != nil && usageIsKnown(msg.ResponseMeta.Usage) {
			outcome.usage = "known"
		}
		return outcome, nil
	case StepEmbed:
		vectors, err := c.embedder.EmbedStrings(ctx, step.Texts)
		if err != nil {
			return attemptOutcome{}, err
		}
		dims := make([]int, len(vectors))
		for i, vec := range vectors {
			dims[i] = len(vec)
		}
		return attemptOutcome{usage: "n/a", vecCount: len(vectors), vecDims: dims}, nil
	case StepRerank:
		role, err := c.registry.GetReranker(ctx)
		if err != nil {
			return attemptOutcome{}, err
		}
		docs := step.Docs
		if len(docs) == 0 {
			docs = ProbeDocuments()
		}
		if step.TopK > len(docs) {
			return attemptOutcome{}, fmt.Errorf("probe: rerank top_k %d exceeds %d documents", step.TopK, len(docs))
		}
		reranked, err := role.Rerank(ctx, step.Query, docs, step.TopK)
		if err != nil {
			return attemptOutcome{}, err
		}
		return attemptOutcome{usage: "n/a", rerank: len(reranked)}, nil
	}
	return attemptOutcome{}, fmt.Errorf("%w: unknown step kind %q", ErrEmptyStep, step.Kind)
}

// usageIsKnown 与 providers 结算口径保持一致：nil、任一分项为负、
// total 与 prompt+completion 不一致、或全零，一律视为 unknown（不把 unknown 当 0）。
// 本函数只用于报告标注，真实结算仍由 providers.AttemptLease 执行。
func usageIsKnown(u *schema.TokenUsage) bool {
	if u == nil {
		return false
	}
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 {
		return false
	}
	if u.TotalTokens > 0 && u.TotalTokens != u.PromptTokens+u.CompletionTokens {
		return false
	}
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// shouldRetry 判定是否进行受控重试，只对**明确允许**的底层传输错误重试。
//
// 先识别重定向、策略、HTTP 状态、预算与取消错误并停止（见 errcat.go）；
// 只有剩余的真正连接级中断（EOF/连接重置/超时）才重试，
// 以免把"重定向被禁止"误当传输错误而重复命中原端点。
func (c *Chain) shouldRetry(err error, attempt int) bool {
	if attempt >= c.plan.Retry.MaxAttempts {
		return false
	}
	if !c.plan.Retry.RetryOnTransportError {
		return false
	}
	return isRetryableTransportError(err)
}

// checkSendBoundary 复述构造期已决策的远端发送边界（防御性检查）。
//
// NewChain 是 Chain 的唯一构造入口且已 fail closed；这里让每个公开发送入口都
// 重新声明同一边界，即使将来出现新的构造路径也不会静默放行远端发送。
func (c *Chain) checkSendBoundary() error {
	if !c.remoteSend {
		return nil
	}
	return remoteSendAuthorizationError(c.plan)
}

// beginAttempt 为一次发送尝试取得**入口准入**：
// 在同一把锁下检查全局停止并登记在途句柄。它只说明“本次尝试被允许开始”，
// 不说明请求已经发出：预算预留、guard 与请求编码都在其后。
// 发送闸门（sendGate）在 RoundTrip 之前做第二个、且可观测的判定：
// 停止标记发布后，已准入但尚未到达闸门的尝试一律返回 ErrAlreadyStopped，零字节发出。
// 注意：通过闸门也不等于已交给 transport 或对端已收到（见 sendgate.go 四状态）。
func (c *Chain) beginAttempt(parent context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if c.stopped {
		summary := c.stopSummary
		c.mu.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("%w: %s", ErrAlreadyStopped, summary)
	}
	if c.inflight == nil {
		c.inflight = make(map[*attemptHandle]struct{})
	}
	handle := &attemptHandle{cancel: cancel}
	c.inflight[handle] = struct{}{}
	c.mu.Unlock()

	release := func() {
		cancel()
		c.mu.Lock()
		delete(c.inflight, handle)
		c.mu.Unlock()
	}
	return ctx, release, nil
}

// isStopped 报告链是否已因任意步骤（含并发步骤）停止。
func (c *Chain) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// enter 检查是否已停止（W-5：停止后不再启动任何新步骤）。
// 只回显脱敏后的停止摘要，不回显原始错误（后者可能含服务端正文）。
func (c *Chain) enter() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return fmt.Errorf("%w: %s", ErrAlreadyStopped, c.stopSummary)
	}
	return nil
}

// stop 记录首个停止原因（后续错误不覆盖首个），只保留脱敏摘要；
// 并在同一把锁下发布**停止标记**（stopped=true）：这是发送闸门准入的写侧线性化点。
// 锁外取消全部在途句柄：已准入、尚未到达发送闸门的尝试因此不会发送；
// 已通过闸门（含已交给 transport）的请求不声称可撤销，只能靠 ctx 取消尽快放弃。
// 闸门准入与底层 RoundTrip 之间没有锁，因此"已通过闸门但未交给 transport"的
// 请求仍可能在标记之后发出：该残余窗口未闭合，见 sendgate.go。
// 预算 lease 由角色包装层终结（Abort/Finish），不会泄露。
func (c *Chain) stop(err error) {
	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		c.stopSummary = sanitizeStopReason(err)
	}
	cancels := make([]context.CancelFunc, 0, len(c.inflight))
	for handle := range c.inflight {
		cancels = append(cancels, handle.cancel)
	}
	c.mu.Unlock()
	// 测试钩子：只为记录“标记已发布、取消尚未施加”的时序（生产为 nil）。
	if hook := c.beforeCancel; hook != nil {
		hook()
	}
	for _, cancel := range cancels {
		cancel()
	}
}

// markStopped 把停止状态与脱敏摘要写入对外报告。
func (c *Chain) markStopped(report *Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	report.Stopped = c.stopped
	report.StopReason = c.stopSummary
}
