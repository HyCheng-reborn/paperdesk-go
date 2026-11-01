package providers

import (
	"context"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// B6: Builder 同步测试
// ---------------------------------------------------------------------------

// TestBuilder_ConcurrentRegistration 验证并发注册不 DATA RACE。
func TestBuilder_ConcurrentRegistration(t *testing.T) {
	// 重置冻结状态（测试隔离）
	resetBuilderState()
	defer resetBuilderState() // 测试后清理

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan error, n*3)

	for i := 0; i < n; i++ {
		wg.Add(3)
		name := string(rune('A' + i))
		go func() {
			defer wg.Done()
			if err := RegisterChatBuilder("test_"+name, func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
				return nil, nil
			}); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := RegisterEmbedBuilder("test_"+name, func(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
				return nil, nil
			}); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := RegisterRerankBuilder("test_"+name, func(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
				return nil, nil
			}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestBuilder_OpenAIRegistered 验证 OpenAI 生产 builder 注册（Fix 6）。
func TestBuilder_OpenAIRegistered(t *testing.T) {
	resetBuilderState()
	defer resetBuilderState()

	// 验证 openai builder 已注册
	_, ok := lookupChatBuilder("openai")
	if !ok {
		t.Error("expected openai chat builder to be registered")
	}
	_, ok = lookupEmbedBuilder("openai")
	if !ok {
		t.Error("expected openai embed builder to be registered")
	}
	_, ok = lookupRerankBuilder("openai")
	if !ok {
		t.Error("expected openai rerank builder to be registered")
	}
}

// TestBuilder_UnknownProviderRejected 验证未知 Provider 返回错误。
func TestBuilder_UnknownProviderRejected(t *testing.T) {
	resetBuilderState()
	defer resetBuilderState() // 测试后清理

	// 注册一个已知 provider
	_ = RegisterChatBuilder("known", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return nil, nil
	})
	freezeBuilders()

	// 构建 Registry 并尝试获取未知 provider 的角色
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	reg, err := NewRegistry(RegistryConfig{
		Chat: func() *ProviderProfile {
			p, _ := NewProviderProfile(ProviderProfile{
				ProviderName: "known",
				Protocol:     "openai-compatible",
				Endpoint:     "http://localhost",
				Model:        "test-model",
			})
			return p
		}(),
		Budget:      budget,
		Credentials: func(ctx context.Context, p *ProviderProfile) (string, error) { return "key", nil },
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// 尝试获取未知 provider 的角色
	unknownProfile, _ := NewProviderProfile(ProviderProfile{
		ProviderName: "unknown_provider",
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost",
		Model:        "test",
	})
	// 直接调用内部 buildChat 来测试
	r := reg.(*registry)
	_, err = r.buildChat(context.Background(), unknownProfile)
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !containsStr(err.Error(), "unknown provider") {
		t.Errorf("expected 'unknown provider' error, got: %v", err)
	}
}

// TestBuilder_NilBuilderRejected 验证 nil builder 拒绝。
func TestBuilder_NilBuilderRejected(t *testing.T) {
	resetBuilderState()
	defer resetBuilderState()

	err := RegisterChatBuilder("nil_test", nil)
	if err == nil {
		t.Fatal("expected error for nil builder, got nil")
	}
	if err != ErrNilBuilder {
		t.Errorf("expected ErrNilBuilder, got: %v", err)
	}

	err = RegisterEmbedBuilder("nil_test", nil)
	if err != ErrNilBuilder {
		t.Errorf("expected ErrNilBuilder for embed, got: %v", err)
	}

	err = RegisterRerankBuilder("nil_test", nil)
	if err != ErrNilBuilder {
		t.Errorf("expected ErrNilBuilder for rerank, got: %v", err)
	}
}

// TestBuilder_DuplicateRegistrationRejected 验证重复注册拒绝。
func TestBuilder_DuplicateRegistrationRejected(t *testing.T) {
	resetBuilderState()
	defer resetBuilderState()

	err := RegisterChatBuilder("dup_test", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	err = RegisterChatBuilder("dup_test", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error for duplicate registration, got nil")
	}
}

// TestBuilder_FrozenAfterRegistryBuild 验证首次构建后冻结。
func TestBuilder_FrozenAfterRegistryBuild(t *testing.T) {
	resetBuilderState()
	defer resetBuilderState() // 测试后清理

	_ = RegisterChatBuilder("freeze_test", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return nil, nil
	})

	budget, _ := NewProcessBudget(0, 0, 0, 0)
	_, err := NewRegistry(RegistryConfig{
		Chat: func() *ProviderProfile {
			p, _ := NewProviderProfile(ProviderProfile{
				ProviderName: "freeze_test",
				Protocol:     "openai-compatible",
				Endpoint:     "http://localhost",
				Model:        "test",
			})
			return p
		}(),
		Budget:      budget,
		Credentials: func(ctx context.Context, p *ProviderProfile) (string, error) { return "key", nil },
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// 冻结后注册应失败
	err = RegisterChatBuilder("after_freeze", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return nil, nil
	})
	if err != ErrBuilderFrozen {
		t.Errorf("expected ErrBuilderFrozen, got: %v", err)
	}
}

// resetBuilderState 重置 builder 状态（测试隔离）。
// 重新注册标准 provider（openai/bailian），保证其他测试不受影响。
func resetBuilderState() {
	builderMu.Lock()
	defer builderMu.Unlock()
	chatBuilders = map[string]ChatBuilder{}
	embedBuilders = map[string]EmbedBuilder{}
	rerankBuilders = map[string]RerankBuilder{}
	buildersFrozen = false
	// 重新注册标准 provider（与 TestMain 一致，保留 Profile 信息）
	chatBuilders["openai"] = func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return &profileChat{FakeChat: NewFakeChat(), profile: cfg.Profile.Clone()}, nil
	}
	embedBuilders["openai"] = func(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
		return NewFakeEmbed(1024, "openai-embed"), nil
	}
	rerankBuilders["openai"] = func(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
		return NewFakeRerank(), nil
	}
	chatBuilders["bailian"] = func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return &profileChat{FakeChat: NewFakeChat(), profile: cfg.Profile.Clone()}, nil
	}
	embedBuilders["bailian"] = func(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
		return NewFakeEmbed(1024, "bailian-embed"), nil
	}
	rerankBuilders["bailian"] = func(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
		return NewFakeRerank(), nil
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
