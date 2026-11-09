package probe_test

// 本文件是 Stage 2D 草案 §3.2.2 W-1..W-5 的离线验收测试。
//
// 边界（与 design/stage2d-plan.md §2.1 方案 A 对应）：
//   - 目标侧：本机 fake ES（httptest）承载一次性索引元数据，Binding/guard/身份比较
//     走 vector + common 的生产实现。它只证明"绑定、guard 与身份校验链被真实执行"，
//     不证明任何真实 ES 事实（真实 ES 属 S-2，需另行授权）。
//   - 模型侧：本机 httptest 对端，只替身 HTTP 响应；预算预留/承诺、角色 adapter、
//     guardedEmbedder、rerank 校验链、错误分类全部是生产代码。
//   - 断言使用"服务端命中数"作为真实发送次数，配合预算快照，避免只测预算组件。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// probeHarness 是"本机 fake ES 真实绑定 + 本机模型对端"的完整探针链。
type probeHarness struct {
	Plan   probe.Plan
	Target *probe.LocalFakeESTarget
	Model  *probe.LocalModelServer
	Chain  *probe.Chain
}

// newProbeHarness 构造探针链。所有端点都是本机 httptest，ES 目标是本机 fake ES；
// mutate 在构造前调整计划（预算/重试/维度）与模型对端行为。
func newProbeHarness(t *testing.T, mutate func(plan *probe.Plan, behavior *probe.ModelBehavior)) *probeHarness {
	t.Helper()

	model := probe.NewLocalModelServer()
	t.Cleanup(model.Close)

	plan := validPlan()
	plan.Chat.Endpoint = model.URL()
	plan.Embed.Endpoint = model.URL()
	plan.Rerank.URL = model.RerankURL()

	behavior := probe.DefaultModelBehavior()
	if mutate != nil {
		mutate(&plan, &behavior)
	}
	// 默认让 Embedding 响应维度等于计划声明值（guardedEmbedder 会真实校验）。
	// 显式设置 EmbedDims 的用例（维度不匹配）优先。
	if behavior.EmbedDims == 0 {
		behavior.EmbedDims = plan.Embed.Dimensions
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("test plan must be valid: %v", err)
	}
	model.SetBehavior(behavior)

	target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	t.Cleanup(target.Close)

	chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{
		APIKey: "probe-offline-placeholder-key",
	})
	if err != nil {
		t.Fatalf("new probe chain: %v", err)
	}
	t.Cleanup(chain.Close)

	return &probeHarness{Plan: plan, Target: target, Model: model, Chain: chain}
}

// TestProbeChain_ProductionBindingAndScope 证明探针链真的走生产绑定/身份/guard 链，
// 并记录 Chat/Embedding/Rerank 各自的离线覆盖范围（fake ES 不等于真实 ES 已验证）。
func TestProbeChain_ProductionBindingAndScope(t *testing.T) {
	h := newProbeHarness(t, nil)

	// 构建期真实读取本机 fake ES 两次：
	//  1) vector.BindPhysicalTarget 读取 settings/mappings 产出 TargetBinding；
	//  2) common.NewEmbedding → verifiedTargetBinding 重新读取并比对完整指纹。
	if got, want := h.Target.Reads(), int64(2); got != want {
		t.Fatalf("build-time fake ES reads = %d, want %d (bind + verified binding)", got, want)
	}

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if report.Stopped {
		t.Fatalf("Run() reported stopped: %s", report.StopReason)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1", got)
	}
	if got := h.Model.EmbedHits(); got != 1 {
		t.Fatalf("embed hits = %d, want 1", got)
	}
	if got := h.Model.RerankHits(); got != 1 {
		t.Fatalf("rerank hits = %d, want 1", got)
	}

	// Embedding 前后各一次 guard 重新读取：2（构建）+ 2（前/后）= 4。
	if got, want := h.Target.Reads(), int64(4); got != want {
		t.Fatalf("fake ES reads after one embedding = %d, want %d (build 2 + guard before/after)", got, want)
	}
	// fake ES 只收到"读取本索引"的请求：没有建索引/写入/删除尝试。
	if got := h.Target.OtherCalls(); got != 0 {
		t.Fatalf("fake ES received %d non-read requests, want 0", got)
	}

	if len(report.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(report.Steps))
	}
	embedStep := report.Steps[1]
	if embedStep.Step != probe.StepEmbed || embedStep.Attempts != 1 || embedStep.CommittedAttempts != 1 {
		t.Fatalf("embed step report = %+v, want one committed attempt", embedStep)
	}
	if embedStep.VectorCount != 1 || len(embedStep.VectorDims) != 1 || embedStep.VectorDims[0] != h.Plan.Embed.Dimensions {
		t.Fatalf("embed step vectors = %d/%v, want 1 vector of %d dims", embedStep.VectorCount, embedStep.VectorDims, h.Plan.Embed.Dimensions)
	}
	if report.Steps[2].RerankResults != 1 {
		t.Fatalf("rerank results = %d, want 1", report.Steps[2].RerankResults)
	}
	if got := report.ProcessBudget; got.CommittedAttempts != 3 || got.InFlight != 0 {
		t.Fatalf("process budget = %+v, want 3 committed attempts and 0 in flight", got)
	}
}

// TestProbeChain_IdentityMismatchRejected 证明两处身份校验都在链上：
//  1. 配置层面的 similarity 与生产期望（cosine）不一致：预检（Plan.Validate）在
//     构造期即拒绝，Readiness 与执行入口同源（复核 P2）；
//  2. 后端数据层面的指纹不一致：即使配置自洽，common.NewEmbedding 的完整指纹
//     比较仍会在构造期 fail closed（用 SetFingerprint 从 fake ES 侧制造 dot）。
func TestProbeChain_IdentityMismatchRejected(t *testing.T) {
	model := probe.NewLocalModelServer()
	defer model.Close()

	plan := validPlan()
	plan.Target.Similarity = "dot"
	model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})

	target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	defer target.Close()

	// 1) 配置层面的不一致：预检先拒绝，不再需要走到后端才会失败。
	chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{
		APIKey: "probe-offline-placeholder-key",
	})
	if chain != nil {
		chain.Close()
	}
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Fatalf("NewChain() = %v, want ErrFingerprintMismatch (declared similarity vs production expectation)", err)
	}

	// 2) 后端数据层面的不一致：配置自洽（cosine），但 fake ES 里的 fingerprint
	// 声明了另一个 embedding 模型（mapping 本身仍自洽，因此只会在身份比较处失败）。
	same := validPlan()
	model.SetBehavior(probe.ModelBehavior{EmbedDims: same.Embed.Dimensions})
	stored, err := vector.NewFingerprint(vector.FingerprintVersion, "probe-embed-other", same.Embed.Dimensions, same.Target.ProviderName, same.Target.Similarity)
	if err != nil {
		t.Fatalf("build stored fingerprint: %v", err)
	}
	target2, err := probe.NewLocalFakeESTarget(same.Target, same.Embed.Model, same.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	defer target2.Close()
	target2.SetFingerprint(stored)

	chain2, err := probe.NewChain(context.Background(), same, target2, probe.ChainOptions{
		APIKey: "probe-offline-placeholder-key",
	})
	if chain2 != nil {
		chain2.Close()
	}
	// 失败来自生产链的重新读取与身份比较（binding.Verify → VerifyFingerprint），
	// 不是探针自己的新规则。
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Fatalf("NewChain() = %v, want ErrFingerprintMismatch (stored fingerprint differs)", err)
	}
	if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
		t.Fatalf("model hits = %d, want 0 (fail closed before any send)", got)
	}
}

// TestProbeChain_GuardRejectsReplacedTarget 证明 guard 在探针入口真实生效：
// 绑定后目标被替换，下一次 embedding 在发送前被拒绝（零新增发送）。
func TestProbeChain_GuardRejectsReplacedTarget(t *testing.T) {
	h := newProbeHarness(t, nil)
	embedStep := probe.DefaultSteps()[1]

	h.Target.SetResourceID("uuid-probe-replaced")
	report, err := h.Chain.RunStep(context.Background(), embedStep)
	if !errors.Is(err, vector.ErrResourceReplaced) {
		t.Fatalf("RunStep() = %v, want ErrResourceReplaced", err)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 (guard must reject before send)", got)
	}
	if report.Attempts != 0 || report.CommittedAttempts != 0 || report.InFlight != 0 {
		t.Fatalf("step report = %+v, want zero attempts/commits/in-flight", report)
	}
	// 构建 2 次 + 被拒绝的这一次前置 guard 1 次。
	if got, want := h.Target.Reads(), int64(3); got != want {
		t.Fatalf("fake ES reads = %d, want %d", got, want)
	}
}

// 默认构建的远端隔离用例见 endpoint_policy_test.go（`//go:build !online`）：
// 那组用例断言"默认构建没有任何调用方开关可以打开远端"。

// TestW1_RetryReservesAndCommitsEveryAttempt：W-1。
// 受控重试的每次 HTTP 尝试都重新经过同一请求级 owner 的预留 → 承诺。
func TestW1_RetryReservesAndCommitsEveryAttempt(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, behavior *probe.ModelBehavior) {
		plan.Retry = probe.RetryPolicy{MaxAttempts: 2, RetryOnTransportError: true}
		behavior.EmbedAbortFirst = 1 // 首次请求以连接中断结束（真实传输错误）
	})

	report, err := h.Chain.Run(context.Background(), []probe.Step{probe.DefaultSteps()[1]})
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if got := h.Model.EmbedHits(); got != 2 {
		t.Fatalf("embed hits = %d, want 2 (one aborted + one retried)", got)
	}
	step := report.Steps[0]
	if step.Attempts != 2 || step.CommittedAttempts != 2 {
		t.Fatalf("step report = %+v, want 2 attempts and 2 commits", step)
	}
	if got := report.ProcessBudget.CommittedAttempts; got != 2 {
		t.Fatalf("process committed attempts = %d, want 2", got)
	}
	// usage 缺失（含被中断的尝试）按 unknown 保守结算：每次预留 4 输入 token。
	// "probe document" 长度 14 → 14/4+1 = 4。
	if got := step.InputUsed; got != 8 {
		t.Fatalf("step input used = %d, want 8 (2 x conservative 4)", got)
	}
	if step.InFlight != 0 || report.ProcessBudget.InFlight != 0 {
		t.Fatalf("in-flight leak: step=%+v process=%+v", step, report.ProcessBudget)
	}
	// fake ES 读取：构建 2 + 失败尝试前置 guard 1 + 成功尝试前后各 1 = 5。
	if got, want := h.Target.Reads(), int64(5); got != want {
		t.Fatalf("fake ES reads = %d, want %d", got, want)
	}
}

// TestW2_ExhaustedBudgetSendsNothingFurther：W-2。
// 进程预算耗尽后不再有任何新增发送，后续步骤不启动。
func TestW2_ExhaustedBudgetSendsNothingFurther(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, _ *probe.ModelBehavior) {
		plan.Budget.MaxRequests = 1 // 只允许 1 次尝试
	})

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	if !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("Run() = %v, want ErrBudgetExhausted", err)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1 (the only admitted attempt)", got)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 after exhaustion", got)
	}
	if got := h.Model.RerankHits(); got != 0 {
		t.Fatalf("rerank hits = %d, want 0 after exhaustion", got)
	}
	if len(report.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (chat + rejected embed, rerank never started)", len(report.Steps))
	}
	embedStep := report.Steps[1]
	if embedStep.Step != probe.StepEmbed || embedStep.Attempts != 0 || embedStep.CommittedAttempts != 0 {
		t.Fatalf("embed step report = %+v, want zero attempts/commits", embedStep)
	}
	if !report.Stopped || report.StopReason != "category=budget_exhausted" {
		t.Fatalf("report = %+v, want a stopped run with the budget category", report)
	}
	if got := report.ProcessBudget.CommittedAttempts; got != 1 {
		t.Fatalf("process committed attempts = %d, want 1", got)
	}
}

// TestW3_ExpiredCallerContextStopsBeforeSend：W-3（deadline 在探针入口生效）。
// 使用已过期的调用方 ctx（与 StepDeadline 共用同一 ownerErr 判定路径）：
// 预留阶段即失败，零新增发送。（1ns 级 StepDeadline 受本机时钟粒度影响，
// 不保证在预留前过期，因此不用它断言“入口零发送”；预算自身 deadline 见下一用例。）
func TestW3_ExpiredCallerContextStopsBeforeSend(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, _ *probe.ModelBehavior) {
		plan.Retry = probe.RetryPolicy{MaxAttempts: 3, RetryOnTransportError: true}
	})

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	report, err := h.Chain.Run(expired, probe.DefaultSteps())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want context.DeadlineExceeded", err)
	}
	if got := h.Model.ChatHits() + h.Model.EmbedHits() + h.Model.RerankHits(); got != 0 {
		t.Fatalf("model hits = %d, want 0 (deadline before send)", got)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (stop after the first deadline failure)", len(report.Steps))
	}
	step := report.Steps[0]
	if step.Attempts != 0 || step.CommittedAttempts != 0 {
		t.Fatalf("step report = %+v, want zero attempts/commits", step)
	}
	if got := report.ProcessBudget; got.AttemptsUsed != 0 || got.InFlight != 0 {
		t.Fatalf("process budget = %+v, want zero usage", got)
	}
}

// TestW3_DeadlineDuringSendIsNotRetried：W-3（deadline 在发送后生效，且不触发重试）。
func TestW3_DeadlineDuringSendIsNotRetried(t *testing.T) {
	gate := probe.NewGate()
	h := newProbeHarness(t, func(plan *probe.Plan, behavior *probe.ModelBehavior) {
		plan.Budget.StepDeadline = 150 * time.Millisecond
		plan.Retry = probe.RetryPolicy{MaxAttempts: 3, RetryOnTransportError: true}
		behavior.ChatGate = gate // 让对端阻塞到 ctx deadline
	})
	// 夹具清理顺序（LIFO）：先放行阻塞的 handler，再关闭本机模型对端。
	t.Cleanup(gate.Release)

	started := time.Now()
	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	elapsed := time.Since(started)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("elapsed = %s, want the step deadline to actually expire", elapsed)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1 (no retry on deadline)", got)
	}
	step := report.Steps[0]
	if step.Attempts != 1 || step.CommittedAttempts != 1 {
		t.Fatalf("step report = %+v, want exactly 1 admitted attempt", step)
	}
	if step.InFlight != 0 || report.ProcessBudget.InFlight != 0 {
		t.Fatalf("in-flight not released: step=%+v process=%+v", step, report.ProcessBudget)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(report.Steps))
	}
	if got := h.Model.EmbedHits() + h.Model.RerankHits(); got != 0 {
		t.Fatalf("later steps sent %d requests after a deadline stop, want 0", got)
	}
}

// TestW3_ConcurrencyLimitAppliesAtProbeEntry：W-3（并发上限在探针入口生效）。
//
// 并发停止语义（复核 P1）：第二个步骤因唯一并发额度不足失败即触发全局停止，
// 该停止会取消仍在途的第一个尝试（而不是让它继续/重试），因此第一个步骤以
// ErrAlreadyStopped 结束；已提交的尝试计入预算，in-flight 归零。
func TestW3_ConcurrencyLimitAppliesAtProbeEntry(t *testing.T) {
	gate := probe.NewGate()
	h := newProbeHarness(t, func(_ *probe.Plan, behavior *probe.ModelBehavior) {
		behavior.ChatGate = gate
	})
	t.Cleanup(gate.Release)
	if h.Plan.Budget.MaxConcurrency != 1 {
		t.Fatalf("test precondition: MaxConcurrency = %d, want 1", h.Plan.Budget.MaxConcurrency)
	}

	chatStep := probe.DefaultSteps()[0]
	errCh := make(chan error, 1)
	go func() {
		_, err := h.Chain.RunStep(context.Background(), chatStep)
		errCh <- err
	}()

	select {
	case <-gate.Reached():
	case <-time.After(3 * time.Second):
		t.Fatal("chat request never reached the local model endpoint")
	}

	// 第一个请求仍在途（占用唯一并发 slot）：第二步必须在入口被拒绝。
	report, err := h.Chain.RunStep(context.Background(), probe.DefaultSteps()[1])
	if !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("second step = %v, want ErrBudgetExhausted", err)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 while concurrency is exhausted", got)
	}
	if report.Attempts != 0 || report.CommittedAttempts != 0 {
		t.Fatalf("second step report = %+v, want zero attempts/commits", report)
	}

	gate.Release()
	if err := <-errCh; !errors.Is(err, probe.ErrAlreadyStopped) {
		t.Fatalf("first step = %v, want ErrAlreadyStopped (global stop cancels the in-flight attempt)", err)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1 (the cancelled attempt must not be retried)", got)
	}
	final := h.Chain.ProcessSnapshot()
	if final.InFlight != 0 {
		t.Fatalf("process in-flight = %d, want 0 after release", final.InFlight)
	}
	if final.CommittedAttempts != 1 {
		t.Fatalf("process committed attempts = %d, want 1 (rejected step must not commit)", final.CommittedAttempts)
	}
}

// TestW4_UsageSettlement：W-4。
// usage 缺失按 unknown 处理（不记 0），结算可观测且释放 in-flight。
func TestW4_UsageSettlement(t *testing.T) {
	t.Run("usage_missing_is_unknown", func(t *testing.T) {
		h := newProbeHarness(t, func(_ *probe.Plan, behavior *probe.ModelBehavior) {
			behavior.ChatUsage = false
		})

		report, err := h.Chain.Run(context.Background(), []probe.Step{probe.DefaultSteps()[0]})
		if err != nil {
			t.Fatalf("Run() unexpected error: %v", err)
		}
		step := report.Steps[0]
		if step.Usage != "unknown" {
			t.Fatalf("usage = %q, want unknown", step.Usage)
		}
		// unknown 不记 0：按预留量保守结算（"probe ping" → 10/4+1 = 3 输入，输出=MaxOutputTokens）。
		if step.InputUsed != 3 {
			t.Fatalf("input used = %d, want 3 (conservative reserve)", step.InputUsed)
		}
		if want := int64(h.Plan.Chat.MaxOutputTokens); step.OutputUsed != want {
			t.Fatalf("output used = %d, want %d (conservative reserve, not 0)", step.OutputUsed, want)
		}
		if step.InFlight != 0 {
			t.Fatalf("step in-flight = %d, want 0", step.InFlight)
		}
		budget := report.ProcessBudget
		if budget.InFlight != 0 || budget.InputReserved != 0 || budget.OutputReserved != 0 {
			t.Fatalf("process budget = %+v, want released reservations", budget)
		}
		if budget.InputUsed != 3 || budget.OutputUsed != int64(h.Plan.Chat.MaxOutputTokens) {
			t.Fatalf("process settlement = %+v, want the conservative amounts", budget)
		}
	})

	t.Run("usage_reported_is_known", func(t *testing.T) {
		h := newProbeHarness(t, nil) // 默认 chat 响应携带 usage 3/5/8

		report, err := h.Chain.Run(context.Background(), []probe.Step{probe.DefaultSteps()[0]})
		if err != nil {
			t.Fatalf("Run() unexpected error: %v", err)
		}
		step := report.Steps[0]
		if step.Usage != "known" {
			t.Fatalf("usage = %q, want known", step.Usage)
		}
		if step.InputUsed != 3 || step.OutputUsed != 5 {
			t.Fatalf("settlement = (%d, %d), want (3, 5) from reported usage", step.InputUsed, step.OutputUsed)
		}
		if step.InFlight != 0 || report.ProcessBudget.InFlight != 0 {
			t.Fatalf("in-flight not released: step=%+v process=%+v", step, report.ProcessBudget)
		}
	})
}

// TestW5_UnexpectedErrorStopsSubsequentSteps：W-5。
func TestW5_UnexpectedErrorStopsSubsequentSteps(t *testing.T) {
	h := newProbeHarness(t, func(_ *probe.Plan, behavior *probe.ModelBehavior) {
		behavior.ChatStatus = 500 // 非预期 HTTP 状态：不是传输错误，不重试
	})

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	if err == nil {
		t.Fatal("Run() = nil, want a non-unexpected-status error")
	}
	if errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("Run() = %v, want a non-budget error", err)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (stop after the failed chat step)", len(report.Steps))
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1 (no retry for unexpected status)", got)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 after a stop", got)
	}
	if got := h.Model.RerankHits(); got != 0 {
		t.Fatalf("rerank hits = %d, want 0 after a stop", got)
	}
	if !report.Stopped || report.StopReason != "category=http_status status=500" {
		t.Fatalf("report = %+v, want a stopped run with the sanitized status category", report)
	}
	if step := report.Steps[0]; step.Attempts != 1 || step.CommittedAttempts != 1 || step.InFlight != 0 {
		t.Fatalf("chat step report = %+v, want 1 admitted attempt and no in-flight leak", step)
	}

	// 停止后不得再启动任何步骤。
	if _, err := h.Chain.RunStep(context.Background(), probe.DefaultSteps()[1]); !errors.Is(err, probe.ErrAlreadyStopped) {
		t.Fatalf("RunStep() after stop = %v, want ErrAlreadyStopped", err)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 after ErrAlreadyStopped", got)
	}
}

// TestProbeChain_ResponseDimensionMismatchRejected 证明 guardedEmbedder 的响应校验在链上：
// 对端返回的向量维度与声明不一致时，尝试已发送但结果被拒绝，且预算已结算、无在途泄漏。
func TestProbeChain_ResponseDimensionMismatchRejected(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, behavior *probe.ModelBehavior) {
		behavior.EmbedDims = plan.Embed.Dimensions / 2 // 明确的维度不匹配
	})

	report, err := h.Chain.RunStep(context.Background(), probe.DefaultSteps()[1])
	if !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Fatalf("RunStep() = %v, want ErrEmbeddingDimensions", err)
	}
	if got := h.Model.EmbedHits(); got != 1 {
		t.Fatalf("embed hits = %d, want 1 (request was sent before validation)", got)
	}
	if report.Attempts != 1 || report.CommittedAttempts != 1 {
		t.Fatalf("step report = %+v, want 1 admitted attempt", report)
	}
	if report.VectorCount != 0 || report.InFlight != 0 {
		t.Fatalf("step report = %+v, want no accepted vectors and no in-flight leak", report)
	}
	// 维度不匹配属于校验链拒绝，证据只写脱敏类别（此处从返回的 error 派生）。
	if got := probe.ClassifyError(err); got != "category=dimension_mismatch" {
		t.Fatalf("stop reason = %q, want the sanitized dimension category", got)
	}
}

// TestRedirect_ProhibitedIsNotRetried：复核 P2。
// 307 重定向被 transport 策略禁止后必须立即停止，不得当成传输错误重试：
// 只命中原端点一次，重定向目标 0 次，后续步骤不启动，预算只承诺一次。
func TestRedirect_ProhibitedIsNotRetried(t *testing.T) {
	var targetHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	h := newProbeHarness(t, func(plan *probe.Plan, behavior *probe.ModelBehavior) {
		plan.Retry = probe.RetryPolicy{MaxAttempts: 2, RetryOnTransportError: true}
		behavior.ChatRedirect = redirectTarget.URL + "/steal"
	})

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	if !errors.Is(err, providers.ErrRedirectProhibited) {
		t.Fatalf("Run() = %v, want ErrRedirectProhibited (错误链必须保留)", err)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("origin hits = %d, want 1 (禁止的重定向不得重试)", got)
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0 (重定向目标不得被访问)", got)
	}
	if got := h.Model.EmbedHits() + h.Model.RerankHits(); got != 0 {
		t.Fatalf("later step hits = %d, want 0 after stop", got)
	}
	step := report.Steps[0]
	if step.Attempts != 1 || step.CommittedAttempts != 1 || step.InFlight != 0 {
		t.Fatalf("chat step = %+v, want exactly 1 admitted attempt and no in-flight leak", step)
	}
	if got := report.ProcessBudget.CommittedAttempts; got != 1 {
		t.Fatalf("process committed = %d, want 1 (same owner, single commit)", got)
	}
	if report.StopReason != "category=redirect_prohibited" {
		t.Fatalf("stop reason = %q, want the sanitized redirect category", report.StopReason)
	}
	if strings.Contains(report.StopReason, "127.0.0.1") || strings.Contains(report.StopReason, "http") {
		t.Fatalf("stop reason leaks endpoint details: %q", report.StopReason)
	}
}

// TestPreflight_InvalidLaterStepSendsNothing：复核 P2。
// 第一步合法、第二步非法时，执行入口必须在发送前失败（模型零命中），
// 且与 Readiness 的结论一致（不会出现"未就绪却已发送"）。
func TestPreflight_InvalidLaterStepSendsNothing(t *testing.T) {
	h := newProbeHarness(t, nil)
	steps := []probe.Step{probe.DefaultSteps()[0], {Kind: probe.StepEmbed}} // 第二步缺 texts

	report, err := h.Chain.Run(context.Background(), steps)
	if !errors.Is(err, probe.ErrEmptyStep) {
		t.Fatalf("Run() = %v, want ErrEmptyStep", err)
	}
	if got := h.Model.ChatHits(); got != 0 {
		t.Fatalf("chat hits = %d, want 0 (later invalid step must be rejected before the first send)", got)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0", got)
	}
	if len(report.Steps) != 0 {
		t.Fatalf("steps = %d, want 0 (no step may start)", len(report.Steps))
	}
	if !report.Stopped || report.StopReason != "category=validation" {
		t.Fatalf("report = %+v, want a stopped run with the validation category", report)
	}
	if got := report.ProcessBudget.CommittedAttempts; got != 0 {
		t.Fatalf("process committed = %d, want 0", got)
	}

	// Readiness 与实际执行入口同源：同一个步骤集合必须同时被两边拒绝。
	plan := h.Plan
	plan.Authorization = probe.Authorization{OnlineApproved: true, Scope: probe.ScopeS1, ApprovedBy: "offline", ApprovedAt: "offline"}
	const envName = "PROBE_FIX_PREFLIGHT_CREDENTIAL"
	t.Setenv(envName, "placeholder-not-a-real-key")
	cfg := probe.OnlineConfig{Plan: plan, Credentials: probe.Credentials{EnvNames: []string{envName}}, Steps: steps}
	if cfg.Ready() {
		t.Fatalf("Ready() = true, want false for the same steps: %+v", probe.Readiness(cfg))
	}
	for _, item := range probe.Readiness(cfg) {
		if item.Name == "steps" && item.OK {
			t.Fatalf("steps readiness must not be OK: %+v", item)
		}
	}

	// 运行期校验仍保留（绕过 Run 直接调用 RunStep 也不发送）。
	if _, err := h.Chain.RunStep(context.Background(), probe.Step{Kind: probe.StepEmbed}); !errors.Is(err, probe.ErrEmptyStep) {
		t.Fatalf("RunStep() = %v, want ErrEmptyStep (runtime validation kept)", err)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 after the rejected RunStep", got)
	}
}

// TestStopReasonIsSanitized：复核 P2。
// 服务端错误正文不得进入对外证据；但底层错误不得被吞掉（调用方仍可诊断）。
func TestStopReasonIsSanitized(t *testing.T) {
	const marker = "offline-sensitive-marker-7f3a"
	h := newProbeHarness(t, func(_ *probe.Plan, behavior *probe.ModelBehavior) {
		behavior.ChatStatus = http.StatusBadRequest
		behavior.ChatErrorBody = `{"error":{"message":"` + marker + `","type":"invalid_request_error"}}`
	})

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
	if err == nil {
		t.Fatal("Run() = nil, want the provider error")
	}
	// 不通过吞掉错误实现脱敏：调用方拿到的错误仍保留原始内容。
	if !strings.Contains(err.Error(), marker) {
		t.Fatalf("returned error lost the underlying provider detail: %v", err)
	}
	if strings.Contains(report.StopReason, marker) {
		t.Fatalf("report leaks the provider body: %q", report.StopReason)
	}
	if report.StopReason != "category=http_status status=400" {
		t.Fatalf("stop reason = %q, want the sanitized status category", report.StopReason)
	}

	// 停止后的错误同样不得回显原始正文。
	_, stopErr := h.Chain.RunStep(context.Background(), probe.DefaultSteps()[1])
	if !errors.Is(stopErr, probe.ErrAlreadyStopped) {
		t.Fatalf("RunStep() = %v, want ErrAlreadyStopped", stopErr)
	}
	if strings.Contains(stopErr.Error(), marker) {
		t.Fatalf("ErrAlreadyStopped leaks the provider body: %v", stopErr)
	}
}
