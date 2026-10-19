package rag

import (
	"context"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B5: ProcessBudget 由进程 owner 持有，跨 Registry 重建复用同一实例（不刷新）
// ---------------------------------------------------------------------------

// budgetOf 从 Registry 提取绑定的 ProcessBudget（registry 实现暴露 Budget()）。
func budgetOf(t *testing.T, reg providers.ProviderRegistry) *providers.ProcessBudget {
	t.Helper()
	bc, ok := reg.(interface {
		Budget() *providers.ProcessBudget
	})
	if !ok {
		t.Fatal("registry does not expose Budget()")
	}
	b := bc.Budget()
	if b == nil {
		t.Fatal("registry Budget() returned nil")
	}
	return b
}

// TestOwner_ProcessBudgetNotRefresh 验证连续两次 buildRegistry 复用同一 ProcessBudget：
// 第一次的累计结算在第二次仍然可见（进程预算不随 Registry 重建刷新）。
func TestOwner_ProcessBudgetNotRefresh(t *testing.T) {
	ctx := context.Background()

	// 隔离：重置进程级 processBudget，测试后恢复。
	processBudgetMu.Lock()
	old := processBudget
	processBudget = nil
	processBudgetMu.Unlock()
	defer func() {
		processBudgetMu.Lock()
		processBudget = old
		processBudgetMu.Unlock()
	}()

	pc := &config.ProviderConfig{
		Name:        "openai",
		BaseURL:     "http://localhost:1/v1",
		ChatModel:   "gpt-test",
		APIKey:      "test-key",
		MaxRequests: 5,
	}

	reg1, err := buildRegistry(ctx, pc)
	if err != nil {
		t.Fatalf("first buildRegistry: %v", err)
	}
	b1 := budgetOf(t, reg1)

	// 针对第一次的 ProcessBudget 累计一次 committed。
	rb, err := b1.NewRequestBudget(ctx)
	if err != nil {
		t.Fatalf("NewRequestBudget: %v", err)
	}
	lease, err := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 20})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := lease.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	lease.Finish(nil)
	if got := b1.Snapshot().CommittedAttempts; got != 1 {
		t.Fatalf("b1 CommittedAttempts = %d, want 1", got)
	}

	// 第二次重建 Registry。
	reg2, err := buildRegistry(ctx, pc)
	if err != nil {
		t.Fatalf("second buildRegistry: %v", err)
	}
	b2 := budgetOf(t, reg2)

	// 同一实例：进程 owner 持有，跨 Registry 重建复用。
	if b1 != b2 {
		t.Error("ProcessBudget must be reused across Registry rebuild, got a fresh instance")
	}
	// 累计不刷新：第二次仍看到第一次的结算。
	if got := b2.Snapshot().CommittedAttempts; got != 1 {
		t.Errorf("b2 CommittedAttempts = %d, want 1 (accumulation must persist)", got)
	}
}
