package providers

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// B5: 后台显式移交（Index 同步 → 后台共享同一累计 owner，不刷新）
// ---------------------------------------------------------------------------

// TestOwner_IndexBackgroundTransfer 验证 Index 场景的 TaskBudget 显式移交契约：
//  1. 同步侧预留并结算一次；
//  2. TransferToBackground 返回共享同一 scope 的后台句柄（累计不刷新）；
//  3. 后台继续结算，同步侧句柄可见同一累计（同一 scope）；
//  4. 移交后同步侧 Reserve 返回 ErrBudgetTransferred（不再持有活跃引用）。
func TestOwner_IndexBackgroundTransfer(t *testing.T) {
	ctx := context.Background()
	pb, err := NewProcessBudget(10, 0, 0, 0)
	if err != nil {
		t.Fatalf("NewProcessBudget: %v", err)
	}

	tb, err := pb.NewTaskBudget(ctx, WithMaxRequests(5))
	if err != nil {
		t.Fatalf("NewTaskBudget: %v", err)
	}

	// 同步阶段结算一次。
	settleOnce(t, tb)
	if got := tb.Snapshot().CommittedAttempts; got != 1 {
		t.Fatalf("sync CommittedAttempts = %d, want 1", got)
	}

	// 显式移交后台。
	bg, err := tb.TransferToBackground()
	if err != nil {
		t.Fatalf("TransferToBackground: %v", err)
	}
	if bg == nil {
		t.Fatal("TransferToBackground returned nil")
	}

	// 累计不刷新：后台句柄继承同步阶段的一次 committed。
	if got := bg.Snapshot().CommittedAttempts; got != 1 {
		t.Errorf("background CommittedAttempts = %d, want 1 (must inherit, not refresh)", got)
	}

	// 后台继续结算一次。
	settleOnceTask(t, bg)

	// 同一 scope：两侧都看到累计 2 次（共享，非各自独立）。
	if got := bg.Snapshot().CommittedAttempts; got != 2 {
		t.Errorf("background CommittedAttempts = %d, want 2", got)
	}
	if got := tb.Snapshot().CommittedAttempts; got != 2 {
		t.Errorf("sync-side CommittedAttempts = %d, want 2 (shared scope)", got)
	}

	// 移交后同步侧句柄失效：Reserve 返回 ErrBudgetTransferred。
	_, err = tb.Reserve(&TokenEstimate{InputTokens: 1, MaxOutput: 1})
	if !errors.Is(err, ErrBudgetTransferred) {
		t.Errorf("sync-side Reserve after transfer = %v, want ErrBudgetTransferred", err)
	}

	// 重复移交被拒。
	if _, err := tb.TransferToBackground(); !errors.Is(err, ErrBudgetTransferred) {
		t.Errorf("re-transfer = %v, want ErrBudgetTransferred", err)
	}
}

// settleOnce 在 TaskBudget 上完成一次 Reserve→Commit→Finish。
func settleOnce(t *testing.T, tb *TaskBudget) {
	t.Helper()
	lease, err := tb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 20})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := lease.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	lease.Finish(nil)
}

// settleOnceTask 是 settleOnce 的别名，用于后台句柄（语义清晰）。
func settleOnceTask(t *testing.T, tb *TaskBudget) {
	t.Helper()
	settleOnce(t, tb)
}
