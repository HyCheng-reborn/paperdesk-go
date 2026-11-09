package common

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// TestP1_VersionedProviderConfigFailsClosed covers the real config chain: a
// versioned embedding model cannot produce a usable expected fingerprint and
// cannot construct a Registry role, so no request can carry the bare model name
// while the identity claims a version.
func TestP1_VersionedProviderConfigFailsClosed(t *testing.T) {
	conf, reg := newBoundProviderConf(t, "openai", "m@v1", 768)
	reg.embedRole = &fakeEmbedRole{modelID: "m@v1", dimensions: 768, providerName: "openai"}

	if _, err := ExpectedEmbeddingFingerprint(conf); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("expected fingerprint construction to fail closed, got: %v", err)
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

// TestP2_ExpectedFingerprintUsesSharedConstructor proves the direct entry and
// the Rag gate build their expectation from one constructor: full identity
// including similarity, not an ad-hoc field subset.
func TestP2_ExpectedFingerprintUsesSharedConstructor(t *testing.T) {
	conf := &config.Config{
		EmbeddingDimensions: 768,
		Provider: &config.ProviderConfig{
			Name:                " OpenAI ",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 768,
		},
	}
	got, err := ExpectedEmbeddingFingerprint(conf)
	if err != nil {
		t.Fatalf("ExpectedEmbeddingFingerprint: %v", err)
	}
	want, err := vector.NewExpectedEmbeddingFingerprint("text-embedding-3-large", 768, "openai")
	if err != nil {
		t.Fatalf("NewExpectedEmbeddingFingerprint: %v", err)
	}
	if !got.Equal(want) || got.Digest() != want.Digest() {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
	if got.Similarity != vector.SimilarityCosine {
		t.Fatalf("similarity = %q, want %q", got.Similarity, vector.SimilarityCosine)
	}
}

// TestP2_DirectEntryComparesFullFingerprint pins the direct entry against the
// complete fingerprint: a cosine binding is accepted, a dot binding with the
// same provider/model/dimensions is rejected (P2: similarity was previously
// omitted from the comparison).
func TestP2_DirectEntryComparesFullFingerprint(t *testing.T) {
	ctx := context.Background()

	t.Run("matching cosine binding is accepted", func(t *testing.T) {
		conf, reg := newBoundProviderConf(t, "openai", "text-embedding-3-large", 768)
		expected, err := ExpectedEmbeddingFingerprint(conf)
		if err != nil {
			t.Fatalf("ExpectedEmbeddingFingerprint: %v", err)
		}
		binding, ok := conf.PhysicalBinding.(*vector.TargetBinding)
		if !ok {
			t.Fatalf("unexpected binding type %T", conf.PhysicalBinding)
		}
		bound := binding.Fingerprint()
		if !bound.Equal(expected) {
			t.Fatalf("binding %+v != expected %+v", bound, expected)
		}
		reg.embedRole = &fakeEmbedRole{
			modelID:      "text-embedding-3-large",
			dimensions:   768,
			vectors:      [][]float64{make([]float64, 768)},
			providerName: "openai",
		}

		eb, err := NewEmbedding(ctx, conf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := eb.EmbedStrings(ctx, []string{"hello"}); err != nil {
			t.Fatalf("EmbedStrings: %v", err)
		}
	})

	t.Run("dot binding is rejected", func(t *testing.T) {
		env := newTestBindingEnvWithSimilarity(t, "openai", "text-embedding-3-large", 768, "dot")
		reg := &fakeRegistry{embedRole: &fakeEmbedRole{
			modelID:      "text-embedding-3-large",
			dimensions:   768,
			vectors:      [][]float64{make([]float64, 768)},
			providerName: "openai",
		}}
		conf := &config.Config{
			EmbeddingDimensions:      768,
			IndexName:                testBindingIndex,
			ManagedResourceLifecycle: true,
			Client:                   env.Store.GetClient(),
			PhysicalBinding:          env.Binding,
			VectorIOGuard:            env.Binding.Guard(),
			Provider: &config.ProviderConfig{
				Name:                "openai",
				EmbeddingModel:      "text-embedding-3-large",
				EmbeddingDimensions: 768,
			},
			Registry: reg,
		}

		_, err := NewEmbedding(ctx, conf)
		if err == nil {
			t.Fatal("expected non-cosine binding to be rejected")
		}
		if !errors.Is(err, ErrEmbeddingIdentityMismatch) {
			t.Fatalf("expected ErrEmbeddingIdentityMismatch, got: %v", err)
		}
		if !errors.Is(err, vector.ErrFingerprintMismatch) {
			t.Fatalf("expected vector.ErrFingerprintMismatch in chain, got: %v", err)
		}
		if !strings.Contains(err.Error(), "similarity") {
			t.Fatalf("error must name the differing field, got: %v", err)
		}
		if reg.embedCalls != 0 {
			t.Fatalf("Registry.GetEmbedder must not be used (calls=%d)", reg.embedCalls)
		}
	})
}
