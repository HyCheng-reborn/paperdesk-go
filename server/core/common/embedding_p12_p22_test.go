package common

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// unverifiedEmbedRole implements EmbedRole but cannot expose a Profile, which is
// what P1-2 treats as an unverifiable identity (fail closed).
type unverifiedEmbedRole struct {
	modelID    string
	dimensions int
}

func (r *unverifiedEmbedRole) EmbedStrings(_ context.Context, _ []string, _ ...embedding.Option) ([][]float64, error) {
	return [][]float64{make([]float64, r.dimensions)}, nil
}
func (r *unverifiedEmbedRole) Dimensions() int { return r.dimensions }
func (r *unverifiedEmbedRole) ModelID() string { return r.modelID }

// newBoundProviderConf builds a config whose config identity matches a verified
// binding created from the local fake ES target.
func newBoundProviderConf(t *testing.T, provider, model string, dims int) (*config.Config, *fakeRegistry) {
	t.Helper()
	reg := &fakeRegistry{}
	conf := &config.Config{
		EmbeddingDimensions: dims,
		Provider: &config.ProviderConfig{
			Name:                provider,
			EmbeddingModel:      model,
			EmbeddingDimensions: dims,
		},
		Registry: reg,
	}
	bindTestTarget(t, conf)
	return conf, reg
}

func TestP12_EmbeddingIdentityFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		role     providers.EmbedRole
		provider string
		model    string
	}{
		{
			name:     "role without verifiable profile",
			role:     &unverifiedEmbedRole{modelID: "text-embedding-3-large", dimensions: 768},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
		{
			name:     "empty provider name in profile",
			role:     &fakeEmbedRole{modelID: "text-embedding-3-large", dimensions: 768},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
		{
			name:     "different provider same dimensions",
			role:     &fakeEmbedRole{modelID: "text-embedding-3-large", dimensions: 768, providerName: "bailian"},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
		{
			name:     "different model same dimensions",
			role:     &fakeEmbedRole{modelID: "other-model", dimensions: 768, providerName: "openai"},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
		{
			name:     "role adds version to unversioned config",
			role:     &fakeEmbedRole{modelID: "text-embedding-3-large@v2", dimensions: 768, providerName: "openai"},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
		{
			name:     "role with empty version",
			role:     &fakeEmbedRole{modelID: "text-embedding-3-large@", dimensions: 768, providerName: "openai"},
			provider: "openai",
			model:    "text-embedding-3-large",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf, reg := newBoundProviderConf(t, tc.provider, tc.model, 768)
			reg.embedRole = tc.role

			_, err := NewEmbedding(context.Background(), conf)
			if err == nil {
				t.Fatal("expected identity verification to fail closed")
			}
			if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
				t.Fatalf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
			}
		})
	}
}

// TestP12_EmptyVersionConfigRejected proves that "m@" and "@v1" are invalid
// identities rather than aliases for another model.
func TestP12_EmptyVersionConfigRejected(t *testing.T) {
	for _, model := range []string{"m@", "@v1", "m@v1@v2"} {
		t.Run(model, func(t *testing.T) {
			conf, reg := newBoundProviderConf(t, "openai", model, 768)
			reg.embedRole = &fakeEmbedRole{modelID: model, dimensions: 768, providerName: "openai"}

			_, err := NewEmbedding(context.Background(), conf)
			if err == nil {
				t.Fatalf("expected %q to be rejected", model)
			}
			if !errors.Is(err, ErrEmbeddingConfigInvalid) && !errors.Is(err, ErrEmbeddingIdentityMismatch) {
				t.Fatalf("expected invalid identity error, got: %v", err)
			}
		})
	}
}

// TestP13_UntrustedBindingIsRejected proves that only a binding produced by
// BindPhysicalTarget for the same concrete target and client is accepted.
func TestP13_UntrustedBindingIsRejected(t *testing.T) {
	role := &fakeEmbedRole{modelID: "text-embedding-3-large", dimensions: 768, providerName: "openai"}
	base, _ := newBoundProviderConf(t, "openai", "text-embedding-3-large", 768)
	base.Registry.(*fakeRegistry).embedRole = role

	foreign := newTestBindingEnv(t, "openai", "text-embedding-3-large", 768)

	cases := []struct {
		name   string
		mutate func(conf *config.Config)
	}{
		{"missing binding", func(conf *config.Config) { conf.PhysicalBinding = nil }},
		{"unexpected binding type", func(conf *config.Config) { conf.PhysicalBinding = "not-a-binding" }},
		{"zero value binding", func(conf *config.Config) { conf.PhysicalBinding = &vector.TargetBinding{} }},
		{"binding for different target name", func(conf *config.Config) { conf.IndexName = "other-index" }},
		{"binding for foreign client", func(conf *config.Config) { conf.Client = foreign.Store.GetClient() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := base.Copy()
			tc.mutate(conf)

			_, err := NewEmbedding(context.Background(), conf)
			if err == nil {
				t.Fatal("expected untrusted binding to be rejected")
			}
			if !errors.Is(err, ErrEmbeddingProfileNotReady) {
				t.Fatalf("expected ErrEmbeddingProfileNotReady, got: %v", err)
			}
		})
	}
}

// TestP22_OptionsAreFilteredWithoutSilentLoss verifies P2-2 against the locked
// Eino v0.6.0 option semantics: a model override that changes the bound identity
// is rejected, while the same model and unrelated options are forwarded in order.
func TestP22_OptionsAreFilteredWithoutSilentLoss(t *testing.T) {
	const boundModel = "text-embedding-3-large"

	var received []embedding.Option
	inner := &fakeEmbedRoleWithOpts{
		fakeEmbedRole: fakeEmbedRole{
			modelID:      boundModel,
			dimensions:   768,
			vectors:      [][]float64{make([]float64, 768)},
			providerName: "openai",
		},
		onEmbed: func(_ context.Context, _ []string, opts ...embedding.Option) ([][]float64, error) {
			received = opts
			return [][]float64{make([]float64, 768)}, nil
		},
	}
	conf, reg := newBoundProviderConf(t, "openai", boundModel, 768)
	reg.embedRole = inner

	eb, err := NewEmbedding(context.Background(), conf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var order []string
	markerA := embedding.WrapImplSpecificOptFn(func(*string) { order = append(order, "a") })
	sameModel := embedding.WithModel(boundModel)
	markerB := embedding.WrapImplSpecificOptFn(func(*string) { order = append(order, "b") })

	if _, err := eb.EmbedStrings(context.Background(), []string{"hello"}, markerA, sameModel, markerB); err != nil {
		t.Fatalf("EmbedStrings error: %v", err)
	}
	if len(received) != 3 {
		t.Fatalf("expected 3 forwarded options, got %d", len(received))
	}
	var probe string
	embedding.GetImplSpecificOptions(&probe, received...)
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("unrelated options lost or reordered: %v", order)
	}
	commonOpts := embedding.GetCommonOptions(&embedding.Options{}, received...)
	if commonOpts.Model == nil || *commonOpts.Model != boundModel {
		t.Fatalf("bound model option not forwarded: %#v", commonOpts.Model)
	}

	received = nil
	_, err = eb.EmbedStrings(context.Background(), []string{"hello"}, markerA, embedding.WithModel("other-model"), markerB)
	if !errors.Is(err, ErrEmbeddingModelOverride) {
		t.Fatalf("expected ErrEmbeddingModelOverride, got: %v", err)
	}
	if received != nil {
		t.Fatal("inner embedder must not be called when model override is rejected")
	}
}

// TestProviderNameNormalizationIsConsistent checks fingerprint, Registry Profile
// and embedding identity comparison share one normalization semantic.
func TestProviderNameNormalizationIsConsistent(t *testing.T) {
	fp, err := vector.NewFingerprint(vector.FingerprintVersion, "text-embedding-3-large", 768, "  OpenAI ", "cosine")
	if err != nil {
		t.Fatal(err)
	}
	if fp.ProviderName != "openai" {
		t.Fatalf("fingerprint provider = %q, want openai", fp.ProviderName)
	}
	profile, err := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: "  OpenAI ",
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     "http://offline.invalid/v1",
		Model:        "text-embedding-3-large",
		Dimensions:   768,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProviderName != fp.ProviderName {
		t.Fatalf("profile provider = %q, fingerprint provider = %q", profile.ProviderName, fp.ProviderName)
	}

	// Mixed-case config name, normalized binding and normalized role must agree.
	role := &fakeEmbedRole{modelID: "text-embedding-3-large", dimensions: 768, providerName: "openai"}
	conf, reg := newBoundProviderConf(t, " OpenAI ", "text-embedding-3-large", 768)
	reg.embedRole = role
	if _, err := NewEmbedding(context.Background(), conf); err != nil {
		t.Fatalf("normalized identities should match: %v", err)
	}
}
