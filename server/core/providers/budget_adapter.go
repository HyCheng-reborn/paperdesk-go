package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// B4: 预算执行包装层，确保所有通过共享 Build*Role 构造的角色都经过 Reserve/Commit/Finish。
// 与百炼适配器的预算逻辑等价，但作用于通用适配层（覆盖 OpenAI 等所有 Provider）。

// defaultBudgetMaxOutput 是 Chat 预算包装的默认输出 token 预留。
// B3 同源：当 opts 中有 MaxTokens 时使用实际值，否则使用此默认值。
const defaultBudgetMaxOutput int64 = 8192

// ---------------------------------------------------------------------------
// budgetChatRole: Chat 预算包装
// ---------------------------------------------------------------------------

// budgetChatRole 包装 ChatRole，附加 Reserve/Commit/Finish 预算执行。
// B4: 所有通过 BuildChatRole 构造的角色（含 OpenAI）均经此门禁。
type budgetChatRole struct {
	inner   ChatRole
	budget  *ProcessBudget
	profile *ProviderProfile
}

var _ ChatRole = (*budgetChatRole)(nil)

func (b *budgetChatRole) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("providers: empty messages")
	}

	maxOutput := resolveMaxOutputFromOpts(opts)
	est := estimateChatTokens(messages, maxOutput)

	lease, err := b.reserveBudget(ctx, est)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	msg, err := b.inner.Generate(ctx, messages, opts...)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	if lease != nil {
		lease.Finish(extractUsageFromMessage(msg))
	}
	return msg, nil
}

func (b *budgetChatRole) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("providers: empty messages")
	}

	maxOutput := resolveMaxOutputFromOpts(opts)
	est := estimateChatTokens(messages, maxOutput)

	lease, err := b.reserveBudget(ctx, est)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	sr, err := b.inner.Stream(ctx, messages, opts...)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	if lease == nil {
		return sr, nil
	}
	return wrapBudgetStreamReader(sr, lease), nil
}

func (b *budgetChatRole) reserveBudget(ctx context.Context, est *TokenEstimate) (*AttemptLease, error) {
	if b.budget == nil {
		return nil, nil
	}
	rb := RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = b.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("providers: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("providers: budget reserve: %w", err)
	}
	return lease, nil
}

// Profile 委托内层适配器，保留 Stage 1 契约（生产 Registry 传 Budget 时返回本包装器）。
func (b *budgetChatRole) Profile() ProviderProfile {
	if b.profile == nil {
		return ProviderProfile{}
	}
	return b.profile.snapshot()
}

// Capabilities 委托内层 Profile 声明的能力集。
func (b *budgetChatRole) Capabilities() Capabilities {
	if b.profile == nil {
		return Capabilities{}
	}
	return b.profile.Capabilities
}

// ---------------------------------------------------------------------------
// budgetEmbedRole: Embed 预算包装
// ---------------------------------------------------------------------------

// budgetEmbedRole 包装 EmbedRole，附加 Reserve/Commit/Finish 预算执行。
type budgetEmbedRole struct {
	inner   EmbedRole
	budget  *ProcessBudget
	profile *ProviderProfile
}

var _ EmbedRole = (*budgetEmbedRole)(nil)

func (b *budgetEmbedRole) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("providers: empty texts")
	}

	est := estimateEmbedTokens(texts)

	lease, err := b.reserveBudget(ctx, est)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	result, err := b.inner.EmbedStrings(ctx, texts, opts...)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	if lease != nil {
		lease.Finish(nil) // Embedding usage 由具体 Provider 适配器（如 bailian）处理
	}
	return result, nil
}

func (b *budgetEmbedRole) Dimensions() int { return b.inner.Dimensions() }
func (b *budgetEmbedRole) ModelID() string { return b.inner.ModelID() }

// Profile 委托内层适配器，保留 Stage 1 契约。
func (b *budgetEmbedRole) Profile() ProviderProfile {
	if b.profile == nil {
		return ProviderProfile{}
	}
	return b.profile.snapshot()
}

func (b *budgetEmbedRole) reserveBudget(ctx context.Context, est *TokenEstimate) (*AttemptLease, error) {
	if b.budget == nil {
		return nil, nil
	}
	rb := RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = b.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("providers: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("providers: budget reserve: %w", err)
	}
	return lease, nil
}

// ---------------------------------------------------------------------------
// budgetRerankRole: Rerank 预算包装
// ---------------------------------------------------------------------------

// budgetRerankRole 包装 RerankRole，附加 Reserve/Commit/Finish 预算执行。
type budgetRerankRole struct {
	inner   RerankRole
	budget  *ProcessBudget
	profile *ProviderProfile
}

var _ RerankRole = (*budgetRerankRole)(nil)

func (b *budgetRerankRole) Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error) {
	est := estimateRerankTokens(query, docs)

	lease, err := b.reserveBudget(ctx, est)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	result, err := b.inner.Rerank(ctx, query, docs, topK)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	if lease != nil {
		lease.Finish(nil) // Rerank usage 通常不返回 token 计数
	}
	return result, nil
}

func (b *budgetRerankRole) reserveBudget(ctx context.Context, est *TokenEstimate) (*AttemptLease, error) {
	if b.budget == nil {
		return nil, nil
	}
	rb := RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = b.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("providers: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("providers: budget reserve: %w", err)
	}
	return lease, nil
}

// Profile 委托内层适配器，保留 Stage 1 契约。
func (b *budgetRerankRole) Profile() ProviderProfile {
	if b.profile == nil {
		return ProviderProfile{}
	}
	return b.profile.snapshot()
}

// ---------------------------------------------------------------------------
// 共享辅助函数
// ---------------------------------------------------------------------------

// resolveMaxOutputFromOpts 从 model.Option 列表解析实际 MaxTokens（B3 同源）。
func resolveMaxOutputFromOpts(opts []model.Option) int64 {
	resolved := model.GetCommonOptions(&model.Options{}, opts...)
	if resolved != nil && resolved.MaxTokens != nil && *resolved.MaxTokens > 0 {
		return int64(*resolved.MaxTokens)
	}
	return defaultBudgetMaxOutput
}

// estimateChatTokens 从消息估算 token 数。
func estimateChatTokens(messages []*schema.Message, maxOutput int64) *TokenEstimate {
	var inputChars int64
	for _, m := range messages {
		if m != nil {
			inputChars += int64(len(m.Content))
		}
	}
	return &TokenEstimate{
		InputTokens: inputChars/4 + 1,
		MaxOutput:   maxOutput,
	}
}

// estimateEmbedTokens 从文本估算 Embedding token 数。
func estimateEmbedTokens(texts []string) *TokenEstimate {
	var inputChars int64
	for _, t := range texts {
		inputChars += int64(len(t))
	}
	return &TokenEstimate{
		InputTokens: inputChars/4 + 1,
		MaxOutput:   0,
	}
}

// estimateRerankTokens 从 query + docs 估算 Rerank token 数。
func estimateRerankTokens(query string, docs []*schema.Document) *TokenEstimate {
	var inputChars int64 = int64(len(query))
	for _, d := range docs {
		if d != nil {
			inputChars += int64(len(d.Content))
		}
	}
	return &TokenEstimate{
		InputTokens: inputChars/4 + 1,
		MaxOutput:   0,
	}
}

// extractUsageFromMessage 从 schema.Message 提取 TokenUsage。
func extractUsageFromMessage(msg *schema.Message) *model.TokenUsage {
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
// budgetStreamReader: 流式预算终结包装
// ---------------------------------------------------------------------------

// budgetStreamReader 包装 StreamReader，通过 Recv/Close 单一所有权终结 lease。
type budgetStreamReader struct {
	inner     *schema.StreamReader[*schema.Message]
	lease     *AttemptLease
	once      sync.Once
	closeOnce sync.Once
	lastUsage *model.TokenUsage
	mu        sync.Mutex
}

func (w *budgetStreamReader) Recv() (*schema.Message, error) {
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

func (w *budgetStreamReader) Close() {
	w.closeOnce.Do(func() {
		w.inner.Close()
	})
	w.finalize(nil)
}

func (w *budgetStreamReader) finalize(err error) {
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

// wrapBudgetStreamReader 包装 StreamReader，使用 budgetStreamReader 单一所有权终结。
// 所有权链：下游 Close/取消 → writer.Send 返回 true → 源 Close → lease 终结 → goroutine 退出。
func wrapBudgetStreamReader(sr *schema.StreamReader[*schema.Message], lease *AttemptLease) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	bsr := &budgetStreamReader{inner: sr, lease: lease}

	go func() {
		defer writer.Close()
		for {
			chunk, err := bsr.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					writer.Send(nil, err)
				}
				return
			}
			// Eino v0.6.0: Send 返回 true 表示 pipe 已关闭（下游 Close/取消）
			if writer.Send(chunk, nil) {
				bsr.Close()
				return
			}
		}
	}()

	return reader
}
