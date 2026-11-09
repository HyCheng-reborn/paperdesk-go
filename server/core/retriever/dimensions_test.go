package retriever

import (
	"context"
	"encoding/json"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDimensions_QdrantRetrieverAssembly(t *testing.T) {
	for _, field := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		t.Run(field, func(t *testing.T) {
			queries := 0
			client := testutil.Qdrant(t, func(req any) {
				if q, ok := req.(*qdrant.QueryPoints); ok {
					queries++
					if len(q.Query.GetNearest().GetDense().Data) != 768 || q.GetUsing() != field {
						t.Errorf("wrong query %v", q)
					}
				}
			})
			srv := testutil.Embedding(t, 768)
			c := &config.Config{QdrantClient: client, IndexName: "offline", APIKey: "offline", BaseURL: srv.URL, EmbeddingModel: "text-embedding-3-large", EmbeddingDimensions: 768}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, coretypes.RetrieverFieldKey, field)
			ret, e := newRetriever(ctx, c)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = ret.Retrieve(ctx, "query"); e != nil {
				t.Fatal(e)
			}
			if queries != 1 {
				t.Fatal(queries)
			}
		})
	}
}

func TestDimensions_ESRetrieverAssembly(t *testing.T) {
	queries := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries++
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		var req map[string]any
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		script := req["query"].(map[string]any)["script_score"].(map[string]any)["script"].(map[string]any)
		params := script["params"].(map[string]any)
		found := false
		for _, v := range params {
			if a, ok := v.([]any); ok && len(a) == 768 {
				found = true
			}
		}
		if !found {
			t.Error("query dimension not 768")
		}
		w.Write([]byte(`{"took":0,"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`))
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
	ret, e := newRetriever(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ret.Retrieve(ctx, "query"); e != nil {
		t.Fatal(e)
	}
	if queries != 1 {
		t.Fatal(queries)
	}
}
