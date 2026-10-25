package vector

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDimensions_ESBothCreatePaths(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		var body struct {
			Mappings struct {
				Properties map[string]struct {
					Dims int `json:"dims"`
				} `json:"properties"`
			} `json:"mappings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, k := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
			if body.Mappings.Properties[k].Dims != 768 {
				t.Errorf("%s dims=%d", k, body.Mappings.Properties[k].Dims)
			}
		}
		w.Write([]byte(`{"acknowledged":true,"index":"test"}`))
	}))
	defer srv.Close()
	v, e := NewVectorStore(&Config{Type: "es", ES: &ESConfig{Address: srv.URL}, EmbeddingDimensions: 768})
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	if e = v.CreateIndex(context.Background(), "test", nil); e != nil {
		t.Fatal(e)
	}
	s := v.(*ESVectorStore)
	c := &config.Config{Client: s.client, IndexName: "test", EmbeddingDimensions: 768}
	if e = c.CreateIndex(context.Background()); e != nil {
		t.Fatal(e)
	}
	c.EmbeddingDimensions = -1
	if e = c.CreateIndex(context.Background()); !errors.Is(e, config.ErrEmbeddingDimensions) {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("unexpected sends %d", calls)
	}
}
func TestDimensions_FactoryRejectsBeforeClient(t *testing.T) {
	for _, kind := range []string{"es", "qdrant"} {
		if _, e := NewVectorStore(&Config{Type: kind, EmbeddingDimensions: -1}); !errors.Is(e, config.ErrEmbeddingDimensions) {
			t.Fatal(kind, e)
		}
	}
}

func TestDimensions_QdrantBothCreatePaths(t *testing.T) {
	creates := 0
	fields := 0
	c := testutil.Qdrant(t, func(req any) {
		switch q := req.(type) {
		case *qdrant.CreateCollection:
			creates++
			for _, v := range q.VectorsConfig.GetParamsMap().Map {
				if v.Size != 768 {
					t.Errorf("collection dimension=%d", v.Size)
				}
			}
		case *qdrant.CreateFieldIndexCollection:
			fields++
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &QdrantVectorStore{client: c, dimensions: 768}
	if e := s.CreateIndex(ctx, "test", nil); e != nil {
		t.Fatal(e)
	}
	conf := &config.Config{QdrantClient: c, IndexName: "test", EmbeddingDimensions: 768}
	if e := conf.CreateIndex(ctx); e != nil {
		t.Fatal(e)
	}
	if creates != 2 || fields != 2 {
		t.Fatal(creates, fields)
	}
}
