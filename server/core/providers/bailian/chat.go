// Package bailian 提供阿里云百炼（DashScope）的 Provider 适配器实现。
// 走 OpenAI 兼容协议（/compatible-mode/v1），复用 eino-ext 底层，
// 附加百炼特有的错误映射、usage 保守处理与响应验证。
package bailian

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// 委托给 StreamSession，返回 reader 保持 ChatRole 接口兼容。
func (a *chatAdapter) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	session, err := a.StreamSession(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return session.Reader(), nil
}

// StreamSession 返回受控流会话，管理完整生命周期（derivedCtx、lease、转发 goroutine）。
// 无预算时 lease 为 nil，转发 goroutine 的 defer finalize 是 no-op。
func (a *chatAdapter) StreamSession(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*providers.StreamSession, error) {
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
	streamCtx, streamCancel := context.WithCancel(ctx)
	sr, err := a.inner.Stream(streamCtx, messages, opts...)
	if err != nil {
		streamCancel()
		if lease != nil {
			lease.Abort(err)
		}
		return nil, mapChatError(err)
	}

	// 统一 Pipe 转发路径（无论有无 lease）。
	// P1-2: 传入父 ctx，使父 ctx 单独取消（消费者未显式 Close/Drain）也能收敛。
	return providers.WrapBudgetStreamReader(ctx, streamCancel, sr, lease, nil), nil
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
