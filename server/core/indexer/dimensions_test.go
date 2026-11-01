package indexer

import (
	"context"
	"encoding/json"
	einoindexer "github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDimensions_QdrantSyncAsyncAssembly(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			writes := 0
			client := testutil.Qdrant(t, func(req any) {
				if u, ok := req.(*qdrant.UpsertPoints); ok {
					writes++
					for _, p := range u.Points {
						for _, v := range p.Vectors.GetVectors().Vectors {
							if len(v.Data) != 768 {
								t.Errorf("stored %d dimensions", len(v.Data))
							}
						}
					}
				}
			})
			srv := testutil.Embedding(t, 768)
			c := &config.Config{QdrantClient: client, IndexName: "offline", APIKey: "offline", BaseURL: srv.URL, EmbeddingModel: "text-embedding-3-large", EmbeddingDimensions: 768}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var idx einoindexer.Indexer
			var e error
			if async {
				idx, e = newAsyncIndexer(ctx, c)
			} else {
				idx, e = newIndexer(ctx, c)
			}
			if e != nil {
				t.Fatal(e)
			}
			q := idx.(*QdrantIndexer)
			if q.config.VectorDim != 768 || q.config.IsAsync != async {
				t.Fatal(q.config)
			}
			ctx = context.WithValue(ctx, coretypes.KnowledgeName, "kb")
			if _, e = idx.Store(ctx, []*schema.Document{{Content: "doc", MetaData: map[string]any{coretypes.FieldQAContent: "question"}}}); e != nil {
				t.Fatal(e)
			}
			if writes != 1 {
				t.Fatal(writes)
			}
		})
	}
}

func TestDimensions_ESIndexersAssembly(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(map[bool]string{false: "sync", true: "async"}[async], func(t *testing.T) {
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				w.Header().Set("Content-Type", "application/json")
				dec := json.NewDecoder(r.Body)
				var action map[string]any
				var doc map[string]any
				if e := dec.Decode(&action); e != nil {
					t.Error(e)
				}
				if e := dec.Decode(&doc); e != nil {
					t.Error(e)
				}
				if v, ok := doc[coretypes.FieldContentVector].([]any); !ok || len(v) != 768 {
					t.Errorf("stored vector %T length mismatch", doc[coretypes.FieldContentVector])
				}
				writes.Add(1)
				w.Write([]byte(`{"errors":false,"items":[{"index":{"_index":"offline","_id":"id","status":201}}]}`))
			}))
			defer srv.Close()
			client, e := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
			if e != nil {
				t.Fatal(e)
			}
			emb := testutil.Embedding(t, 768)
			c := &config.Config{Client: client, IndexName: "offline", APIKey: "offline", BaseURL: emb.URL, EmbeddingModel: "text-embedding-3-large", EmbeddingDimensions: 768}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			idx, e := newIndexer(ctx, c)
			if async {
				idx, e = newAsyncIndexer(ctx, c)
			}
			if e != nil {
				t.Fatal(e)
			}
			ctx = context.WithValue(ctx, coretypes.KnowledgeName, "kb")
			if _, e = idx.Store(ctx, []*schema.Document{{ID: "id", Content: "doc", MetaData: map[string]any{}}}); e != nil {
				t.Fatal(e)
			}
			if writes.Load() != 1 {
				t.Fatal(writes.Load())
			}
		})
	}
}
