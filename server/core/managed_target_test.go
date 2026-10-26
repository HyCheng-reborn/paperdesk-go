package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
	coreindexer "github.com/wangle201210/go-rag/server/core/indexer"
	coreretriever "github.com/wangle201210/go-rag/server/core/retriever"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// Actual production async graph and ES client, with only the remote endpoints
// replaced. Replacement happens inside embedding, after the entry gate.
func TestManagedTargetActualGraphs(t *testing.T) {
	for _, operation := range []string{"index", "retrieve"} {
		for _, scenario := range []string{"stable", "replaced", "missing", "alias", "unavailable", "cancelled"} {
			replace := scenario != "stable"
			name := operation + "/" + scenario
			t.Run(name, func(t *testing.T) {
				var mu sync.Mutex
				uuid := "uuid-A"
				changed := false
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				writes, searches, embeddings := 0, 0, 0
				fp, err := vector.NewFingerprint("v1", "text-embedding-3-large", 2, "legacy", "cosine")
				if err != nil {
					t.Fatal(err)
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Elastic-Product", "Elasticsearch")
					switch {
					case r.URL.Path == "/v1/embeddings":
						var body struct {
							Input []string `json:"input"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						mu.Lock()
						embeddings++
						changed = true
						if scenario == "replaced" {
							uuid = "uuid-B"
						}
						mu.Unlock()
						if scenario == "cancelled" {
							cancel()
						}
						data := make([]any, len(body.Input))
						for i := range data {
							data[i] = map[string]any{"index": i, "embedding": []float64{0.1, 0.2}}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": fp.EmbeddingModelID, "usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1}})
					case strings.HasSuffix(r.URL.Path, "/_bulk"):
						_, _ = io.Copy(io.Discard, r.Body)
						mu.Lock()
						writes++
						mu.Unlock()
						_, _ = w.Write([]byte(`{"errors":false,"items":[{"index":{"_index":"idx","_id":"doc1","status":201}}]}`))
					case strings.HasSuffix(r.URL.Path, "/_search"):
						mu.Lock()
						searches++
						mu.Unlock()
						_, _ = w.Write([]byte(`{"took":0,"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`))
					case r.Method == "GET" && r.URL.Path == "/idx":
						mu.Lock()
						current := uuid
						active := changed
						mu.Unlock()
						if active && scenario == "missing" {
							w.WriteHeader(404)
							return
						}
						if active && scenario == "unavailable" {
							w.WriteHeader(503)
							return
						}
						targetName := "idx"
						if active && scenario == "alias" {
							targetName = "different-physical-name"
						}
						props := map[string]any{}
						for _, field := range []string{"content_vector", "qa_content_vector"} {
							props[field] = map[string]any{"type": "dense_vector", "dims": 2, "similarity": "cosine", "index": true}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{targetName: map[string]any{"settings": map[string]any{"index": map[string]any{"uuid": current}}, "mappings": map[string]any{"_meta": map[string]any{"go-rag-fingerprint": fp}, "properties": props}}})
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer srv.Close()
				vs, err := vector.NewESVectorStore(&vector.ESConfig{Address: srv.URL}, 2)
				if err != nil {
					t.Fatal(err)
				}
				guard, err := vector.BindPhysicalTarget(ctx, vs, "idx", fp)
				if err != nil {
					t.Fatal(err)
				}
				off := false
				conf := &config.Config{Client: vs.GetClient(), IndexName: "idx", APIKey: "offline", BaseURL: srv.URL + "/v1", EmbeddingModel: fp.EmbeddingModelID, EmbeddingDimensions: 2, QAEnabled: &off, ManagedResourceLifecycle: true, VectorIOGuard: guard}
				checkError := func(err error) {
					t.Helper()
					var want error
					switch scenario {
					case "replaced":
						want = vector.ErrResourceReplaced
					case "missing":
						want = vector.ErrFingerprintMissing
					case "alias":
						want = vector.ErrPhysicalTargetRequired
					case "cancelled":
						want = context.Canceled
					}
					if want != nil && !errors.Is(err, want) {
						t.Fatalf("expected %v: %v", want, err)
					}
					if scenario == "unavailable" && err == nil {
						t.Fatal("uncertain response must fail closed")
					}
					if !replace && err != nil {
						t.Fatal(err)
					}
				}
				if operation == "index" {
					graph, err := coreindexer.BuildIndexerAsync(ctx, conf, nil)
					if err != nil {
						t.Fatal(err)
					}
					r := &Rag{conf: conf, idxerAsync: graph}
					if err := r.VerifyFingerprint(ctx, vs); err != nil {
						t.Fatal(err)
					}
					_, err = r.IndexAsync(ctx, &IndexAsyncReq{KnowledgeName: "kb", Docs: []*schema.Document{{ID: "doc1", Content: "offline text", MetaData: map[string]any{}}}})
					checkError(err)
				} else {
					graph, err := coreretriever.BuildRetriever(ctx, conf)
					if err != nil {
						t.Fatal(err)
					}
					_, err = graph.Invoke(ctx, "offline query")
					checkError(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if embeddings != 1 {
					t.Fatalf("embeddings=%d", embeddings)
				}
				if replace && (writes != 0 || searches != 0) {
					t.Fatalf("replacement reached backend: writes=%d searches=%d", writes, searches)
				}
				if !replace && operation == "index" && writes != 1 {
					t.Fatalf("writes=%d", writes)
				}
				if !replace && operation == "retrieve" && searches != 1 {
					t.Fatalf("searches=%d", searches)
				}
			})
		}
	}
}

func TestManagedTargetCoreNewDoesNotBypassPolicy(t *testing.T) {
	conf := &config.Config{IndexName: "idx", EmbeddingModel: "m", EmbeddingDimensions: 2}
	_, err := New(context.Background(), conf)
	if !errors.Is(err, vector.ErrManagedLifecycleRequired) {
		t.Fatal(err)
	}
	conf.ManagedResourceLifecycle = true
	_, err = New(context.Background(), conf)
	if !errors.Is(err, vector.ErrResourceBindingNotReady) {
		t.Fatal(err)
	}
}
