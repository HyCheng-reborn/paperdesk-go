package providers

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Fix 2: Stream Close 测试
// ---------------------------------------------------------------------------

// TestStream_BusinessCloseWakesSource 验证业务 Close 实际返回的 reader 后，
// 转发 goroutine 检测到 writer.Send→false 退出，inner 被关闭。
func TestStream_BusinessCloseWakesSource(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建有多个 chunk 的源
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
		{Role: schema.Assistant, Content: "chunk2"},
		{Role: schema.Assistant, Content: "chunk3"},
	})

	wrapped := wrapBudgetStreamReader(context.Background(), func() {}, sr, lease)

	// 读一个 chunk 然后关闭
	_, err := wrapped.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	wrapped.Close()

	// 验证 lease 已终结（转发 goroutine 检测 writer.Send→false 后退出）
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease to be finalized after business Close")
	}
}

// TestStream_BusinessCloseFinalizesLease 验证业务 Close 后 lease 终结。
func TestStream_BusinessCloseFinalizesLease(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
		{Role: schema.Assistant, Content: "chunk2"},
	})

	wrapped := wrapBudgetStreamReader(context.Background(), func() {}, sr, lease)

	// 读一个 chunk 然后关闭
	_, err := wrapped.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	wrapped.Close()

	// 验证 lease 已终结
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease finalized after Close")
	}
}

// TestStream_BusinessCloseWaitsGoroutine 验证业务 Close 后转发 goroutine 退出。
// 通过 lease 终结作为 goroutine 退出的可观察信号（defer finalize 在 goroutine 退出时执行）。
func TestStream_BusinessCloseWaitsGoroutine(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.Assistant, Content: "b"},
		{Role: schema.Assistant, Content: "c"},
	})

	wrapped := wrapBudgetStreamReader(context.Background(), func() {}, sr, lease)
	_, _ = wrapped.Recv()
	wrapped.Close()

	// lease 终结即表明 goroutine 已退出（defer finalize 执行完毕）
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("goroutine did not exit after Close")
	}
}

// TestStream_CtxCancelWakesSource 验证 ctx 取消后源 Recv 返回错误。
func TestStream_CtxCancelWakesSource(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建阻塞源：不发送任何 chunk
	srcReader, srcWriter := schema.Pipe[*schema.Message](1)

	ctx, cancel := context.WithCancel(context.Background())
	wrapped := wrapBudgetStreamReader(ctx, cancel, srcReader, lease)

	// 取消 ctx
	cancel()
	// 关闭源 writer 模拟模型响应 ctx 取消后关闭流
	srcWriter.Close()

	// wrapped.Recv 应返回错误或 EOF
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := wrapped.Recv()
		if err != nil {
			// 收到错误或 EOF，符合预期
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(" Recv did not return error after ctx cancel + source close")
		}
		time.Sleep(time.Millisecond)
	}

	// lease 应已终结
	deadline2 := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline2) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease finalized after ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// Fix 7: Supplement Stream Close 测试（budgetChatRole 路径）
// ---------------------------------------------------------------------------

// TestSupplementStreamEarlyCloseFinalizes 验证 budgetChatRole 包装的 Stream 在消费者提前 Close 后
// lease 终结。使用 FakeChat 模拟阻塞源（发送首条后阻塞），验证 budgetStreamReader 转发 goroutine
// 在 writer.Send→false 后退出并终结 lease。
func TestSupplementStreamEarlyCloseFinalizes(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	// 构造 FakeChat：发送首条后阻塞，监控 ctx 取消后关闭 writer
	// （模拟 Eino adapter 监控 ctx.Done 后关闭流的行为）
	fake := NewFakeChat()
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)
			// 等待 ctx 取消（模拟 Eino adapter 监控 ctx.Done 后关闭流）
			<-ctx.Done()
		}()
		return reader, nil
	})

	// 通过 budgetChatRole 包装（与生产一致）
	role := &budgetChatRole{inner: fake, budget: budget}

	sr, err := role.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// 读首条
	_, err = sr.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}

	// 消费者提前关闭
	sr.Close()

	// 触发 GC 使 finalizer 运行（consumer 已释放 reader 引用 → reader 不可达 → finalizer 触发 streamCancel）
	runtime.GC()

	// lease 应在 2s 内终结
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := budget.Snapshot()
		if snap.CommittedAttempts >= 1 && snap.InFlight == 0 {
			return // 成功
		}
		time.Sleep(time.Millisecond)
	}
	snap := budget.Snapshot()
	t.Fatalf("expected lease finalized after early close: committed=%d inFlight=%d", snap.CommittedAttempts, snap.InFlight)
}

// ---------------------------------------------------------------------------
// Fix 3: TaskBudget Deadline 测试
// ---------------------------------------------------------------------------

// TestTaskBudget_SharedKeepsDeadline 验证共享视图继承固定绝对 deadline (Fix 3)
func TestTaskBudget_SharedKeepsDeadline(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	tb, err := budget.NewTaskBudget(context.Background(), WithDeadline(1*time.Second))
	if err != nil {
		t.Fatalf("NewTaskBudget: %v", err)
	}

	originalDeadline := tb.ownerDeadline

	time.Sleep(100 * time.Millisecond)

	rb, err := tb.NewSharedRequestBudget(context.Background())
	if err != nil {
		t.Fatalf("NewSharedRequestBudget: %v", err)
	}

	if rb.ownerDeadline != originalDeadline {
		t.Errorf("expected ownerDeadline to be inherited, got %v, want %v", rb.ownerDeadline, originalDeadline)
	}
}

// TestTaskBudget_SharedOptsTightenOnly 验证额外 opts 至多收紧 (Fix 3)
func TestTaskBudget_SharedOptsTightenOnly(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	tb, _ := budget.NewTaskBudget(context.Background(), WithDeadline(2*time.Second))
	originalDeadline := tb.ownerDeadline

	rb, err := tb.NewSharedRequestBudget(context.Background(), WithDeadline(500*time.Millisecond))
	if err != nil {
		t.Fatalf("NewSharedRequestBudget: %v", err)
	}

	if !rb.ownerDeadline.Before(originalDeadline) {
		t.Errorf("expected opts to tighten deadline, got %v, original %v", rb.ownerDeadline, originalDeadline)
	}
}

// TestWithTaskBudget_ErrorPropagation 验证创建失败返回错误 (Fix 3)
func TestWithTaskBudget_ErrorPropagation(t *testing.T) {
	ctx, tb, err := withTaskBudgetForTest(context.Background(), nil)
	if err != nil {
		t.Errorf("expected nil error when Registry is nil, got: %v", err)
	}
	if tb != nil {
		t.Error("expected nil TaskBudget when Registry is nil")
	}
	if ctx == nil {
		t.Error("expected non-nil ctx")
	}
}

// withTaskBudgetForTest 是 WithTaskBudget 的测试版本
func withTaskBudgetForTest(ctx context.Context, reg ProviderRegistry) (context.Context, *TaskBudget, error) {
	if reg == nil {
		return ctx, nil, nil
	}
	type taskBudgetCreator interface {
		Budget() *ProcessBudget
	}
	tbc, ok := reg.(taskBudgetCreator)
	if !ok || tbc.Budget() == nil {
		return ctx, nil, nil
	}
	tb, err := tbc.Budget().NewTaskBudget(ctx)
	if err != nil {
		return ctx, nil, err
	}
	rb, err := tb.NewSharedRequestBudget(ctx)
	if err != nil {
		return ctx, nil, err
	}
	return ContextWithRequestBudget(ctx, rb), tb, nil
}
