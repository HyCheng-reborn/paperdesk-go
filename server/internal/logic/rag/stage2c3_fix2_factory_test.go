package rag

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// resetProcessBudget 清理进程级预算单例，避免其他用例遗留状态影响本用例。
func resetProcessBudget(t *testing.T) {
	t.Helper()
	processBudgetMu.Lock()
	old := processBudget
	processBudget = nil
	processBudgetMu.Unlock()
	t.Cleanup(func() {
		processBudgetMu.Lock()
		processBudget = old
		processBudgetMu.Unlock()
	})
}

// TestP1_FactoryFailsClosedBeforeTouchingES walks the real YAML → readRagConfig →
// defaultRagFactory chain: a versioned embedding model must be rejected while
// computing the expected fingerprint, i.e. before the factory can read, create or
// claim any index whose stored identity would carry a version the request cannot.
func TestP1_FactoryFailsClosedBeforeTouchingES(t *testing.T) {
	var requests atomic.Int64
	esSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(esSrv.Close)

	stage2c3Config(t, fmt.Sprintf(`vector:
  type: es
  indexName: offline
  managedResourceLifecycle: true
  es:
    address: %s
embedding:
  model: legacy-embed
  dimensions: 2
chat:
  model: legacy-chat
provider:
  name: openai
  base_url: https://offline.invalid/v1
  api_key: offline-key
  chat_model: provider-chat
  embedding_model: m@v1
  embedding_dimensions: 2
  max_requests: 0
`, esSrv.URL))

	cfg, err := readRagConfig()
	if err != nil {
		t.Fatalf("readRagConfig: %v", err)
	}
	if cfg.Provider == nil || cfg.Provider.EmbeddingModel != "m@v1" {
		t.Fatalf("provider embedding model not read: %+v", cfg.Provider)
	}
	if !cfg.VectorCfg.ManagedResourceLifecycle {
		t.Fatalf("managed lifecycle not read: %+v", cfg.VectorCfg)
	}
	resetProcessBudget(t)

	vs, err := vector.NewVectorStore(&cfg.VectorCfg)
	if err != nil {
		t.Fatalf("NewVectorStore: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })

	if _, err := (&defaultRagFactory{}).Create(cfg, vs); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("factory must fail closed for a versioned model, got: %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("factory must not touch the vector backend before fingerprinting, got %d request(s)", got)
	}
}

// TestP2_DefaultFactoryRejectsNonCosineTarget pins the default factory to the same
// complete fingerprint as the direct entry: an existing index that matches on
// provider/model/dimensions but differs in similarity must be rejected instead of
// being adopted, overwritten or recreated.
func TestP2_DefaultFactoryRejectsNonCosineTarget(t *testing.T) {
	fp, err := vector.NewFingerprint(vector.FingerprintVersion, "text-embedding-3-large", 2, "openai", "dot")
	if err != nil {
		t.Fatal(err)
	}
	esSrv, writes := esFingerprintServerWithSchema(t, fp, 2, "dot_product")

	cfg := RagConfig{
		VectorCfg: vector.Config{
			Type:                     "es",
			IndexName:                "test",
			ES:                       &vector.ESConfig{Address: esSrv.URL},
			EmbeddingDimensions:      2,
			ManagedResourceLifecycle: true,
		},
		EmbeddingDimensions: 2,
		ChatModel:           "offline-chat",
		ChatAPIKey:          "offline",
		ChatBaseURL:         "http://chat.invalid/v1",
		Provider: &config.ProviderConfig{
			Name:                "openai",
			BaseURL:             "http://embedding.invalid/v1",
			APIKey:              "offline",
			EmbeddingModel:      "text-embedding-3-large",
			EmbeddingDimensions: 2,
		},
	}
	off := false
	cfg.QAEnabled = &off
	resetProcessBudget(t)

	vs, err := vector.NewVectorStore(&cfg.VectorCfg)
	if err != nil {
		t.Fatalf("NewVectorStore: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })

	_, err = (&defaultRagFactory{}).Create(cfg, vs)
	if err == nil {
		t.Fatal("expected a non-cosine index to be rejected")
	}
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Fatalf("expected vector.ErrFingerprintMismatch in chain, got: %v", err)
	}
	if !strings.Contains(err.Error(), "similarity") {
		t.Fatalf("error must name the differing field, got: %v", err)
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("factory must not create or overwrite an existing index, got %d write request(s)", got)
	}
}
