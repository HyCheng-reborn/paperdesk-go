package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// TestP1_VersionedProviderEmbeddingConfigFailsClosed walks the real
// YAML → readRagConfig → ProviderProfile → Registry chain. A versioned embedding
// model must not produce a fingerprint (so the factory cannot create or claim an
// index) and must not build an embed role that would request the bare model name.
func TestP1_VersionedProviderEmbeddingConfigFailsClosed(t *testing.T) {
	stage2c3Config(t, `vector:
  type: es
  indexName: offline
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
`)

	cfg, err := readRagConfig()
	if err != nil {
		t.Fatalf("readRagConfig: %v", err)
	}
	if cfg.Provider == nil || cfg.Provider.EmbeddingModel != "m@v1" {
		t.Fatalf("provider embedding model not read: %+v", cfg.Provider)
	}

	conf := &config.Config{
		IndexName: cfg.VectorCfg.IndexName,
		Provider:  cfg.Provider,
	}
	if _, err := core.ComputeFingerprintMeta(conf); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("fingerprint must fail closed for a versioned model, got: %v", err)
	}

	// ProcessBudget 是进程级单例：先清空，避免其他测试遗留状态影响本用例。
	resetProcessBudget(t)

	reg, err := buildRegistry(context.Background(), cfg.Provider)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if _, err := reg.GetEmbedder(context.Background()); !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("GetEmbedder must fail closed for a versioned model, got: %v", err)
	}
}
