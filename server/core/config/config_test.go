package config

import (
	"context"
	"testing"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// TestConfig_CopyPreservesRegistry 验证 Copy 后 Registry 字段保留 (Fix 1)
func TestConfig_CopyPreservesRegistry(t *testing.T) {
	// 创建一个 fake Registry
	fakeReg := &fakeRegistry{}

	original := &Config{
		IndexName: "test-index",
		APIKey:    "test-key",
		Registry:  fakeReg,
	}

	copied := original.Copy()

	// 验证 Registry 字段被复制
	if copied.Registry == nil {
		t.Fatal("expected Registry to be copied, got nil")
	}
	if copied.Registry != fakeReg {
		t.Error("expected Registry to be the same instance")
	}

	// 验证其他字段也被正确复制
	if copied.IndexName != original.IndexName {
		t.Errorf("IndexName mismatch: got %q, want %q", copied.IndexName, original.IndexName)
	}
	if copied.APIKey != original.APIKey {
		t.Errorf("APIKey mismatch: got %q, want %q", copied.APIKey, original.APIKey)
	}
}

// fakeRegistry 是一个用于测试的 fake ProviderRegistry 实现
type fakeRegistry struct{}

func (f *fakeRegistry) GetChat(ctx context.Context) (providers.ChatRole, error) {
	return nil, nil
}
func (f *fakeRegistry) GetRewrite(ctx context.Context) (providers.ChatRole, error) {
	return nil, nil
}
func (f *fakeRegistry) GetQA(ctx context.Context) (providers.ChatRole, error) {
	return nil, nil
}
func (f *fakeRegistry) GetExtract(ctx context.Context) (providers.ChatRole, error) {
	return nil, nil
}
func (f *fakeRegistry) GetEmbedder(ctx context.Context) (providers.EmbedRole, error) {
	return nil, nil
}
func (f *fakeRegistry) GetReranker(ctx context.Context) (providers.RerankRole, error) {
	return nil, nil
}
func (f *fakeRegistry) NewRequestBudget(ctx context.Context, opts ...providers.BudgetOption) (*providers.RequestBudget, error) {
	return nil, nil
}

// 确保 fakeRegistry 实现了接口（编译期检查）
var _ providers.ProviderRegistry = (*fakeRegistry)(nil)
