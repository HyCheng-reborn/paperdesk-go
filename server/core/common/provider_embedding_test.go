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

// fakeEmbedRole implements providers.EmbedRole for testing.
type fakeEmbedRole struct {
	modelID    string
	dimensions int
	vectors    [][]float64
	err        error
}

func (f *fakeEmbedRole) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vectors, nil
}

func (f *fakeEmbedRole) Dimensions() int { return f.dimensions }
func (f *fakeEmbedRole) ModelID() string  { return f.modelID }

// fakeRegistry implements providers.ProviderRegistry for testing.
type fakeRegistry struct {
	embedRole providers.EmbedRole
	embedErr  error
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
		modelID:    "text-embedding-3-large",
		dimensions: 768,
		vectors:    [][]float64{make([]float64, 768)},
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

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
		modelID:    "different-model",
		dimensions: 768,
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

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
		modelID:    "text-embedding-3-large",
		dimensions: 512, // wrong dimensions
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
		t.Errorf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
	}
}

func TestNewProviderEmbedding_ModelWithVersionSuffix(t *testing.T) {
	// Registry returns model ID with @version suffix — should match
	role := &fakeEmbedRole{
		modelID:    "text-embedding-3-large@v2",
		dimensions: 768,
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eb == nil {
		t.Fatal("expected non-nil embedder")
	}
}

func TestNewProviderEmbedding_RegistryGetEmbedderError(t *testing.T) {
	reg := &fakeRegistry{embedErr: fmt.Errorf("credential resolution failed")}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

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
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry: reg,
	}

	_, err := NewEmbedding(context.Background(), conf)
	if err == nil {
		t.Fatal("expected error when Registry returns nil")
	}
	if !errors.Is(err, ErrEmbeddingProfileNotReady) {
		t.Errorf("expected ErrEmbeddingProfileNotReady, got: %v", err)
	}
}

// TestNewProviderEmbedding_GuardPropagation verifies that the VectorIOGuard
// is propagated to the guardedEmbedder.
func TestNewProviderEmbedding_GuardPropagation(t *testing.T) {
	guardCalled := false
	guard := func(ctx context.Context) error {
		guardCalled = true
		return nil
	}
	role := &fakeEmbedRole{
		modelID:    "text-embedding-3-large",
		dimensions: 768,
		vectors:    [][]float64{make([]float64, 768)},
	}
	reg := &fakeRegistry{embedRole: role}

	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                "openai",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
		Registry:      reg,
		VectorIOGuard: guard,
	}

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = eb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedStrings error: %v", err)
	}
	if !guardCalled {
		t.Fatal("guard should have been called")
	}
}
