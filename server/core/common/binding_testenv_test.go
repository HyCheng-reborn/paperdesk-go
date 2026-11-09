package common

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

const testBindingIndex = "test-index"

// testBindingEnv is a local, offline stand-in for a concrete ES index. It serves
// the index metadata that BindPhysicalTarget and TargetBinding.Verify read; no
// real ES, model or network dependency is involved.
type testBindingEnv struct {
	Server      *httptest.Server
	Store       *vector.ESVectorStore
	Binding     *vector.TargetBinding
	Fingerprint *vector.Fingerprint
	Reads       *atomic.Int64
}

// newTestBindingEnv builds a verified binding for (provider, model, dims).
func newTestBindingEnv(t *testing.T, provider, model string, dims int) *testBindingEnv {
	t.Helper()
	return newTestBindingEnvWithSimilarity(t, provider, model, dims, vector.SimilarityCosine)
}

// esDistance maps a fingerprint similarity to the ES dense_vector distance name.
func esDistance(similarity string) string {
	switch similarity {
	case "dot":
		return "dot_product"
	case "euclidean":
		return "l2_norm"
	default:
		return similarity
	}
}

// newTestBindingEnvWithSimilarity builds a verified binding for an arbitrary
// similarity. The local fake ES serves a schema consistent with that fingerprint,
// so only the caller's expectations (not the backend) decide acceptance.
func newTestBindingEnvWithSimilarity(t *testing.T, provider, model string, dims int, similarity string) *testBindingEnv {
	t.Helper()
	fp, err := vector.NewFingerprint(vector.FingerprintVersion, model, dims, provider, similarity)
	if err != nil {
		t.Fatalf("new fingerprint: %v", err)
	}
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ES v8 client requires this header in every response.
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method != http.MethodGet || r.URL.Path != "/"+testBindingIndex {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reads.Add(1)
		props := map[string]any{}
		for _, field := range []string{"content_vector", "qa_content_vector"} {
			props[field] = map[string]any{
				"type": "dense_vector", "dims": dims, "similarity": esDistance(similarity), "index": true,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			testBindingIndex: map[string]any{
				"settings": map[string]any{"index": map[string]any{"uuid": "uuid-test"}},
				"mappings": map[string]any{
					"_meta":      map[string]any{"go-rag-fingerprint": fp},
					"properties": props,
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	store, err := vector.NewESVectorStore(&vector.ESConfig{Address: srv.URL}, dims)
	if err != nil {
		t.Fatalf("new es store: %v", err)
	}
	binding, err := vector.BindPhysicalTarget(context.Background(), store, testBindingIndex, fp)
	if err != nil {
		t.Fatalf("bind physical target: %v", err)
	}
	return &testBindingEnv{
		Server:      srv,
		Store:       store,
		Binding:     binding,
		Fingerprint: fp,
		Reads:       &reads,
	}
}

// bindTestTarget installs the verified binding that the P1-3 contract requires
// for the direct NewEmbedding entry point, deriving identity from conf.Provider.
func bindTestTarget(t *testing.T, conf *config.Config) *testBindingEnv {
	t.Helper()
	if conf.Provider == nil {
		t.Fatal("bindTestTarget requires conf.Provider")
	}
	dims := conf.Provider.EmbeddingDimensions
	if dims <= 0 {
		dims = conf.EmbeddingDimensions
	}
	env := newTestBindingEnv(t, conf.Provider.Name, conf.Provider.EmbeddingModel, dims)
	conf.Client = env.Store.GetClient()
	conf.IndexName = testBindingIndex
	conf.ManagedResourceLifecycle = true
	conf.PhysicalBinding = env.Binding
	conf.VectorIOGuard = env.Binding.Guard()
	return env
}
