package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gogf/gf/v2/container/gvar"
	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
)

type dimensionGetter map[string]any

func (g dimensionGetter) Get(_ context.Context, k string) (*gvar.Var, error) {
	return gvar.New(g[k]), nil
}
func TestDimensions_ConfigReader(t *testing.T) {
	a, b, e := readEmbeddingDimensions(context.Background(), dimensionGetter{"embedding.dimensions": 768, "provider.embedding_dimensions": 768})
	if e != nil || a != 768 || b != 768 {
		t.Fatal(a, b, e)
	}
	for _, v := range []any{0, -1, "768", 1.5, true} {
		if _, _, e = readEmbeddingDimensions(context.Background(), dimensionGetter{"provider.embedding_dimensions": v}); !errors.Is(e, config.ErrEmbeddingDimensions) {
			t.Fatal(v, e)
		}
	}
}

type dimensionVSFactory struct {
	got int
	vs  *fakeVectorStore
}

func (f *dimensionVSFactory) Create(c *vector.Config) (vector.VectorStore, error) {
	f.got = c.EmbeddingDimensions
	return f.vs, nil
}

type dimensionRagFactory struct {
	got  int
	fail error
}

func (f *dimensionRagFactory) Create(c RagConfig, v vector.VectorStore) (*core.Rag, error) {
	f.got = c.EmbeddingDimensions
	return &core.Rag{}, f.fail
}
func TestDimensions_InitFactoryPropagation(t *testing.T) {
	resetState()
	defer resetState()
	cfg := validRagConfig()
	cfg.EmbeddingDimensions = 768
	vf := &dimensionVSFactory{vs: &fakeVectorStore{}}
	rf := &dimensionRagFactory{}
	if e := InitWith(cfg, vf, rf); e != nil {
		t.Fatal(e)
	}
	if vf.got != 768 || rf.got != 768 {
		t.Fatal(vf.got, rf.got)
	}
	if cfg.VectorCfg.EmbeddingDimensions != 0 {
		t.Fatal("mutated caller")
	}
}
func TestDimensions_InitFailClosed(t *testing.T) {
	for _, scenario := range []string{"conflict", "negative", "guard"} {
		t.Run(scenario, func(t *testing.T) {
			resetState()
			defer resetState()
			c := validRagConfig()
			c.EmbeddingDimensions = 768
			want := config.ErrEmbeddingDimensionConflict
			switch scenario {
			case "conflict":
				c.VectorCfg.EmbeddingDimensions = 512
			case "negative":
				c.EmbeddingDimensions = -1
				want = config.ErrEmbeddingDimensions
			case "guard":
				c.Provider = &config.ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}
				want = common.ErrEmbeddingGuardNotReady
			}
			vf := &dimensionVSFactory{vs: &fakeVectorStore{}}
			rf := &dimensionRagFactory{}
			if e := InitWith(c, vf, rf); !errors.Is(e, want) {
				t.Fatal(e)
			}
			if vf.got != 0 || rf.got != 0 {
				t.Fatal("factory called on invalid config")
			}
		})
	}
}

func TestDimensions_DefaultFactoryAssembly(t *testing.T) {
	var requests atomic.Int32
	// F1: Pre-populate valid fingerprint in _meta (no auto-backfill).
	// The fingerprint must match the config: model=text-embedding-3-large, dims=768, provider=openai.
	initFP, _ := vector.NewFingerprint("v1", "text-embedding-3-large", 768, "openai", "cosine")
	initFPMeta, _ := json.Marshal(map[string]any{
		"go-rag-fingerprint": map[string]any{
			"version":            initFP.Version,
			"embedding_model_id": initFP.EmbeddingModelID,
			"dimensions":         initFP.Dimensions,
			"provider_name":      initFP.ProviderName,
			"similarity":         initFP.Similarity,
			"digest":             initFP.Digest(),
		},
		"go-rag-resource-id": "test-uuid-123",
	})
	var storedMeta string = string(initFPMeta)
	var indexCreated bool = false // Track whether index has been created
	// Pre-build mapping properties for schema verification (C2)
	// Dimensions are 768 as set in cfg below
	mappingProps := `{
		"content": {"type": "text"},
		"extra": {"type": "text"},
		"knowledge_name": {"type": "keyword"},
		"content_vector": {"type": "dense_vector", "dims": 768, "index": true, "similarity": "cosine"},
		"qa_content_vector": {"type": "dense_vector", "dims": 768, "index": true, "similarity": "cosine"}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		// Handle different ES API calls
		if r.Method == "HEAD" {
			// Index exists check - return 404 until index is created
			if indexCreated {
				w.WriteHeader(200)
			} else {
				w.WriteHeader(404)
			}
			return
		}
		if r.Method == "PUT" && r.URL.Path == "/test/_mapping" {
			// Put mapping (fingerprint write) - merge _meta into stored_meta
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				if meta, ok := body["_meta"].(map[string]any); ok {
					if metaJSON, err := json.Marshal(meta); err == nil {
						storedMeta = string(metaJSON)
					}
				}
			}
			w.WriteHeader(200)
			w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/test" {
			// Get index - return mappings + _meta + settings.index.uuid
			w.WriteHeader(200)
			resp := fmt.Sprintf(`{"test":{"mappings":{"properties":%s,"_meta":%s},"settings":{"index":{"uuid":"test-uuid-123"}}}}`, mappingProps, storedMeta)
			w.Write([]byte(resp))
			return
		}
		if r.Method == "POST" && r.URL.Path == "/test/_create" {
			// Create index
			indexCreated = true
			w.WriteHeader(200)
			w.Write([]byte(`{"acknowledged":true,"index":"test"}`))
			return
		}
		if r.Method == "PUT" && r.URL.Path == "/test" {
			// Create index (PUT method)
			indexCreated = true
			w.WriteHeader(200)
			w.Write([]byte(`{"acknowledged":true,"index":"test"}`))
			return
		}
		if r.Method == "DELETE" && r.URL.Path == "/test" {
			// Delete index (best-effort cleanup on schema verification failure)
			indexCreated = false
			w.WriteHeader(200)
			w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	cfg := validRagConfig()
	cfg.VectorCfg = vector.Config{Type: "es", IndexName: "test", ES: &vector.ESConfig{Address: srv.URL}, EmbeddingDimensions: 768}
	cfg.VectorCfg.ManagedResourceLifecycle = true // local fake satisfies the managed-lifecycle contract
	cfg.EmbeddingDimensions = 768
	disabled := false
	cfg.QAEnabled = &disabled
	cfg.EmbeddingModel = "text-embedding-3-large"
	cfg.Provider = &config.ProviderConfig{Name: "openai", BaseURL: srv.URL, APIKey: "offline", ChatModel: "offline"}
	oldRegistry := common.GetRegistry()
	defer common.SetRegistry(oldRegistry)
	processBudgetMu.Lock()
	oldBudget := processBudget
	processBudget = nil
	processBudgetMu.Unlock()
	defer func() { processBudgetMu.Lock(); processBudget = oldBudget; processBudgetMu.Unlock() }()
	vs, e := vector.NewVectorStore(&cfg.VectorCfg)
	if e != nil {
		t.Fatal(e)
	}
	defer vs.Close()
	r, e := (&defaultRagFactory{}).Create(cfg, vs)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	// Should have: 1 HEAD (exists) + 1 GET (read fp) + 1 PUT (write fp) + 1 GET (verify fp)
	if requests.Load() < 3 {
		t.Fatalf("expected at least 3 requests, got %d", requests.Load())
	}
	if common.GetRegistry() == nil {
		t.Fatal("factory did not publish registry")
	}
}
func TestDimensions_NormalizeProviderSnapshot(t *testing.T) {
	p := &config.ProviderConfig{Name: "openai", EmbeddingModel: "m"}
	c := validRagConfig()
	c.EmbeddingDimensions = 768
	c.Provider = p
	n, e := normalizeDimensions(c)
	if e != nil {
		t.Fatal(e)
	}
	if n.Provider.EmbeddingDimensions != 768 || p.EmbeddingDimensions != 0 || n.VectorCfg.EmbeddingDimensions != 768 {
		t.Fatal("profile fallback not snapshotted")
	}
	profile, e := providers.NewProviderProfile(providers.ProviderProfile{ProviderName: n.Provider.Name, Protocol: providers.ProtocolOpenAICompat, Endpoint: "http://offline", Model: n.Provider.EmbeddingModel, Dimensions: n.Provider.EmbeddingDimensions})
	if e != nil || profile.Dimensions != 768 {
		t.Fatal(profile, e)
	}
}
