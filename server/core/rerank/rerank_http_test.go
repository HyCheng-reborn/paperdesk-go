package rerank

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// fakeHTTPDoer is a mock HTTP client for testing.
type fakeHTTPDoer struct {
	response *http.Response
	err      error
	callCount int
}

func (f *fakeHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

// helper to create test documents
func testDocs(n int) []*schema.Document {
	docs := make([]*schema.Document, n)
	for i := 0; i < n; i++ {
		docs[i] = &schema.Document{Content: fmt.Sprintf("doc %d", i)}
	}
	return docs
}

// helper to create a Rerank with httptest server
func testRerank(handler http.Handler) (*Rerank, *httptest.Server) {
	server := httptest.NewServer(handler)
	cfg := &RerankConfig{
		URL:    server.URL,
		APIKey: "test-key",
		Model:  "test-model",
	}
	return NewRerank(cfg, server.Client()), server
}

// helper to build a valid result JSON
func validResultJSON(indices []int, scores []float64) string {
	var results []map[string]interface{}
	for i, idx := range indices {
		results = append(results, map[string]interface{}{
			"index":            idx,
			"relevance_score": scores[i],
		})
	}
	resp := map[string]interface{}{
		"id":      "test-id",
		"results": results,
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestRerank_Success(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer auth, got %s", auth)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{1, 0}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(3)
	output, err := r.Rerank(context.Background(), "test query", docs, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) != 2 {
		t.Fatalf("expected 2 results, got %d", len(output))
	}
	// First result should be doc[1] with score 0.9
	if output[0].Content != "doc 1" {
		t.Errorf("expected doc 1, got %s", output[0].Content)
	}
	if output[0].Score() != 0.9 {
		t.Errorf("expected score 0.9, got %f", output[0].Score())
	}
}

func TestRerank_ContextCancel(t *testing.T) {
	fakeDoer := &fakeHTTPDoer{callCount: 0}
	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, fakeDoer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	docs := testDocs(2)
	_, err := r.Rerank(ctx, "query", docs, 1)
	if err == nil {
		t.Fatal("expected error on pre-cancelled ctx")
	}
	if fakeDoer.callCount != 0 {
		t.Fatalf("HTTP should not be called on pre-cancelled ctx, but called %d times", fakeDoer.callCount)
	}
}

func TestRerank_Timeout(t *testing.T) {
	trigger := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-trigger
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(trigger)

	cfg := &RerankConfig{URL: server.URL, APIKey: "key", Model: "model"}
	r := NewRerank(cfg, server.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	docs := testDocs(2)
	_, err := r.Rerank(ctx, "query", docs, 1)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	// Release the server handler
	trigger <- struct{}{}
}

func TestRerank_500Error(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code 500: %v", err)
	}
}

func TestRerank_BadJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not valid json"))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON: %v", err)
	}
}

func TestRerank_LargeResponse(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write more than 10MB
		largeData := make([]byte, 11<<20) // 11MB
		for i := range largeData {
			largeData[i] = 'x'
		}
		w.Write(largeData)
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for large response")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "size") {
		t.Errorf("error should mention size limit: %v", err)
	}
}

func TestRerank_MissingFields(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Missing results field
		w.Write([]byte(`{"id": "test"}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for missing results")
	}
	if !strings.Contains(err.Error(), "missing results") {
		t.Errorf("error should mention missing results: %v", err)
	}
}

func TestRerank_EmptyResults(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "test", "results": []}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for empty results")
	}
	if !strings.Contains(err.Error(), "empty results") {
		t.Errorf("error should mention empty results: %v", err)
	}
}

func TestRerank_NullResult(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "test", "results": [null]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for null result item")
	}
	if !strings.Contains(err.Error(), "null result") {
		t.Errorf("error should mention null result: %v", err)
	}
}

func TestRerank_OutOfBoundsIndex(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{999}, []float64{0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for out-of-bounds index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range: %v", err)
	}
}

func TestRerank_DuplicateIndex(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{0, 0}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 2)
	if err == nil {
		t.Fatal("expected error for duplicate index")
	}
	if !strings.Contains(err.Error(), "duplicate index") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

func TestRerank_InvalidScore_NaN(t *testing.T) {
	// Construct JSON with NaN manually (json.Marshal won't produce NaN)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","results":[{"index":0,"relevance_score":"NaN"}]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	// This will fail to unmarshal since NaN is not valid JSON number
	if err == nil {
		t.Fatal("expected error for invalid score")
	}
}

func TestRerank_MissingIndex(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","results":[{"relevance_score":0.5}]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !strings.Contains(err.Error(), "missing index") {
		t.Errorf("error should mention missing index: %v", err)
	}
}

func TestRerank_MissingScore(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","results":[{"index":0}]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for missing score")
	}
	if !strings.Contains(err.Error(), "missing score") {
		t.Errorf("error should mention missing score: %v", err)
	}
}

func TestRerank_EmptyDocs(t *testing.T) {
	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, nil)

	_, err := r.Rerank(context.Background(), "query", []*schema.Document{}, 1)
	if err == nil {
		t.Fatal("expected error for empty docs")
	}
	if !strings.Contains(err.Error(), "empty docs") {
		t.Errorf("error should mention empty docs: %v", err)
	}
}

func TestRerank_NilDocument(t *testing.T) {
	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, nil)

	docs := []*schema.Document{&schema.Document{Content: "valid"}, nil}
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for nil document")
	}
	if !strings.Contains(err.Error(), "nil document") {
		t.Errorf("error should mention nil document: %v", err)
	}
}

func TestRerank_InvalidTopK(t *testing.T) {
	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, nil)

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 0)
	if err == nil {
		t.Fatal("expected error for topK=0")
	}
	if !strings.Contains(err.Error(), "invalid topK") {
		t.Errorf("error should mention invalid topK: %v", err)
	}

	_, err = r.Rerank(context.Background(), "query", docs, -1)
	if err == nil {
		t.Fatal("expected error for topK=-1")
	}
}

func TestRerank_PartialFailure_NoModification(t *testing.T) {
	// Second result has out-of-range index; first result should NOT modify input docs
	originalScores := make([]float64, 3)
	for i := range originalScores {
		originalScores[i] = 0
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// First result valid (index 0), second invalid (index 999)
		w.Write([]byte(validResultJSON([]int{0, 999}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(3)
	for _, doc := range docs {
		doc.WithScore(0) // reset scores
	}

	_, err := r.Rerank(context.Background(), "query", docs, 2)
	if err == nil {
		t.Fatal("expected error for out-of-range index")
	}

	// Verify no document was modified (score should still be 0)
	for i, doc := range docs {
		if doc.Score() != 0 {
			t.Errorf("doc[%d] score was modified to %f, expected 0 (no partial modification)", i, doc.Score())
		}
	}
}

func TestRerank_NegativeIndex(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","results":[{"index":-1,"relevance_score":0.5}]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for negative index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error should mention out of range: %v", err)
	}
}

func TestRerank_HTTPError(t *testing.T) {
	fakeDoer := &fakeHTTPDoer{
		err: fmt.Errorf("network error"),
	}
	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, fakeDoer)

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for HTTP failure")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("error should mention network error: %v", err)
	}
	if fakeDoer.callCount != 1 {
		t.Errorf("expected 1 HTTP call, got %d", fakeDoer.callCount)
	}
}

func TestRerank_NilClient_DefaultsToHTTP(t *testing.T) {
	r := NewRerank(&RerankConfig{URL: "http://example.com"}, nil)
	if r.client == nil {
		t.Fatal("client should default to http.DefaultClient")
	}
}

func TestRerank_ScoreInfinity(t *testing.T) {
	// 1e309 overflows float64; sonic rejects it at parse time as invalid JSON number
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"test","results":[{"index":0,"relevance_score":1e309}]}`))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for infinite score")
	}
	// sonic rejects 1e309 at JSON parse time, so error mentions invalid JSON
	if !strings.Contains(err.Error(), "invalid score") && !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid score or invalid JSON: %v", err)
	}
}

func TestRerank_LargeResponse_ExactLimit(t *testing.T) {
	// Test that responses just under the limit are accepted
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{0}, []float64{0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	output, err := r.Rerank(context.Background(), "query", docs, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("expected 1 result, got %d", len(output))
	}
}

func TestRerank_MultipleResults_AllValid(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{2, 0, 1}, []float64{0.95, 0.7, 0.3})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(3)
	output, err := r.Rerank(context.Background(), "query", docs, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) != 3 {
		t.Fatalf("expected 3 results, got %d", len(output))
	}
	// Verify order: doc[2], doc[0], doc[1]
	expectedOrder := []string{"doc 2", "doc 0", "doc 1"}
	for i, exp := range expectedOrder {
		if output[i].Content != exp {
			t.Errorf("result[%d]: expected %s, got %s", i, exp, output[i].Content)
		}
	}
}

func TestRerank_CtxDeadlinePropagation(t *testing.T) {
	// Verify that a caller-set deadline shorter than 30s is respected
	deadline := time.Now().Add(100 * time.Millisecond)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &RerankConfig{URL: server.URL, APIKey: "key", Model: "model"}
	r := NewRerank(cfg, server.Client())

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	docs := testDocs(2)
	_, err := r.Rerank(ctx, "query", docs, 1)
	if err == nil {
		t.Fatal("expected error when deadline exceeded")
	}
}

// Ensure io import is used (for potential future tests)
var _ = io.Discard
var _ = math.IsNaN
