package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	_ "github.com/wangle201210/go-rag/server/core/providers/openai"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// stage2c3Config applies a real YAML file to the production GoFrame instance.
// No t.Parallel: readRagConfig uses the production default adapter.
func stage2c3Config(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := gcfg.NewAdapterFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := g.Cfg().GetAdapter()
	g.Cfg().SetAdapter(a)
	t.Cleanup(func() { g.Cfg().SetAdapter(old) })
}

type capturedChat struct {
	hits  atomic.Int64
	auth  atomic.Value
	model atomic.Value
}

func (c *capturedChat) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		c.hits.Add(1)
		c.auth.Store(r.Header.Get("Authorization"))
		var body struct {
			Model string `json:"model"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		c.model.Store(body.Model)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1,
			"model":   body.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *capturedChat) String() string {
	return fmt.Sprintf("hits=%d auth=%v model=%v", c.hits.Load(), c.auth.Load(), c.model.Load())
}

// TestP21_LegacyChatUsesDedicatedEndpointAndCredential covers YAML → RagConfig →
// model construction → HTTP for a legacy Chat that must not reuse embedding
// endpoint or credentials.
func TestP21_LegacyChatUsesDedicatedEndpointAndCredential(t *testing.T) {
	var embedHits atomic.Int64
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embedHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"legacy-embed","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer embedSrv.Close()
	chat := &capturedChat{}
	chatSrv := chat.server(t)

	stage2c3Config(t, fmt.Sprintf(`vector:
  type: es
  indexName: offline
embedding:
  apiKey: embed-test-key
  baseURL: %s/v1
  model: legacy-embed
  dimensions: 2
chat:
  apiKey: chat-test-key
  baseURL: %s/v1
  model: legacy-chat
`, embedSrv.URL, chatSrv.URL))

	cfg, err := readRagConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChatAPIKey != "chat-test-key" || cfg.ChatBaseURL != chatSrv.URL+"/v1" {
		t.Fatalf("chat config not read: key=%q base=%q", cfg.ChatAPIKey, cfg.ChatBaseURL)
	}
	if cfg.EmbeddingAPIKey == cfg.ChatAPIKey || cfg.BaseURL == cfg.ChatBaseURL {
		t.Fatal("legacy chat must not share embedding endpoint/credential")
	}

	conf := &config.Config{
		APIKey:              cfg.EmbeddingAPIKey,
		BaseURL:             cfg.BaseURL,
		EmbeddingModel:      cfg.EmbeddingModel,
		EmbeddingDimensions: cfg.EmbeddingDimensions,
		ChatModel:           cfg.ChatModel,
		ChatAPIKey:          cfg.ChatAPIKey,
		ChatBaseURL:         cfg.ChatBaseURL,
	}
	cm, err := common.GetChatModelForRag(context.Background(), conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	if got := chat.hits.Load(); got != 1 {
		t.Fatalf("chat endpoint hits = %d, want 1 (%s)", got, chat)
	}
	if got, _ := chat.auth.Load().(string); got != "Bearer chat-test-key" {
		t.Fatalf("chat credential = %q, want chat-test-key", got)
	}
	if got, _ := chat.model.Load().(string); got != "legacy-chat" {
		t.Fatalf("chat model = %q, want legacy-chat", got)
	}
	if embedHits.Load() != 0 {
		t.Fatalf("embedding endpoint must not receive chat traffic, got %d", embedHits.Load())
	}
}

// TestP21_MixedProviderChatUsesRegistryNotLegacyChat covers the opposite mixed
// role: Provider Chat must win over the legacy chat endpoint/credential.
func TestP21_MixedProviderChatUsesRegistryNotLegacyChat(t *testing.T) {
	providerChat := &capturedChat{}
	providerSrv := providerChat.server(t)
	legacyChat := &capturedChat{}
	legacySrv := legacyChat.server(t)

	stage2c3Config(t, fmt.Sprintf(`vector:
  type: es
  indexName: offline
embedding:
  apiKey: embed-test-key
  baseURL: http://embedding.invalid/v1
  model: provider-embed
  dimensions: 2
chat:
  apiKey: legacy-test-key
  baseURL: %s/v1
  model: legacy-chat
provider:
  name: OpenAI
  base_url: %s/v1
  api_key: provider-test-key
  chat_model: provider-chat
  embedding_model: provider-embed
  embedding_dimensions: 2
  max_requests: 0
`, legacySrv.URL, providerSrv.URL))

	cfg, err := readRagConfig()
	if err != nil {
		t.Fatal(err)
	}

	processBudgetMu.Lock()
	oldBudget := processBudget
	processBudget = nil
	processBudgetMu.Unlock()
	defer func() {
		processBudgetMu.Lock()
		processBudget = oldBudget
		processBudgetMu.Unlock()
	}()

	reg, err := buildRegistry(context.Background(), cfg.Provider)
	if err != nil {
		t.Fatal(err)
	}
	conf := &config.Config{
		ChatModel:   cfg.ChatModel,
		ChatAPIKey:  cfg.ChatAPIKey,
		ChatBaseURL: cfg.ChatBaseURL,
		Provider:    cfg.Provider,
		Registry:    reg,
	}
	cm, err := common.GetChatModelForRag(context.Background(), conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
		t.Fatal(err)
	}
	if providerChat.hits.Load() != 1 {
		t.Fatalf("provider chat hits = %d, want 1 (%s)", providerChat.hits.Load(), providerChat)
	}
	if got, _ := providerChat.auth.Load().(string); got != "Bearer provider-test-key" {
		t.Fatalf("provider chat credential = %q", got)
	}
	if legacyChat.hits.Load() != 0 {
		t.Fatalf("legacy chat must not be used with Provider Chat: %s", legacyChat)
	}
}

// TestP23_ExplicitNullIsNotMissing covers the real YAML → readRagConfig chain for
// the strict boolean: missing is a default, explicit null and non-bools are errors.
func TestP23_ExplicitNullIsNotMissing(t *testing.T) {
	base := `vector:
  type: es
  indexName: offline
embedding:
  model: m
  dimensions: 2
chat:
  model: c
provider:
  name: openai
  embedding_model: m
  embedding_dimensions: 2
`
	cases := []struct {
		name    string
		extra   string
		wantErr bool
		want    bool
	}{
		{"missing", "", false, false},
		{"null", "  supports_dimensions_request: null\n", true, false},
		{"tilde", "  supports_dimensions_request: ~\n", true, false},
		{"true", "  supports_dimensions_request: true\n", false, true},
		{"false", "  supports_dimensions_request: false\n", false, false},
		{"string", "  supports_dimensions_request: \"true\"\n", true, false},
		{"number", "  supports_dimensions_request: 1\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage2c3Config(t, base+tc.extra)
			cfg, err := readRagConfig()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected explicit null/invalid type to be rejected")
				}
				if !strings.Contains(err.Error(), "must be a boolean") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Provider == nil || cfg.Provider.SupportsDimensionsRequest != tc.want {
				t.Fatalf("supports_dimensions_request = %#v, want %v", cfg.Provider, tc.want)
			}
		})
	}
}

// esFingerprintServer serves one concrete ES index whose stored fingerprint is
// the expected provider identity, allowing the factory binding path to run offline.
func esFingerprintServer(t *testing.T, fp *vector.Fingerprint, dims int) *httptest.Server {
	t.Helper()
	srv, _ := esFingerprintServerWithSchema(t, fp, dims, "cosine")
	return srv
}

// esFingerprintServerWithSchema additionally reports the number of write requests
// (anything other than HEAD/GET) so a test can prove the factory neither created
// nor overwrote an index.
func esFingerprintServerWithSchema(t *testing.T, fp *vector.Fingerprint, dims int, similarity string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	writes := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/test":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/test":
			props := map[string]any{}
			for _, field := range []string{"content_vector", "qa_content_vector"} {
				props[field] = map[string]any{"type": "dense_vector", "dims": dims, "similarity": similarity, "index": true}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"test": map[string]any{
					"settings": map[string]any{"index": map[string]any{"uuid": "uuid-test"}},
					"mappings": map[string]any{
						"_meta":      map[string]any{"go-rag-fingerprint": fp},
						"properties": props,
					},
				},
			})
		default:
			writes.Add(1)
			t.Logf("unexpected ES request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, writes
}

// TestP13_FactoryPathBindsTargetForDirectEntry proves the factory installs a
// verifiable binding while the direct entry point stays fail-closed without one.
func TestP13_FactoryPathBindsTargetForDirectEntry(t *testing.T) {
	fp, err := vector.NewFingerprint(vector.FingerprintVersion, "text-embedding-3-large", 2, "openai", "cosine")
	if err != nil {
		t.Fatal(err)
	}
	esSrv := esFingerprintServer(t, fp, 2)

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

	oldRegistry := common.GetRegistry()
	defer common.SetRegistry(oldRegistry)
	processBudgetMu.Lock()
	oldBudget := processBudget
	processBudget = nil
	processBudgetMu.Unlock()
	defer func() {
		processBudgetMu.Lock()
		processBudget = oldBudget
		processBudgetMu.Unlock()
	}()

	vs, err := vector.NewVectorStore(&cfg.VectorCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer vs.Close()

	rag, err := (&defaultRagFactory{}).Create(cfg, vs)
	if err != nil {
		t.Fatalf("factory path with a verifiable binding must succeed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rag.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	// The same identity without the factory's binding must fail closed at the
	// direct common.NewEmbedding entry.
	direct := &config.Config{
		IndexName:                "test",
		ManagedResourceLifecycle: true,
		EmbeddingDimensions:      2,
		Provider:                 cfg.Provider,
		Registry:                 common.GetRegistry(),
	}
	if _, err := common.NewEmbedding(context.Background(), direct); !errors.Is(err, common.ErrEmbeddingProfileNotReady) {
		t.Fatalf("direct entry without binding must fail closed, got: %v", err)
	}
}
