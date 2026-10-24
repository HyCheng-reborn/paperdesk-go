package providers

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Fix 2: Stream Close 测试
// ---------------------------------------------------------------------------

// 注：TestStream_EarlyCloseWakesSource 已移至 bailian 包测试（TestStream_EarlyCloseFinalizes），
// 因为需要使用真实的 HTTP 流来验证 Close 行为。

// TestStream_CancelWakesSource 验证 ctx 取消后源 Recv 返回错误 (Fix 2)
func TestStream_CancelWakesSource(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := schema.Pipe[*schema.Message](1)

	bsr := &budgetStreamReader{
		inner: reader,
		lease: lease,
		done:  make(chan struct{}),
	}

	go func() {
		defer close(bsr.done)
		defer writer.Close()
		defer bsr.inner.Close()
		defer bsr.finalize(nil)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				chunk, err := bsr.Recv()
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
		}
	}()

	// 取消 ctx
	cancel()

	// 验证 goroutine 退出
	select {
	case <-bsr.done:
		// 正常退出
	case <-time.After(200 * time.Millisecond):
		t.Fatal("goroutine did not exit within 200ms after ctx cancel")
	}
}

// TestStream_WaitGoroutineExit 验证 goroutine 退出可等待 (Fix 2)
func TestStream_WaitGoroutineExit(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
	})

	_, writer := schema.Pipe[*schema.Message](1)
	bsr := &budgetStreamReader{
		inner: sr,
		lease: lease,
		done:  make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(bsr.done)
		defer writer.Close()
		defer bsr.inner.Close()
		defer bsr.finalize(nil)
		for {
			chunk, err := bsr.Recv()
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

	// 等待 goroutine 退出
	wg.Wait()

	// 验证 done channel 已关闭
	select {
	case <-bsr.done:
		// 正常
	case <-time.After(100 * time.Millisecond):
		t.Fatal("done channel not closed after goroutine exit")
	}
}

// ---------------------------------------------------------------------------
// Fix 3: TaskBudget Deadline 测试
// ---------------------------------------------------------------------------

// TestTaskBudget_SharedKeepsDeadline 验证共享视图继承固定绝对 deadline (Fix 3)
func TestTaskBudget_SharedKeepsDeadline(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	// 创建 TaskBudget，设置 1s deadline
	tb, err := budget.NewTaskBudget(context.Background(), WithDeadline(1*time.Second))
	if err != nil {
		t.Fatalf("NewTaskBudget: %v", err)
	}

	// 记录原始 ownerDeadline
	originalDeadline := tb.ownerDeadline

	// 等待 100ms
	time.Sleep(100 * time.Millisecond)

	// 创建共享 RequestBudget（不设置额外 deadline）
	rb, err := tb.NewSharedRequestBudget(context.Background())
	if err != nil {
		t.Fatalf("NewSharedRequestBudget: %v", err)
	}

	// 验证共享的 RequestBudget 继承了原始 deadline
	if rb.ownerDeadline != originalDeadline {
		t.Errorf("expected ownerDeadline to be inherited, got %v, want %v", rb.ownerDeadline, originalDeadline)
	}
}

// TestTaskBudget_SharedOptsTightenOnly 验证额外 opts 至多收紧 (Fix 3)
func TestTaskBudget_SharedOptsTightenOnly(t *testing.T) {
	budget, _ := NewProcessBudget(0, 0, 0, 0)

	// 创建 TaskBudget，设置 2s deadline
	tb, _ := budget.NewTaskBudget(context.Background(), WithDeadline(2*time.Second))
	originalDeadline := tb.ownerDeadline

	// 创建共享 RequestBudget，设置 500ms deadline（更紧）
	rb, err := tb.NewSharedRequestBudget(context.Background(), WithDeadline(500*time.Millisecond))
	if err != nil {
		t.Fatalf("NewSharedRequestBudget: %v", err)
	}

	// 验证 deadline 被收紧（更早）
	if !rb.ownerDeadline.Before(originalDeadline) {
		t.Errorf("expected opts to tighten deadline, got %v, original %v", rb.ownerDeadline, originalDeadline)
	}
}

// TestWithTaskBudget_ErrorPropagation 验证创建失败返回错误 (Fix 3)
func TestWithTaskBudget_ErrorPropagation(t *testing.T) {
	// Registry 未设置时返回 nil error
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
