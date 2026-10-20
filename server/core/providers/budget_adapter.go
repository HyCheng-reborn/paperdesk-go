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
	session, err := b.StreamSession(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return session.Reader(), nil
}

// StreamSession 返回受控流会话，管理预算 lease 生命周期。
// 内层 ChatRole 若实现 StreamSessioner，则使用其会话；否则回退到普通 Stream。
func (b *budgetChatRole) StreamSession(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*StreamSession, error) {
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

	// 创建派生 ctx，传递给内层 Stream/StreamSession。
	// cancel 时取消此 ctx → 内层 writer 关闭 → 转发 goroutine 的 Recv 解除阻塞。
	streamCtx, streamCancel := context.WithCancel(ctx)

	// 获取内层 reader：优先使用 StreamSessioner 接口
	var innerReader *schema.StreamReader[*schema.Message]
	var innerSession *StreamSession
	if ss, ok := b.inner.(StreamSessioner); ok {
		var sErr error
		innerSession, sErr = ss.StreamSession(streamCtx, messages, opts...)
		if sErr != nil {
			streamCancel()
			if lease != nil {
				lease.Abort(sErr)
			}
			return nil, sErr
		}
		innerReader = innerSession.Reader()
	} else {
		// 回退：直接调用 inner.Stream
		var rErr error
		innerReader, rErr = b.inner.Stream(streamCtx, messages, opts...)
		if rErr != nil {
			streamCancel()
			if lease != nil {
				lease.Abort(rErr)
			}
			return nil, rErr
		}
	}

	// 包装 Pipe 转发，附加预算 lease 追踪
	return wrapBudgetStreamReader(streamCancel, innerReader, lease, innerSession), nil
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
// 用于内部 lease 追踪；不直接返回给业务。
type budgetStreamReader struct {
	inner     *schema.StreamReader[*schema.Message]
	lease     *AttemptLease
	once      sync.Once
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

func (w *budgetStreamReader) finalize(err error) {
	w.once.Do(func() {
		if w.lease == nil {
			return // 无 lease 时 finalize 为 no-op
		}
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

// wrapBudgetStreamReader 包装 StreamReader，返回 *StreamSession 供业务消费。
//
// 架构：recv goroutine + 转发 goroutine 分离模式。
//   - recv goroutine：阻塞在 inner Recv，将结果发送到 dataCh（buffer=1，永不阻塞）。
//     当 inner 返回 EOF/错误时退出。
//   - 转发 goroutine：select { dataCh → budgetWriter; done → return }。
//     当 cancel 关闭 done 时立即退出，不阻塞在 inner Recv。
//
// 这解决了 Eino StreamReader.Recv() 只响应 writer 关闭（closeSend），
// 不响应 reader 关闭（closeRecv）或 ctx 取消的问题。
//
// streamCancel 是派生 ctx 的取消函数。cancel 时：
//  1. streamCancel → 取消内层 ctx（生产环境中断 HTTP body read）
//  2. 关闭内层 session（若有）释放底层 HTTP 连接
//  3. 关闭 done → 转发 goroutine 立即退出
//
// recv goroutine 在 inner producer 响应 ctx 取消后自然退出。
// 不使用 runtime.SetFinalizer，正确性由 StreamSession.Close/Drain 保证。
func wrapBudgetStreamReader(streamCancel context.CancelFunc, sr *schema.StreamReader[*schema.Message], lease *AttemptLease, innerSession *StreamSession) *StreamSession {
	reader, writer := schema.Pipe[*schema.Message](1)
	bsr := &budgetStreamReader{inner: sr, lease: lease}
	done := make(chan struct{})      // 转发 goroutine 退出信号
	cancelSig := make(chan struct{}) // cancel 信号，独立于 done

	// closeInnerOnce 保护内层 reader 幂等关闭（innerSession.Close 和转发 goroutine defer 均可调用）。
	var closeInnerOnce sync.Once
	closeInner := func() {
		closeInnerOnce.Do(func() { sr.Close() })
	}

	// cancel：仅取消派生 ctx + 关闭 cancelSig 解除转发 goroutine。
	// 内层 session 由转发 goroutine 的 defer 关闭（避免与 closeInner 双重关闭同一 reader）。
	cancel := func() {
		streamCancel()
		select {
		case <-cancelSig:
		default:
			close(cancelSig)
		}
	}

	// recv goroutine：桥接阻塞 Recv 到 dataCh。
	type recvItem struct {
		chunk *schema.Message
		err   error
	}
	dataCh := make(chan recvItem, 1)
	go func() {
		for {
			chunk, err := bsr.Recv()
			select {
			case dataCh <- recvItem{chunk, err}:
			case <-cancelSig:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// 转发 goroutine：select 监听数据和 cancelSig。
	go func() {
		defer close(done)    // 通知 StreamSession 转发已完成
		defer writer.Close() // 关闭 pipe writer，通知消费者流结束
		// 关闭内层 session（级联取消 HTTP ctx + 关闭内层 reader + 等待内层 goroutine）。
		// 若 innerSession 为 nil，则直接关闭内层 reader。
		defer func() {
			if innerSession != nil {
				innerSession.Close() // 内部已包含 reader 关闭，不重复调用 closeInner
			} else {
				closeInner() // 无 session 时直接关闭 reader
			}
		}()
		defer bsr.finalize(nil) // 终结 lease（nil lease 时为 no-op）
		for {
			select {
			case item := <-dataCh:
				if item.err != nil {
					if !errors.Is(item.err, io.EOF) {
						writer.Send(nil, item.err)
					}
					return
				}
				if writer.Send(item.chunk, nil) {
					return
				}
			case <-cancelSig:
				return
			}
		}
	}()

	return NewStreamSession(reader, cancel, done, lease)
}
