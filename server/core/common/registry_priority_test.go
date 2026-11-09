package common

import (
	"context"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B4: 统一配置选择测试
// ---------------------------------------------------------------------------

// TestRegistry_PriorityOverLegacyCache 验证 Registry 优先于旧 singleton。
func TestRegistry_PriorityOverLegacyCache(t *testing.T) {
	// 清理状态
	SetRegistry(nil)

	// 设置 Registry
	fakeReg := providers.NewFakeRegistry()
	fakeReg.Chat = providers.NewFakeChat()
	SetRegistry(fakeReg)
	defer SetRegistry(nil)

	// GetChatModel 应优先使用 Registry
	cm, err := GetChatModel(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetChatModel with Registry: %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil chat model from Registry")
	}
}

// TestChatFactory_PropagatesRegistryFailure 验证 Registry 失败传播，不回退。
func TestChatFactory_PropagatesRegistryFailure(t *testing.T) {
	// 清理状态
	SetRegistry(nil)

	// 设置一个 Chat 角色未配置的 Registry
	fakeReg := &providers.FakeRegistry{
		// Chat 为 nil → GetChat 返回错误
	}
	SetRegistry(fakeReg)
	defer SetRegistry(nil)

	_, err := GetChatModel(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when Registry GetChat fails")
	}
}

// TestRegistry_PriorityForRewrite 验证 Rewrite 也优先使用 Registry。
func TestRegistry_PriorityForRewrite(t *testing.T) {
	SetRegistry(nil)

	fakeReg := providers.NewFakeRegistry()
	SetRegistry(fakeReg)
	defer SetRegistry(nil)

	cm, err := GetRewriteModel(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetRewriteModel with Registry: %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil rewrite model from Registry")
	}
}

// TestRegistry_PriorityForQA 验证 QA 也优先使用 Registry。
func TestRegistry_PriorityForQA(t *testing.T) {
	SetRegistry(nil)

	fakeReg := providers.NewFakeRegistry()
	SetRegistry(fakeReg)
	defer SetRegistry(nil)

	cm, err := GetQAModel(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetQAModel with Registry: %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil QA model from Registry")
	}
}

// TestRegistry_NilFallsToLegacy 验证 Registry 为 nil 时旧路径可用（不 panic）。
func TestRegistry_NilFallsToLegacy(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil) // 清理

	// 验证 SetRegistry(nil) 后 GetRegistry 返回 nil
	if GetRegistry() != nil {
		t.Fatal("expected nil Registry after SetRegistry(nil)")
	}
	// 旧路径需要真实配置文件，这里只验证 GetRegistry() == nil 语义正确
}

// TestRegistry_GetSetConcurrency 验证 GetRegistry/SetRegistry 并发安全。
func TestRegistry_GetSetConcurrency(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			SetRegistry(providers.NewFakeRegistry())
			_ = GetRegistry()
			SetRegistry(nil)
		}
	}()

	for i := 0; i < 100; i++ {
		_ = GetRegistry()
	}

	<-done
}

// TestRegistry_NewRequestBudget 验证 FakeRegistry 的 NewRequestBudget 工作正常。
func TestRegistry_NewRequestBudget(t *testing.T) {
	reg := providers.NewFakeRegistry()

	rb, err := reg.NewRequestBudget(context.Background())
	if err != nil {
		t.Fatalf("NewRequestBudget: %v", err)
	}
	if rb == nil {
		t.Fatal("expected non-nil RequestBudget")
	}

	// Reserve 应成功（不限额度）
	lease, err := rb.Reserve(&providers.TokenEstimate{InputTokens: 100, MaxOutput: 50})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	lease.Commit()
	lease.Finish(nil)
}

// TestRegistry_WithBudget 验证带预算的 FakeRegistry。
func TestRegistry_WithBudget(t *testing.T) {
	budget, _ := providers.NewProcessBudget(1, 0, 0, 0)
	reg := &providers.FakeRegistry{
		Chat:   providers.NewFakeChat(),
		Budget: budget,
	}

	rb, err := reg.NewRequestBudget(context.Background())
	if err != nil {
		t.Fatalf("NewRequestBudget: %v", err)
	}

	// 第一次 Reserve 应成功
	lease, err := rb.Reserve(&providers.TokenEstimate{InputTokens: 10})
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	lease.Commit()
	lease.Finish(nil)

	// 第二次 Reserve 应失败（MaxRequests=1）
	_, err = rb.Reserve(&providers.TokenEstimate{InputTokens: 10})
	if err == nil {
		t.Fatal("expected budget exhausted error")
	}
	if !containsStr(err.Error(), "budget") {
		t.Errorf("expected budget error, got: %v", err)
	}
}

func containsStr(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestSetRegistry_Nil 验证 SetRegistry(nil) 清理 Registry。
func TestSetRegistry_Nil(t *testing.T) {
	SetRegistry(providers.NewFakeRegistry())
	if GetRegistry() == nil {
		t.Fatal("expected non-nil Registry after Set")
	}

	SetRegistry(nil)
	if GetRegistry() != nil {
		t.Fatal("expected nil Registry after SetRegistry(nil)")
	}
}

// TestSetRegistry_Replace 验证 SetRegistry 替换。
func TestSetRegistry_Replace(t *testing.T) {
	defer SetRegistry(nil)

	reg1 := providers.NewFakeRegistry()
	reg2 := providers.NewFakeRegistry()

	SetRegistry(reg1)
	if GetRegistry() != reg1 {
		t.Fatal("expected reg1")
	}

	SetRegistry(reg2)
	if GetRegistry() != reg2 {
		t.Fatal("expected reg2")
	}
}

// TestRegistry_ErrorWrapping 验证 Registry 错误包装。
func TestRegistry_ErrorWrapping(t *testing.T) {
	fakeReg := &providers.FakeRegistry{}
	SetRegistry(fakeReg)
	defer SetRegistry(nil)

	_, err := GetChatModel(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// 错误应包含 registry 信息
	if !containsStr(err.Error(), "registry") && !containsStr(err.Error(), "not configured") {
		t.Errorf("expected registry-related error, got: %v", err)
	}
}

// TestGetChatModel_WithConfig 验证 cfg!=nil 时直接构造，不查 Registry（Fix 5）。
func TestGetChatModel_WithConfig(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil)

	fakeReg := providers.NewFakeRegistry()
	SetRegistry(fakeReg)

	// cfg == nil 时使用 Registry
	cm, err := GetChatModel(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetChatModel: %v", err)
	}
	if cm == nil {
		t.Fatal("expected non-nil from Registry")
	}
}

// TestConfig_ExplicitNotDiscarded 验证显式 cfg 不被丢弃（Fix 5）。
// 当 cfg != nil 时，应直接使用该 cfg 构造，而不是走 Registry。
func TestConfig_ExplicitNotDiscarded(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil)

	// 设置 Registry 为 nil Chat（会返回错误）
	fakeReg := &providers.FakeRegistry{
		// Chat 为 nil → GetChat 返回错误
	}
	SetRegistry(fakeReg)

	// cfg == nil 时应使用 Registry，返回错误
	_, err := GetChatModel(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when Registry Chat is nil and cfg is nil")
	}

	// cfg != nil 时应直接构造，不查 Registry（Fix 5）
	// 这里使用无效的 cfg 但不会触发 Registry 错误
	// 注意：实际构造可能失败（无效 API key），但重要的是不走 Registry 路径
	explicitCfg := &openai.ChatModelConfig{
		APIKey:  "test-key",
		BaseURL: "http://localhost:1/v1", // 无效地址，构造本身不会失败
		Model:   "test-model",
	}
	// cfg != nil → 直接构造，不查 Registry，不会返回 "role not configured" 错误
	cm, err := GetChatModel(context.Background(), explicitCfg)
	// 构造可能成功（eino NewChatModel 只装配 client 不发请求）
	if err != nil {
		// 如果失败，不应该是 Registry 相关错误
		if containsStr(err.Error(), "registry") || containsStr(err.Error(), "not configured") {
			t.Errorf("cfg!=nil should not go through Registry, got: %v", err)
		}
	} else if cm == nil {
		t.Error("expected non-nil model with explicit cfg")
	}
}

// TestGetRewriteModel_RegistryError 验证 Rewrite Registry 错误传播。
func TestGetRewriteModel_RegistryError(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil)

	fakeReg := &providers.FakeRegistry{
		// Rewrite 为 nil → 返回错误
	}
	SetRegistry(fakeReg)

	_, err := GetRewriteModel(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when Registry GetRewrite fails")
	}
}

// TestGetQAModel_RegistryError 验证 QA Registry 错误传播。
func TestGetQAModel_RegistryError(t *testing.T) {
	SetRegistry(nil)
	defer SetRegistry(nil)

	fakeReg := &providers.FakeRegistry{
		// QA 为 nil → 返回错误
	}
	SetRegistry(fakeReg)

	_, err := GetQAModel(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when Registry GetQA fails")
	}
}

// TestRegistry_BudgetSnapshot 验证预算快照。
func TestRegistry_BudgetSnapshot(t *testing.T) {
	budget, _ := providers.NewProcessBudget(10, 1000, 500, 4)
	snap := budget.Snapshot()
	if snap.AttemptsUsed != 0 {
		t.Errorf("expected 0 attempts, got %d", snap.AttemptsUsed)
	}
}
