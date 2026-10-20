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

	// 包装 Pipe 转发，附加预算 lease 追踪。
	// P1-2：传入父 ctx，使父 ctx 单独取消（消费者未显式 Close/Drain）也能收敛。
	return WrapBudgetStreamReader(ctx, streamCancel, innerReader, lease, innerSession), nil
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

// WrapBudgetStreamReader 包装 StreamReader，返回 *StreamSession 供业务消费。
//
// 架构：recv supervisor goroutine + 临时 Recv goroutine + 转发 goroutine + WaitGroup 跟踪。
//   - recv supervisor：循环起一个临时 goroutine 执行阻塞的 inner Recv，结果进 dataCh（buffer=1）。
//     supervisor 自身 select 监听 recvDone 与 cancelSig；当 inner 返回 EOF/错误或 cancelSig
//     关闭时退出。
//   - 临时 Recv goroutine（P1-2）：每一个都登记到 WaitGroup。done 关闭时保证没有
//     goroutine 仍停在 bsr.Recv 中；否则调用方会误以为流已完全回收，而实际上仍有
//     一个阻塞的 Recv 持有上游连接。
//   - 转发 goroutine：select { dataCh → writer; cancelSig → return }。
//     负责关闭内层 session/reader 和终结 lease。
//   - waiter goroutine：等待 WaitGroup 归零（supervisor + 全部临时 goroutine + 转发）后关闭 done。
//
// done channel 覆盖所有任务（supervisor + 临时 Recv + forward），确保：
//   - StreamSession.Close()/Drain() 等待所有 goroutine 退出
//   - 父 ctx 取消 + 背压也能收敛
//   - 终结者和终态唯一
//
// 临时 goroutine 的退出依赖源 reader 的 writer 关闭：
// cancel() → streamCancel() 取消 derivedCtx → 生产环境 HTTP body Read 返回错误 →
// 内层 writer 关闭 → Recv 返回 EOF → 临时 goroutine 退出 → wg 归零 → done 关闭。
// 注意：closeSrc()（StreamReader.Close → closeRecv）只能解除阻塞的 Send，不能解除
// 阻塞的 Recv，因此真正的中断靠 streamCancel()。
//
// streamCancel 是派生 ctx 的取消函数。cancel 时：
//  1. streamCancel → 取消内层 ctx（生产环境中断 HTTP body read）
//  2. close(cancelSig) → 解除 supervisor/forward goroutine 的 select 阻塞
//  3. reader.Close() → 解除 forward 在 writer.Send 上的背压阻塞
//  4. closeSrc → 关闭源 reader，解除源生产者的 Send 阻塞并使其关闭 writer
//  5. 等待 waiter goroutine 确认所有任务退出
//
// parentCtx 是调用方传入 StreamSession 的原始 ctx（streamCtx 的父）。P1-2：监听它，
// 在它取消时主动调 cancel()，不依赖消费者显式调用 Close/Drain；因此“仅取消父 ctx
// 然后等 Done”也能收敛。
// watcher goroutine 不登记到 WaitGroup（否则与 waiter 形成循环等待），它在
// parentCtx.Done / cancelSig / done 任一发生时退出，不泄漏。
//
// 不使用 runtime.SetFinalizer，正确性由 StreamSession.Close/Drain 与 parentCtx watcher 保证。
func WrapBudgetStreamReader(parentCtx context.Context, streamCancel context.CancelFunc, sr *schema.StreamReader[*schema.Message], lease *AttemptLease, innerSession *StreamSession) *StreamSession {
	reader, writer := schema.Pipe[*schema.Message](1)
	// 输出 pipe reader 有多个关闭者：cancel()（含 parentCtx watcher 触发的）、
	// StreamSession.Close()/Drain()、以及业务侧消费者的提前 Close。
	// SetAutomaticClose 使底层 closeRecv 走 atomic CAS，重复 Close 不会触发
	// close(s.closed) panic。必须在分发 reader 之前调用（该 API 非并发安全）。
	reader.SetAutomaticClose()
	bsr := &budgetStreamReader{inner: sr, lease: lease}
	done := make(chan struct{})      // 所有 goroutine 退出信号
	cancelSig := make(chan struct{}) // cancel 信号

	if parentCtx == nil {
		parentCtx = context.Background()
	}

	// closeSrcOnce protects source close idempotency.
	// Both cancel() and forward goroutine defer call this.
	// Eino StreamReader.Close() is NOT idempotent (close(s.closed) panics on double-close).
	var closeSrcOnce sync.Once
	closeSrc := func() {
		closeSrcOnce.Do(func() {
			if innerSession != nil {
				innerSession.Close()
			} else {
				sr.Close()
			}
		})
	}

	// cancelOnce protects close(cancelSig) idempotency (concurrent cancel safe)
	var cancelOnce sync.Once
	// cancel 逐步解除全部阻塞点，顺序按“先不阻塞的信号、后可能阻塞的回收”排列：
	//  1. streamCancel：取消 derivedCtx，中断底层 HTTP body read（解除阻塞 Recv 的唯一手段）
	//  2. close(cancelSig)：解除 supervisor / forward 的 select 阻塞
	//  3. reader.Close()：closeRecv 使 forward 阻塞中的 writer.Send 立即返回 true
	//     （否则消费者停止读取 + 输出管道满时 forward 会永久阻塞，done 不会关闭）
	//  4. closeSrc：回收源；innerSession.Close() 会等内层 done，放在最后避免卡住前三步
	//
	// 注意：closeSrc（StreamReader.Close → closeRecv）只能解除阻塞的 Send，不能解除
	// 阻塞的 Recv；解除阻塞 Recv 靠第 1 步的 streamCancel。
	cancel := func() {
		streamCancel() // 1. cancel derived ctx
		cancelOnce.Do(func() {
			close(cancelSig) // 2. Signal cancel
		})
		reader.Close() // 3. 解除 forward 在 writer.Send 上的背压阻塞（CAS 幂等）
		closeSrc()     // 4. Close source reader to unblock the source producer's Send
	}

	// WaitGroup tracks the recv supervisor and the forward goroutine.
	// 临时 Recv goroutine 在 supervisor 内部 wg.Add(1) 动态登记；因为 supervisor 自身
	// 尚未 Done，计数永不会在 Add 时归零，因此不会触发 WaitGroup misuse。
	var wg sync.WaitGroup
	wg.Add(2)

	// parent ctx watcher（P1-2）：父 ctx 取消时主动 cancel，使 cancelSig 关闭、
	// closeSrc 触发，不依赖消费者显式调用 Close/Drain。
	go func() {
		select {
		case <-parentCtx.Done():
			cancel()
		case <-cancelSig:
			// cancel 已被调用，无需重复
		case <-done:
			// 所有任务已退出，无需再 cancel
		}
	}()

	// recv supervisor goroutine：桥接阻塞 Recv 到 dataCh。
	// 将阻塞的 bsr.Recv() 放入临时 goroutine，外层 select 监听结果和 cancelSig。
	// 当 cancelSig 关闭时 supervisor 立即退出；在飞的临时 goroutine 仍登记在 wg 中，
	// 因此 done 会等到它真正从 bsr.Recv 返回后才关闭。
	type recvItem struct {
		chunk *schema.Message
		err   error
	}
	dataCh := make(chan recvItem, 1)
	go func() {
		defer wg.Done()
		for {
			// 临时 goroutine 执行阻塞 Recv；recvDone 缓冲 1 且每轮只有一个临时
			// goroutine，因此它的发送永不阻塞，退出不会被 supervisor 卡住。
			recvDone := make(chan recvItem, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				chunk, err := bsr.Recv()
				recvDone <- recvItem{chunk, err}
			}()
			// 等待 Recv 结果或 cancel 信号
			select {
			case item := <-recvDone:
				select {
				case dataCh <- item:
				case <-cancelSig:
					return
				}
				if item.err != nil {
					return
				}
			case <-cancelSig:
				return
			}
		}
	}()

	// 转发 goroutine：select 监听数据和 cancelSig。
	go func() {
		defer wg.Done()
		defer writer.Close() // 关闭 pipe writer，通知消费者流结束
		// Close source: closeSrc is idempotent via sync.Once (safe even if cancel already closed it).
		defer closeSrc()
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

	// waiter goroutine：等待 supervisor、全部临时 Recv goroutine 和 forward 都退出后关闭 done。
	// 这确保 done 覆盖所有任务，而非仅 forward。
	go func() {
		wg.Wait()
		close(done)
	}()

	return NewStreamSession(reader, cancel, done, lease)
}
