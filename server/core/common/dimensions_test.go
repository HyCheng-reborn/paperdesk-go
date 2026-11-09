package common

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/wangle201210/go-rag/server/core/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDimensions_LegacyEmbeddingHTTP(t *testing.T) {
	for _, model := range []string{"text-embedding-3-large", "BAAI/bge-m3", "Qwen/Qwen3-Embedding-8B"} {
		t.Run(model, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
					t.Error(e)
				}
				_, present := req["dimensions"]
				want := model != "BAAI/bge-m3"
				if present != want {
					t.Errorf("dimensions presence %v", present)
				}
				if want && req["dimensions"] != float64(768) {
					t.Errorf("request dims %v", req["dimensions"])
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": make([]float64, 768)}}})
			}))
			defer srv.Close()
			e, err := NewEmbedding(context.Background(), &config.Config{APIKey: "offline", BaseURL: srv.URL, EmbeddingModel: model, EmbeddingDimensions: 768})
			if err != nil {
				t.Fatal(err)
			}
			vs, err := e.EmbedStrings(context.Background(), []string{"q"})
			if err != nil || len(vs[0]) != 768 {
				t.Fatal(vs, err)
			}
		})
	}
}
func TestDimensions_BadResponseAndGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 2}}}})
	}))
	defer srv.Close()
	c := &config.Config{APIKey: "offline", BaseURL: srv.URL, EmbeddingModel: "text-embedding-3-large", EmbeddingDimensions: 768}
	e, err := NewEmbedding(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.EmbedStrings(context.Background(), []string{"q"}); !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Fatal(err)
	}
	c.Provider = &config.ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}
	if _, err = NewEmbedding(context.Background(), c); !errors.Is(err, ErrEmbeddingGuardNotReady) {
		t.Fatal(err)
	}
	c.Provider = nil
	c.EmbeddingDimensions = -1
	if _, err = NewEmbedding(context.Background(), c); !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Fatal(err)
	}
}

func TestDimensions_CompatibilityESBuilder(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method == "HEAD" {
			w.WriteHeader(404)
			return
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		props := body["mappings"].(map[string]any)["properties"].(map[string]any)
		for _, p := range props {
			if v, ok := p.(map[string]any)["dims"]; ok && v != float64(768) {
				t.Errorf("dims %v", v)
			}
		}
		w.Write([]byte(`{"acknowledged":true,"index":"test"}`))
	}))
	defer srv.Close()
	client, e := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
	if e != nil {
		t.Fatal(e)
	}
	if e = CreateIndexIfNotExists(context.Background(), client, "test", 768); e != nil {
		t.Fatal(e)
	}
	if e = CreateIndexIfNotExists(context.Background(), client, "test", -1); !errors.Is(e, config.ErrEmbeddingDimensions) {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
