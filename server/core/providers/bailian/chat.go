// Package bailian 提供阿里云百炼（DashScope）的 Provider 适配器实现。
// 走 OpenAI 兼容协议（/compatible-mode/v1），复用 eino-ext 底层，
// 附加百炼特有的错误映射、usage 保守处理与响应验证。
package bailian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"

	chatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 百炼 Chat 适配层错误。
var (
	ErrNilProfile   = errors.New("bailian chat: nil ProviderProfile")
	ErrNilTransport = errors.New("bailian chat: nil Transport")
	ErrNilInner     = errors.New("bailian chat: inner model is nil")
)

// chatAdapter 包装 Eino model.BaseChatModel，附加预算接入（B1）与错误映射。
//
// 直接持有 Eino 原始模型实例（非 ChatRole 包装），以便从 Generate 返回的
// schema.Message.ResponseMeta 中提取真实 usage 进行预算结算。
type chatAdapter struct {
	inner   model.BaseChatModel // Eino 原始模型，用于直接调用并提取 usage
	profile *providers.ProviderProfile
	budget  *providers.ProcessBudget
}

// 编译期断言。
var _ providers.ChatRole = (*chatAdapter)(nil)

// BuildChat 构造百炼 Chat 角色适配器。
//
// 走 OpenAI 兼容协议 /compatible-mode/v1/chat/completions；
// Profile.Endpoint 应为完整 base_url（如 https://dashscope.aliyuncs.com/compatible-mode/v1）。
// 错误映射：HTTP 4xx/5xx → 明确错误信息；usage 缺失时保守处理（不静默当 0）。
func BuildChat(ctx context.Context, cfg providers.BuildConfig) (providers.ChatRole, error) {
	if cfg.Profile == nil {
		return nil, ErrNilProfile
	}
	if cfg.Transport == nil {
		return nil, ErrNilTransport
	}
	p := cfg.Profile
	cm, err := chatopenai.NewChatModel(ctx, &chatopenai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		BaseURL:    p.Endpoint,
		Model:      p.Model,
		HTTPClient: cfg.Transport.Client(),
	})
	if err != nil {
		return nil, mapChatBuildError(err)
	}
	if cm == nil {
		return nil, ErrNilInner
	}
	return &chatAdapter{inner: cm, profile: p.Clone(), budget: cfg.Budget}, nil
}

// Generate 调用底层模型并映射错误，附加预算结算（B1）。
// Fix 2e: 本地校验在 Reserve 之前完成。
// B3: 从 opts 解析实际 MaxTokens，与预留同源。
func (a *chatAdapter) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	// Fix 2e: 本地校验前置（在 Reserve 之前）
	if len(messages) == 0 {
		return nil, fmt.Errorf("bailian chat: empty messages")
	}

	// B3: 从 opts 解析实际输出上限，同源计算预留
	effectiveMaxOutput := a.resolveMaxOutput(opts)

	// B1: 预算接入
	lease, err := a.reserveBudget(ctx, a.estimateTokensWithOutput(messages, effectiveMaxOutput))
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	msg, err := a.inner.Generate(ctx, messages, opts...)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, mapChatError(err)
	}
	// 提取 usage 并结算
	if lease != nil {
		lease.Finish(extractUsage(msg))
	}
	return msg, nil
}

// Stream 调用底层流式接口并映射错误，附加预算结算（B1）。
// Fix 3: 使用 leaseStreamReader 包装，单一所有权终结 lease。
// B3: 从 opts 解析实际 MaxTokens，与预留同源。
// Fix 7: 使用 derivedCtx 传递给 inner Stream，使得转发 goroutine 退出时
// cancel 可中断底层 HTTP 请求（安全网）。
func (a *chatAdapter) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	// Fix 2e: 本地校验前置
	if len(messages) == 0 {
		return nil, fmt.Errorf("bailian chat: empty messages")
	}

	// B3: 从 opts 解析实际输出上限，同源计算预留
	effectiveMaxOutput := a.resolveMaxOutput(opts)

	// B1: 预算接入
	lease, err := a.reserveBudget(ctx, a.estimateTokensWithOutput(messages, effectiveMaxOutput))
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	// Fix 7: 派生可取消 ctx，传递给 inner Stream。
	// 转发 goroutine 退出时 cancel()，中断底层 HTTP 请求（安全网）。
	streamCtx, streamCancel := context.WithCancel(ctx)
	sr, err := a.inner.Stream(streamCtx, messages, opts...)
	if err != nil {
		streamCancel() // 创建失败，立即释放
		if lease != nil {
			lease.Abort(err)
		}
		return nil, mapChatError(err)
	}
	// 无预算或无 lease：直接返回（仍需保留 cancel 防止泄漏）
	if lease == nil {
		// 无 lease 时仍需保证 goroutine 退出后 cancel
		go func() {
			defer streamCancel()
			// 等待 sr 耗尽（EOF/error）后取消
			for {
				_, recvErr := sr.Recv()
				if recvErr != nil {
					return
				}
			}
		}()
		return sr, nil
	}
	// Fix 3: 使用 leaseStreamReader 包装，单一所有权终结
	return wrapLeaseStreamReader(streamCtx, streamCancel, sr, lease), nil
}

// reserveBudget 从 ctx 复用已有 RequestBudget，或创建新的。Fix 2a: owner 复用。
// budget 为 nil 时返回 (nil, nil)，调用方检查 nil 跳过预算。
func (a *chatAdapter) reserveBudget(ctx context.Context, est *providers.TokenEstimate) (*providers.AttemptLease, error) {
	if a.budget == nil {
		return nil, nil // 无预算：跳过
	}
	// Fix 2a: 优先从 ctx 复用入口 owner 构造的 RequestBudget
	rb := providers.RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = a.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("bailian chat: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("bailian chat: budget reserve: %w", err)
	}
	return lease, nil
}

// defaultMaxOutputTokens 是 Chat 模型的默认最大输出 token 预留。
// B3: 仅作为 opts 未指定 MaxTokens 时的回退默认值。
const defaultMaxOutputTokens int64 = 8192

// resolveMaxOutput 从 opts 解析实际 MaxTokens，与预算预留同源计算（B3）。
// opts 中有 MaxTokens 则使用该值；否则使用 defaultMaxOutputTokens。
func (a *chatAdapter) resolveMaxOutput(opts []model.Option) int64 {
	resolved := model.GetCommonOptions(&model.Options{}, opts...)
	if resolved != nil && resolved.MaxTokens != nil && *resolved.MaxTokens > 0 {
		return int64(*resolved.MaxTokens)
	}
	return defaultMaxOutputTokens
}

// estimateTokensWithOutput 从消息估算 token 数，使用实际输出上限（B3 同源）。
func (a *chatAdapter) estimateTokensWithOutput(messages []*schema.Message, maxOutput int64) *providers.TokenEstimate {
	var inputChars int64
	for _, m := range messages {
		if m != nil {
			inputChars += int64(len(m.Content))
		}
	}
	inputTokens := inputChars/4 + 1 // 保守：至少 1 token
	return &providers.TokenEstimate{
		InputTokens: inputTokens,
		MaxOutput:   maxOutput,
	}
}

// extractUsage 从 schema.Message 提取 TokenUsage 并转换为 model.TokenUsage。
// Eino OpenAI 适配层在 ResponseMeta.Usage 中填充 usage 信息。
func extractUsage(msg *schema.Message) *model.TokenUsage {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return nil
	}
	u := msg.ResponseMeta.Usage
	return &model.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// ---------------------------------------------------------------------------
// Fix 3: leaseStreamReader — 单一所有权终结 lease
// ---------------------------------------------------------------------------

// leaseStreamReader 包装 StreamReader，通过 Recv 单一所有权终结 lease。
// 用于内部 lease 追踪；不直接返回给业务。
type leaseStreamReader struct {
	inner     *schema.StreamReader[*schema.Message]
	lease     *providers.AttemptLease
	once      sync.Once
	lastUsage *model.TokenUsage
	mu        sync.Mutex
}

func (w *leaseStreamReader) Recv() (*schema.Message, error) {
	chunk, err := w.inner.Recv()
	if err != nil {
		w.finalize(err)
		return nil, err
	}
	if chunk != nil && chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil {
		w.mu.Lock()
		w.lastUsage = &model.TokenUsage{
			PromptTokens:     chunk.ResponseMeta.Usage.PromptTokens,
			CompletionTokens: chunk.ResponseMeta.Usage.CompletionTokens,
			TotalTokens:      chunk.ResponseMeta.Usage.TotalTokens,
		}
		w.mu.Unlock()
	}
	return chunk, nil
}

func (w *leaseStreamReader) finalize(err error) {
	w.once.Do(func() {
		w.mu.Lock()
		usage := w.lastUsage
		w.mu.Unlock()
		if err != nil && !errors.Is(err, io.EOF) {
			w.lease.Abort(err)
		} else {
			w.lease.Finish(usage)
		}
	})
}

// wrapLeaseStreamReader 包装 StreamReader，返回 pipe reader 供业务消费。
// 转发 goroutine 在后台从 lsw.Recv() 读取数据并写入 pipe writer；
// 当 writer.Send 返回 false（消费者已关闭）或源 EOF 时退出。
// lease 终结由转发 goroutine 的 defer 保证，不依赖消费者调用 Close。
//
// streamCancel 是派生 ctx 的取消函数；转发 goroutine 退出时调用，
// 中断底层 HTTP 请求（安全网：确保 body read 不悬挂）。
// 生产关闭链：consumer Close → adapter 检测 Send→false → adapter 退出 →
// inner writer 关闭 → inner receive 返回 → 转发 goroutine 退出 → cancel → lease 终结。
func wrapLeaseStreamReader(_ context.Context, streamCancel context.CancelFunc, sr *schema.StreamReader[*schema.Message], lease *providers.AttemptLease) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	lsw := &leaseStreamReader{inner: sr, lease: lease}

	go func() {
		defer streamCancel() // 安全网：goroutine 退出时取消 ctx，中断底层 HTTP 请求
		defer writer.Close() // 关闭 pipe writer，通知消费者流结束
		defer lsw.inner.Close()
		defer lsw.finalize(nil)
		for {
			chunk, err := lsw.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					writer.Send(nil, err)
				}
				return
			}
			if writer.Send(chunk, nil) {
				return
			}
		}
	}()

	// Finalizer: 当 consumer 调用 Close 后释放 reader 引用，GC 回收时触发 streamCancel。
	// 这使得支持 ctx 的源（如 Eino adapter 监控 ctx.Done）能及时中断阻塞的 body read。
	// reader 仅被 consumer 持有（goroutine 持有 writer，不持有 reader），
	// consumer Close 后 reader 不可达 → finalizer 运行 → cancel → 源中断。
	runtime.SetFinalizer(reader, func(_ *schema.StreamReader[*schema.Message]) {
		streamCancel()
	})

	return reader
}

// mapChatBuildError 映射构造期错误。
func mapChatBuildError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "Unauthorized"):
		return fmt.Errorf("bailian chat: authentication failed (401): %w", err)
	case strings.Contains(msg, "403"), strings.Contains(msg, "Forbidden"):
		return fmt.Errorf("bailian chat: access forbidden (403): %w", err)
	case strings.Contains(msg, "404"), strings.Contains(msg, "not found"):
		return fmt.Errorf("bailian chat: model or endpoint not found (404): %w", err)
	case strings.Contains(msg, "429"), strings.Contains(msg, "rate limit"):
		return fmt.Errorf("bailian chat: rate limited (429): %w", err)
	case strings.Contains(msg, "5"):
		// 宽泛匹配 5xx
		if strings.Contains(msg, "500") || strings.Contains(msg, "502") ||
			strings.Contains(msg, "503") || strings.Contains(msg, "504") {
			return fmt.Errorf("bailian chat: server error: %w", err)
		}
	}
	return fmt.Errorf("bailian chat: build failed: %w", err)
}

// mapChatError 映射运行期错误（4xx/5xx → 明确错误）。
func mapChatError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized"):
		return fmt.Errorf("bailian chat: authentication failed (401): %w", err)
	case strings.Contains(msg, "403") || strings.Contains(msg, "Forbidden"):
		return fmt.Errorf("bailian chat: access forbidden (403): %w", err)
	case strings.Contains(msg, "404") || strings.Contains(msg, "not found"):
		return fmt.Errorf("bailian chat: model or endpoint not found (404): %w", err)
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit"):
		return fmt.Errorf("bailian chat: rate limited (429): %w", err)
	case strings.Contains(msg, "500") || strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") || strings.Contains(msg, "504"):
		return fmt.Errorf("bailian chat: server error: %w", err)
	}
	return err
}
