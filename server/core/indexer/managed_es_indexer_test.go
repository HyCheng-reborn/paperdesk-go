package indexer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino-ext/components/indexer/es8"
	"github.com/cloudwego/eino/components/embedding"
	componentindexer "github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
)

type managedTestEmbedder struct {
	calls        int
	beforeReturn func()
}

func (e *managedTestEmbedder) EmbedStrings(ctx context.Context, texts []string, _ ...embedding.Option) ([][]float64, error) {
	e.calls++
	if e.beforeReturn != nil {
		e.beforeReturn()
	}
	vectors := make([][]float64, len(texts))
	for n := range texts {
		vectors[n] = []float64{float64(n), 1}
	}
	return vectors, nil
}

func TestManagedBulkBoundedBatchesAndPhysicalTarget(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		requests.Add(1)
		if r.URL.Path != "/physical/_bulk" {
			t.Errorf("target=%s", r.URL.Path)
		}
		scanner := bufio.NewScanner(r.Body)
		var items []any
		for scanner.Scan() {
			var meta struct {
				Index struct {
					ID     string `json:"_id"`
					Target string `json:"_index"`
				} `json:"index"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
				t.Error(err)
				return
			}
			if meta.Index.Target != "physical" {
				t.Error(meta)
			}
			if !scanner.Scan() {
				t.Error("missing document line")
				return
			}
			var fields map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
				t.Error(err)
				return
			}
			if len(fields["content_vector"].([]any)) != 2 || len(fields["qa_content_vector"].([]any)) != 2 {
				t.Error(fields)
			}
			items = append(items, map[string]any{"index": map[string]any{"_index": "physical", "_id": meta.Index.ID, "status": 201}})
		}
		if err := scanner.Err(); err != nil {
			t.Error(err)
		}
		if len(items) != 1 {
			t.Errorf("two vector fields must fit within limit 3, documents=%d", len(items))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": false, "items": items})
	}))
	defer srv.Close()
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	embedder := &managedTestEmbedder{}
	idx := &managedESIndexer{conf: &es8.IndexerConfig{Client: client, Index: "physical", BatchSize: 3, Embedding: embedder,
		DocumentToFields: func(_ context.Context, doc *schema.Document) (map[string]es8.FieldValue, error) {
			return map[string]es8.FieldValue{"content": {Value: doc.Content, EmbedKey: "content_vector"}, "qa": {Value: "qa", EmbedKey: "qa_content_vector"}}, nil
		}}, guard: func(ctx context.Context) error { return ctx.Err() }}
	docs := make([]*schema.Document, 3)
	for n := range docs {
		docs[n] = &schema.Document{ID: strconv.Itoa(n), Content: "offline"}
	}
	ids, err := idx.Store(context.Background(), docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || requests.Load() != 3 || embedder.calls != 3 {
		t.Fatal(ids, requests.Load(), embedder.calls)
	}
}

func TestManagedBulkRejectsBeforeHTTPAndValidatesAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"changed", "mapping-error", "override", "partial", "wrong-target", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("X-Elastic-Product", "Elasticsearch")
				status, target := 500, "physical"
				if scenario == "wrong-target" {
					status, target = 201, "different-resource"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"errors": false, "items": []any{map[string]any{"index": map[string]any{"_index": target, "_id": "doc", "status": status}}}})
			}))
			defer srv.Close()
			client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("target changed")
			changed := false
			embedder := &managedTestEmbedder{beforeReturn: func() {
				if scenario == "changed" {
					changed = true
				}
				if scenario == "cancelled" {
					cancel()
				}
			}}
			idx := &managedESIndexer{conf: &es8.IndexerConfig{Client: client, Index: "physical", BatchSize: 10, Embedding: embedder,
				DocumentToFields: func(_ context.Context, doc *schema.Document) (map[string]es8.FieldValue, error) {
					if scenario == "mapping-error" {
						return nil, sentinel
					}
					return map[string]es8.FieldValue{"content": {Value: doc.Content, EmbedKey: "content_vector"}}, nil
				}}, guard: func(ctx context.Context) error {
				if changed {
					return sentinel
				}
				return ctx.Err()
			}}
			var opts []componentindexer.Option
			if scenario == "override" {
				opts = append(opts, componentindexer.WithEmbedding(embedder))
			}
			ids, err := idx.Store(ctx, []*schema.Document{{ID: "doc", Content: "offline"}}, opts...)
			if err == nil || ids != nil {
				t.Fatal(ids, err)
			}
			if scenario == "changed" || scenario == "mapping-error" {
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			}
			if scenario == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if scenario == "partial" || scenario == "wrong-target" {
				if requests.Load() != 1 {
					t.Fatal(requests.Load())
				}
			} else if requests.Load() != 0 {
				t.Fatalf("rejected operation reached bulk: %d", requests.Load())
			}
		})
	}
}
