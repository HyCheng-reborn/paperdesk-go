package core

import (
	"context"
	"errors"
	"testing"

	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

func TestFingerprintChatOnlyProviderIsolation(t *testing.T) {
	base := &config.Config{IndexName: "idx", EmbeddingModel: "legacy-model", EmbeddingDimensions: 1024, BaseURL: "http://embedding.invalid/v1", APIKey: "embedding-key"}
	want, err := ComputeFingerprintMeta(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"openai", "bailian"} {
		t.Run(provider, func(t *testing.T) {
			conf := base.Copy()
			conf.Provider = &config.ProviderConfig{Name: provider, BaseURL: "http://chat.invalid/v1", APIKey: "chat-key", ChatModel: "chat-only"}
			got, err := ComputeFingerprintMeta(conf)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) || got.Digest() != want.Digest() {
				t.Fatalf("chat changed embedding identity: got=%+v want=%+v", got, want)
			}
		})
	}
}

func TestFingerprintChatOnlyProviderPreservesStoredIdentity(t *testing.T) {
	fp, err := vector.NewFingerprint("v1", "legacy-model", 1024, "legacy", "cosine")
	if err != nil {
		t.Fatal(err)
	}
	vs := &fakeVectorStore{fingerprint: fp, resourceID: "resource-A"}
	conf := &config.Config{IndexName: "idx", EmbeddingModel: "legacy-model", EmbeddingDimensions: 1024, Provider: &config.ProviderConfig{Name: "openai", ChatModel: "chat-only"}}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)
	if err := r.VerifyFingerprint(context.Background(), vs); err != nil {
		t.Fatal(err)
	}
	if err := r.checkFingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected, err := ComputeFingerprintMeta(conf)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := vector.BindPhysicalTarget(context.Background(), vs, "idx", expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background()); err != nil {
		t.Fatal(err)
	}
	if vs.writeCalls != 0 || vs.fingerprint.ProviderName != "legacy" {
		t.Fatal("must not rewrite stored identity")
	}
}

func TestFingerprintExplicitEmbeddingProfileStillGuarded(t *testing.T) {
	conf := &config.Config{EmbeddingModel: "legacy-model", EmbeddingDimensions: 1024, Provider: &config.ProviderConfig{Name: "bailian", EmbeddingModel: "profile-model", EmbeddingDimensions: 1024}}
	fp, err := ComputeFingerprintMeta(conf)
	if err != nil {
		t.Fatal(err)
	}
	if fp.ProviderName != "bailian" || fp.EmbeddingModelID != "profile-model" {
		t.Fatal(fp)
	}
	if _, err := common.NewEmbedding(context.Background(), conf); !errors.Is(err, common.ErrEmbeddingGuardNotReady) {
		t.Fatal(err)
	}
}

func TestFingerprintChatOnlyProviderDoesNotReclaimMislabeledRecord(t *testing.T) {
	fp, err := vector.NewFingerprint("v1", "legacy-model", 1024, "openai", "cosine")
	if err != nil {
		t.Fatal(err)
	}
	vs := &fakeVectorStore{fingerprint: fp, resourceID: "resource-A"}
	conf := &config.Config{IndexName: "idx", EmbeddingModel: "legacy-model", EmbeddingDimensions: 1024, Provider: &config.ProviderConfig{Name: "openai", ChatModel: "chat-only"}}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)
	if err := r.VerifyFingerprint(context.Background(), vs); !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Fatalf("mislabeled identity must still fail closed: %v", err)
	}
	if vs.writeCalls != 0 || vs.fingerprint.ProviderName != "openai" {
		t.Fatal("must not repair stored identity implicitly")
	}
}
