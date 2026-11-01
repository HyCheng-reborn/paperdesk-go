package common

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// fakeEmbedRole implements providers.ProfiledEmbedRole for testing.
type fakeEmbedRole struct {
	modelID      string
	dimensions   int
	vectors      [][]float64
	err          error
	providerName string // C3-R2: for ProfiledEmbedRole.Profile()
}

func (f *fakeEmbedRole) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vectors, nil
}

func (f *fakeEmbedRole) Dimensions() int { return f.dimensions }
func (f *fakeEmbedRole) ModelID() string { return f.modelID }
func (f *fakeEmbedRole) Profile() providers.ProviderProfile {
	model, version, err := providers.SplitModelID(f.modelID)
	if err != nil {
		// Mirror a role that cannot expose a verifiable identity.
		return providers.ProviderProfile{ProviderName: f.providerName}
	}
	return providers.ProviderProfile{ProviderName: f.providerName, Model: model, Version: version}
}

// fakeRegistry implements providers.ProviderRegistry for testing.
type fakeRegistry struct {
	embedRole  providers.EmbedRole
	embedErr   error
	embedCalls int // GetEmbedder 调用次数（证明 fail closed 发生在构造角色之前）
}

func (f *fakeRegistry) GetChat(ctx context.Context) (providers.ChatRole, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeRegistry) GetRewrite(ctx context.Context) (providers.ChatRole, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeRegistry) GetQA(ctx context.Context) (providers.ChatRole, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeRegistry) GetExtract(ctx context.Context) (providers.ChatRole, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeRegistry) GetEmbedder(ctx context.Context) (providers.EmbedRole, error) {
	f.embedCalls++
	return f.embedRole, f.embedErr
}
func (f *fakeRegistry) GetReranker(ctx context.Context) (providers.RerankRole, error) {
	return nil, fmt.Errorf("not implemented")
}
func (f *fakeRegistry) NewRequestBudget(ctx context.Context, opts ...providers.BudgetOption) (*providers.RequestBudget, error) {
	return nil, fmt.Errorf("not implemented")
}

// ---------------------------------------------------------------------------
// ResolveEmbeddingIdentity tests
// ---------------------------------------------------------------------------

func TestResolveEmbeddingIdentity_ProviderPath(t *testing.T) {
	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
	}
	name, model, dims, err := ResolveEmbeddingIdentity(conf)
	if err != nil {
		t.Fatal(err)
	}
	if name != "openai" {
		t.Errorf("provider name = %q, want openai", name)
	}
	if model != "text-embedding-3-large" {
		t.Errorf("model = %q, want text-embedding-3-large", model)
	}
	if dims != 768 {
		t.Errorf("dims = %d, want 768", dims)
	}
}

func TestResolveEmbeddingIdentity_LegacyPath(t *testing.T) {
	conf := &config.Config{
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: 1024,
	}
	name, model, dims, err := ResolveEmbeddingIdentity(conf)
	if err != nil {
		t.Fatal(err)
	}
	if name != "legacy" {
		t.Errorf("provider name = %q, want legacy", name)
	}
	if model != "text-embedding-3-large" {
		t.Errorf("model = %q, want text-embedding-3-large", model)
	}
	if dims != 1024 {
		t.Errorf("dims = %d, want 1024", dims)
	}
}

func TestResolveEmbeddingIdentity_ProviderTakesPrecedence(t *testing.T) {
	conf := &config.Config{
		EmbeddingDimensions: 768, // must match provider to avoid conflict
		Provider: &config.ProviderConfig{
			Name:                "bailian",
			EmbeddingModel:      "provider-model",
			EmbeddingDimensions: 768,
		},
	}
	name, model, dims, err := ResolveEmbeddingIdentity(conf)
	if err != nil {
		t.Fatal(err)
	}
	if name != "bailian" {
		t.Errorf("provider name = %q, want bailian", name)
	}
	if model != "provider-model" {
		t.Errorf("model = %q, want provider-model", model)
	}
	if dims != 768 {
		t.Errorf("dims = %d, want 768", dims)
	}
}

func TestResolveEmbeddingIdentity_ProviderNoDims(t *testing.T) {
	conf := &config.Config{
		Provider: &config.ProviderConfig{
			Name:           "openai",
			EmbeddingModel: "text-embedding-3-large",
			// No EmbeddingDimensions set
		},
	}
	_, _, _, err := ResolveEmbeddingIdentity(conf)
	if err == nil {
		t.Fatal("expected error when provider has no dimensions")
	}
}

// ---------------------------------------------------------------------------
// IsActiveProviderEmbedding tests
// ---------------------------------------------------------------------------

func TestIsActiveProviderEmbedding(t *testing.T) {
	tests := []struct {
		name string
		conf *config.Config
		want bool
	}{
		{"nil config", nil, false},
		{"nil provider", &config.Config{}, false},
		{"empty model", &config.Config{Provider: &config.ProviderConfig{}}, false},
		{"active", &config.Config{Provider: &config.ProviderConfig{EmbeddingModel: "m"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsActiveProviderEmbedding(tc.conf); got != tc.want {
				t.Errorf("IsActiveProviderEmbedding() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HasProviderChatModel tests
// ---------------------------------------------------------------------------

func TestHasProviderChatModel(t *testing.T) {
	tests := []struct {
		name string
		conf *config.Config
		want bool
	}{
		{"nil config", nil, false},
		{"nil provider", &config.Config{}, false},
		{"empty model", &config.Config{Provider: &config.ProviderConfig{}}, false},
		{"active", &config.Config{Provider: &config.ProviderConfig{ChatModel: "m"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasProviderChatModel(tc.conf); got != tc.want {
				t.Errorf("HasProviderChatModel() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// newProviderEmbedding tests
// ---------------------------------------------------------------------------

func TestNewProviderEmbedding_RegistryNil(t *testing.T) {
	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		// Registry is nil
	}
	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when Registry is nil")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

func TestNewProviderEmbedding_Success(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		vectors:      [][]float64{make([]float64, 768)},
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eb == nil {
		t.Fatal("expected non-nil embedder")
	}

	// Verify the embedder works
	result, err := eb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedStrings error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 vector, got %d", len(result))
	}
}

func TestNewProviderEmbedding_ModelMismatch(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "different-model",
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected model mismatch error")
	}
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Errorf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
	}
}

func TestNewProviderEmbedding_DimensionMismatch(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   512, // wrong dimensions
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Errorf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
	}
}

// TestNewProviderEmbedding_VersionedConfigFailsClosed proves that a Provider
// embedding configured with "@version" is rejected instead of being used with a
// request that only carries the bare model name (P1). The identity must not
// merely record a version the wire protocol cannot express.
func TestNewProviderEmbedding_VersionedConfigFailsClosed(t *testing.T) {
	conf, reg := newBoundProviderConf(t, "openai", "text-embedding-3-large@v2", 768)
	reg.embedRole = &fakeEmbedRole{
		modelID:      "text-embedding-3-large@v2",
		dimensions:   768,
		providerName: "openai",
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected versioned provider config to fail closed")
	}
	if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Errorf("expected providers.ErrModelVersionNotExpressible, got: %v", err)
	}
	if !errors.Is(err, ErrEmbeddingConfigInvalid) {
		t.Errorf("expected ErrEmbeddingConfigInvalid, got: %v", err)
	}
	if reg.embedCalls != 0 {
		t.Fatalf("Registry.GetEmbedder must not be used for a rejected identity (calls=%d)", reg.embedCalls)
	}
}

func TestNewProviderEmbedding_RegistryGetEmbedderError(t *testing.T) {
	reg := &fakeRegistry{embedErr: fmt.Errorf("credential resolution failed")}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error from Registry.GetEmbedder")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

func TestNewProviderEmbedding_RegistryReturnsNil(t *testing.T) {
	reg := &fakeRegistry{embedRole: nil, embedErr: nil}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when Registry returns nil")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C3-R1: Individual readiness check tests
// ---------------------------------------------------------------------------

// TestP13_GuardWithoutBinding_Fails verifies P1-3: an arbitrary non-nil
// VectorIOGuard plus a lifecycle flag and a non-empty name does not prove the
// physical target is bound; construction must fail closed.
func TestP13_GuardWithoutBinding_Fails(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when no verified physical binding is installed")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

// TestR1_ManagedLifecycle_False_Fails verifies that ManagedResourceLifecycle=false fails.
func TestR1_ManagedLifecycle_False_Fails(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: false, // R1: must fail
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when ManagedResourceLifecycle is false")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

// TestR1_IndexName_Empty_Fails verifies that IndexName="" fails construction.
func TestR1_IndexName_Empty_Fails(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "", // R1: must fail
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when IndexName is empty")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// C3-R2: Identity verification tests
// ---------------------------------------------------------------------------

// TestR2_ProviderName_Mismatch verifies that config provider != role provider fails.
func TestR2_ProviderName_Mismatch(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		providerName: "bailian", // role says bailian
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai", // config says openai
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected provider name mismatch error")
	}
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Errorf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
	}
}

// TestR2_ConfigNoVersion_RoleHasVersion verifies that config without @version
// but role with @version is rejected.
func TestR2_ConfigNoVersion_RoleHasVersion(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large@v2", // role has version
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large", // config has NO version
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected version mismatch error")
	}
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Errorf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
	}
}

// TestR2_VersionMismatch：带版本配置在到达角色身份比较前就 fail closed；
// role 报出另一个版本也不能让请求发出（P1：版本无法在 wire 上表达）。
func TestR2_VersionMismatch(t *testing.T) {
	conf, reg := newBoundProviderConf(t, "openai", "text-embedding-3-large@rev-A", 768)
	reg.embedRole = &fakeEmbedRole{
		modelID:      "text-embedding-3-large@rev-B",
		dimensions:   768,
		providerName: "openai",
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected versioned config to fail closed")
	}
	if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Errorf("expected providers.ErrModelVersionNotExpressible, got: %v", err)
	}
	if reg.embedCalls != 0 {
		t.Fatalf("Registry.GetEmbedder must not be used (calls=%d)", reg.embedCalls)
	}
}

// TestR2_VersionMatch：即使 role 声明的版本与配置相同，带版本配置也不得成功——
// 只把版本写进 fingerprint / 角色标签而请求不携带它，正是 P1 要禁止的不一致。
func TestR2_VersionMatch(t *testing.T) {
	conf, reg := newBoundProviderConf(t, "openai", "text-embedding-3-large@rev-A", 768)
	reg.embedRole = &fakeEmbedRole{
		modelID:      "text-embedding-3-large@rev-A",
		dimensions:   768,
		providerName: "openai",
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected versioned config to fail closed even when the role matches")
	}
	if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Errorf("expected providers.ErrModelVersionNotExpressible, got: %v", err)
	}
	if reg.embedCalls != 0 {
		t.Fatalf("Registry.GetEmbedder must not be used (calls=%d)", reg.embedCalls)
	}
}

// TestR2_FilterEmbeddingOptions_StripsWithModel verifies that embedding.WithModel
// is stripped from opts passed to the underlying embedder.
func TestR2_FilterEmbeddingOptions_StripsWithModel(t *testing.T) {
	var receivedOpts []embedding.Option
	role := &fakeEmbedRoleWithOpts{
		fakeEmbedRole: fakeEmbedRole{
			modelID:      "text-embedding-3-large",
			dimensions:   768,
			vectors:      [][]float64{make([]float64, 768)},
			providerName: "openai",
		},
		onEmbed: func(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
			receivedOpts = opts
			return [][]float64{make([]float64, 768)}, nil
		},
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Call with embedding.WithModel for a different model — P2-2 requires rejection.
	_, err = eb.EmbedStrings(context.Background(), []string{"hello"}, embedding.WithModel("other-model"))
	if !errors.Is(err, ErrEmbeddingModelOverride) {
		t.Fatalf("expected model override rejection, got: %v", err)
	}
	if len(receivedOpts) != 0 {
		t.Fatalf("underlying embedder must not be called on rejected override, got %d opts", len(receivedOpts))
	}
}

// fakeEmbedRoleWithOpts wraps fakeEmbedRole with a custom EmbedStrings callback.
type fakeEmbedRoleWithOpts struct {
	fakeEmbedRole
	onEmbed func(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
}

func (f *fakeEmbedRoleWithOpts) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if f.onEmbed != nil {
		return f.onEmbed(ctx, texts, opts...)
	}
	return f.fakeEmbedRole.EmbedStrings(ctx, texts, opts...)
}

// ---------------------------------------------------------------------------
// C3-R5: Error chain tests
// ---------------------------------------------------------------------------

// TestR5_ErrorChain_RegistryGetEmbedder_Canceled verifies that context.Canceled
// from Registry.GetEmbedder is preserved via errors.Is.
func TestR5_ErrorChain_RegistryGetEmbedder_Canceled(t *testing.T) {
	reg := &fakeRegistry{embedErr: context.Canceled}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is(err, context.Canceled) == true, got: %v", err)
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected errors.Is(err, ErrEmbeddingProfileNotReady) == true, got: %v", err)
	}
}

// TestR5_ErrorChain_RoleNotConfigured verifies that providers.ErrRoleNotConfigured
// is preserved via errors.Is.
func TestR5_ErrorChain_RoleNotConfigured(t *testing.T) {
	reg := &fakeRegistry{embedErr: providers.ErrRoleNotConfigured}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, providers.ErrRoleNotConfigured) {
		t.Errorf("expected errors.Is(err, ErrRoleNotConfigured) == true, got: %v", err)
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected errors.Is(err, ErrEmbeddingProfileNotReady) == true, got: %v", err)
	}
}

// TestR5_ErrorChain_DimensionConflict verifies that config.ErrEmbeddingDimensionConflict
// is preserved via errors.Is through ResolveEmbeddingIdentity.
func TestR5_ErrorChain_DimensionConflict(t *testing.T) {
	// Config with conflicting legacy and provider dimensions
	conf := &config.Config{
		EmbeddingDimensions: 512, // legacy says 512
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768, // provider says 768 → conflict
		},
	}

	// Test via ResolveEmbeddingIdentity (which wraps the dimension conflict)
	_, _, _, err := ResolveEmbeddingIdentity(conf)
	if err == nil {
		t.Fatal("expected error from dimension conflict")
	}
	if !errors.Is(err, config.ErrEmbeddingDimensionConflict) {
		t.Errorf("expected errors.Is(err, ErrEmbeddingDimensionConflict) == true, got: %v", err)
	}

	// Also verify via NewEmbedding (which wraps in ErrEmbeddingConfigInvalid)
	// Need a Registry to get past the nil check to ResolveEmbeddingIdentity
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}
	conf2 := &config.Config{
		EmbeddingDimensions:      512,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		VectorIOGuard:            func(ctx context.Context) error { return nil },
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf2)

	_, err2 := NewEmbedding(context.Background(), conf2)
	if err2 == nil {
		t.Fatal("expected error from dimension conflict via NewEmbedding")
	}
	if !errors.Is(err2, config.ErrEmbeddingDimensionConflict) {
		t.Errorf("expected errors.Is(err, ErrEmbeddingDimensionConflict) == true via NewEmbedding, got: %v", err2)
	}
	if !errors.Is(err2, ErrEmbeddingConfigInvalid) {
		t.Errorf("expected errors.Is(err, ErrEmbeddingConfigInvalid) == true, got: %v", err2)
	}
}

// TestNewProviderEmbedding_GuardPropagation verifies that the verified binding's
// runtime guard (not a caller-supplied callback) protects EmbedStrings.
func TestNewProviderEmbedding_GuardPropagation(t *testing.T) {
	role := &fakeEmbedRole{
		modelID:      "text-embedding-3-large",
		dimensions:   768,
		vectors:      [][]float64{make([]float64, 768)},
		providerName: "openai",
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions:      768,
		IndexName:                "test-index",
		ManagedResourceLifecycle: true,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}
	env := bindTestTarget(t, conf)
	before := env.Reads.Load()

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = eb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedStrings error: %v", err)
	}
	if env.Reads.Load() <= before {
		t.Fatal("binding guard should re-verify the physical target around embedding")
	}
}
