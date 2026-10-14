package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// fakeHTTPDoer is a mock HTTP client for testing.
type fakeHTTPDoer struct {
	response     *http.Response
	err          error
	callCount    int
	checkRequest func(req *http.Request)
}

func (f *fakeHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	f.callCount++
	if f.checkRequest != nil {
		f.checkRequest(req)
	}
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

// helper to create test documents with metadata
func testDocsWithMeta(n int) []*schema.Document {
	docs := make([]*schema.Document, n)
	for i := 0; i < n; i++ {
		docs[i] = &schema.Document{
			Content:  fmt.Sprintf("doc %d", i),
			MetaData: map[string]any{"key": fmt.Sprintf("val%d", i)},
		}
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
			"index":           idx,
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

func TestRerank_Success_InputUnmodified(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{1, 0}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocsWithMeta(3)
	originalScore := docs[0].Score()
	originalMeta := docs[0].MetaData["key"]

	output, err := r.Rerank(context.Background(), "query", docs, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) != 2 {
		t.Fatalf("expected 2 results, got %d", len(output))
	}

	// Input documents should remain unchanged
	if docs[0].Score() != originalScore {
		t.Errorf("input document score was modified: %f -> %f", originalScore, docs[0].Score())
	}
	if docs[0].MetaData["key"] != originalMeta {
		t.Errorf("input document metadata was modified: %v -> %v", originalMeta, docs[0].MetaData["key"])
	}
	// Output documents should have correct scores
	if output[0].Score() != 0.9 {
		t.Errorf("expected output score 0.9, got %f", output[0].Score())
	}
}

func TestRerank_Success_OutputCorrect(t *testing.T) {
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
	// Verify order and scores
	expectedOrder := []string{"doc 2", "doc 0", "doc 1"}
	expectedScores := []float64{0.95, 0.7, 0.3}
	for i := range expectedOrder {
		if output[i].Content != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], output[i].Content)
		}
		if output[i].Score() != expectedScores[i] {
			t.Errorf("result[%d]: expected score %f, got %f", i, expectedScores[i], output[i].Score())
		}
	}
}

func TestRerank_SharedInput_NoPollution(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{0, 1}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	// Shared input documents
	docs := testDocsWithMeta(2)

	// First rerank call
	output1, err := r.Rerank(context.Background(), "query1", docs, 2)
	if err != nil {
		t.Fatalf("first rerank failed: %v", err)
	}

	// Second rerank call with same input
	output2, err := r.Rerank(context.Background(), "query2", docs, 2)
	if err != nil {
		t.Fatalf("second rerank failed: %v", err)
	}

	// Input should remain unchanged
	if docs[0].Score() != 0 {
		t.Errorf("input doc[0] score was modified to %f", docs[0].Score())
	}
	if docs[0].MetaData["key"] != "val0" {
		t.Errorf("input doc[0] metadata was modified")
	}

	// Both outputs should be independent
	if output1[0].Score() != 0.9 {
		t.Errorf("output1[0] score wrong: %f", output1[0].Score())
	}
	if output2[0].Score() != 0.9 {
		t.Errorf("output2[0] score wrong: %f", output2[0].Score())
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

func TestRerank_InFlightCancel(t *testing.T) {
	requestStarted := make(chan struct{})
	proceed := make(chan struct{})
	helperDone := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-proceed:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			// request cancelled
		}
	}))
	defer server.Close()

	cfg := &RerankConfig{URL: server.URL, APIKey: "key", Model: "model"}
	r := NewRerank(cfg, server.Client())

	ctx, cancel := context.WithCancel(context.Background())

	// Helper goroutine: single owner closes proceed
	go func() {
		defer close(helperDone)
		<-requestStarted
		cancel()
		close(proceed)
	}()

	docs := testDocs(2)
	_, err := r.Rerank(ctx, "query", docs, 1)

	// Wait for helper goroutine to finish
	select {
	case <-helperDone:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("helper goroutine did not finish")
	}

	if err == nil {
		t.Fatal("expected error on in-flight cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}

	// Release the server handler
	trigger <- struct{}{}
}

func TestRerank_DeadlinePropagation(t *testing.T) {
	deadlineChecked := make(chan bool, 1)

	fakeDoer := &fakeHTTPDoer{
		checkRequest: func(req *http.Request) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				deadlineChecked <- false
				return
			}
			// Deadline should be roughly 10s from now (caller's deadline)
			remaining := time.Until(deadline)
			deadlineChecked <- remaining > 0 && remaining <= 11*time.Second
		},
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
		},
	}

	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, fakeDoer)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	docs := testDocs(2)
	_, _ = r.Rerank(ctx, "query", docs, 1)

	select {
	case ok := <-deadlineChecked:
		if !ok {
			t.Fatal("request should carry caller deadline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline check timed out")
	}
}

func TestRerank_DefaultTimeout(t *testing.T) {
	deadlineChecked := make(chan time.Duration, 1)

	fakeDoer := &fakeHTTPDoer{
		checkRequest: func(req *http.Request) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				deadlineChecked <- 0
				return
			}
			deadlineChecked <- time.Until(deadline)
		},
		response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
		},
	}

	cfg := &RerankConfig{URL: "http://unused", APIKey: "key", Model: "model"}
	r := NewRerank(cfg, fakeDoer)

	// No caller deadline -> default 30s timeout
	docs := testDocs(2)
	_, _ = r.Rerank(context.Background(), "query", docs, 1)

	select {
	case remaining := <-deadlineChecked:
		if remaining < 29*time.Second || remaining > 31*time.Second {
			t.Fatalf("expected ~30s default deadline, got: %v", remaining)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline check timed out")
	}
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
		// Write more than 10 MiB
		largeData := make([]byte, 11<<20) // 11 MiB
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
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit: %v", err)
	}
}

func TestRerank_ResponseSize_LimitMinus1(t *testing.T) {
	validJSON := validResultJSON([]int{0}, []float64{0.5})
	padding := strings.Repeat(" ", (10<<20)-1-len(validJSON))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validJSON + padding))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	output, err := r.Rerank(context.Background(), "query", docs, 1)
	if err != nil {
		t.Fatalf("expected success for limit-1 size, got error: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("expected 1 result, got %d", len(output))
	}
}

func TestRerank_ResponseSize_ExactLimit(t *testing.T) {
	validJSON := validResultJSON([]int{0}, []float64{0.5})
	padding := strings.Repeat(" ", (10<<20)-len(validJSON))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validJSON + padding))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	output, err := r.Rerank(context.Background(), "query", docs, 1)
	if err != nil {
		t.Fatalf("expected success for exact limit size, got error: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("expected 1 result, got %d", len(output))
	}
}

func TestRerank_ResponseSize_LimitPlus1(t *testing.T) {
	validJSON := validResultJSON([]int{0}, []float64{0.5})
	padding := strings.Repeat(" ", (10<<20)+1-len(validJSON))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validJSON + padding))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for limit+1 size")
	}
	if !strings.Contains(err.Error(), "exceeds") {
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

func TestRerank_ExcessResults(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Request topK=1 but server returns 2 results
		w.Write([]byte(validResultJSON([]int{0, 1}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	_, err := r.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for excess results")
	}
	if !strings.Contains(err.Error(), "expected at most") {
		t.Errorf("error should mention excess results: %v", err)
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// First result valid (index 0), second invalid (index 999)
		w.Write([]byte(validResultJSON([]int{0, 999}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocsWithMeta(3)
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
	// Verify metadata unchanged
	for i, doc := range docs {
		expected := fmt.Sprintf("val%d", i)
		if doc.MetaData["key"] != expected {
			t.Errorf("doc[%d] metadata was modified: got %v, expected %v", i, doc.MetaData["key"], expected)
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

func TestRerank_TopK_GreaterThanDocs(t *testing.T) {
	// topK > len(docs): should return all documents (sorted by score)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Server returns 2 results even though topK=5
		w.Write([]byte(validResultJSON([]int{1, 0}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocs(2)
	output, err := r.Rerank(context.Background(), "query", docs, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output) != 2 {
		t.Fatalf("expected 2 results (all docs), got %d", len(output))
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

func TestRerank_OutputModification_NoInputOrOtherOutputChange(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validResultJSON([]int{0, 1}, []float64{0.9, 0.5})))
	})

	r, server := testRerank(handler)
	defer server.Close()

	docs := testDocsWithMeta(3)

	// First call
	output1, err := r.Rerank(context.Background(), "query", docs, 2)
	if err != nil {
		t.Fatalf("first rerank failed: %v", err)
	}

	// Modify output1 top-level metadata
	output1[0].MetaData["newKey"] = "newValue"
	output1[0].WithScore(999.0)

	// Second call with same input
	output2, err := r.Rerank(context.Background(), "query", docs, 2)
	if err != nil {
		t.Fatalf("second rerank failed: %v", err)
	}

	// Input should not be modified
	if _, ok := docs[0].MetaData["newKey"]; ok {
		t.Fatal("input document metadata should not be modified by output changes")
	}
	if docs[0].Score() != 0 {
		t.Fatalf("input document score should remain 0, got %f", docs[0].Score())
	}

	// output2 should not be affected by output1 modifications
	if _, ok := output2[0].MetaData["newKey"]; ok {
		t.Fatal("second output should not contain modifications from first output")
	}
	if output2[0].Score() == 999.0 {
		t.Fatal("second output score should not be affected by first output modification")
	}
}

// fakeConfigReader is a test double for ConfigReader.
type fakeConfigReader struct {
	values    map[string]string
	errs      map[string]error
	getString func(ctx context.Context, key string) (string, error)
}

func (r *fakeConfigReader) GetString(ctx context.Context, key string) (string, error) {
	if r.getString != nil {
		return r.getString(ctx, key)
	}
	if err, ok := r.errs[key]; ok {
		return "", err
	}
	return r.values[key], nil
}

// sentinel errors for config read error tests
var (
	errBaseURL = errors.New("baseURL error")
	errModel   = errors.New("model error")
	errAPIKey  = errors.New("apiKey error")
)

func TestLoadConf_BaseURLReadError(t *testing.T) {
	reader := &fakeConfigReader{
		errs: map[string]error{"rerank.baseURL": errBaseURL},
	}
	cfg, err := loadConf(context.Background(), reader)
	if err == nil {
		t.Fatal("expected error")
	}
	if cfg != nil {
		t.Fatal("should not return config on error")
	}
	if !errors.Is(err, errBaseURL) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", err)
	}
}

func TestLoadConf_ModelReadError(t *testing.T) {
	reader := &fakeConfigReader{
		values: map[string]string{"rerank.baseURL": "http://example.com"},
		errs:   map[string]error{"rerank.model": errModel},
	}
	cfg, err := loadConf(context.Background(), reader)
	if err == nil {
		t.Fatal("expected error")
	}
	if cfg != nil {
		t.Fatal("should not return config on error")
	}
	if !errors.Is(err, errModel) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", err)
	}
}

func TestLoadConf_ApiKeyReadError(t *testing.T) {
	reader := &fakeConfigReader{
		values: map[string]string{
			"rerank.baseURL": "http://example.com",
			"rerank.model":   "model-name",
		},
		errs: map[string]error{"rerank.apiKey": errAPIKey},
	}
	cfg, err := loadConf(context.Background(), reader)
	if err == nil {
		t.Fatal("expected error")
	}
	if cfg != nil {
		t.Fatal("should not return config on error")
	}
	if !errors.Is(err, errAPIKey) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", err)
	}
}

func TestLoadConf_RetryAfterFailure(t *testing.T) {
	callCount := 0
	reader := &fakeConfigReader{
		getString: func(ctx context.Context, key string) (string, error) {
			callCount++
			if callCount <= 3 {
				return "", errors.New("temporary error")
			}
			switch key {
			case "rerank.baseURL":
				return "http://example.com", nil
			case "rerank.model":
				return "model-name", nil
			case "rerank.apiKey":
				return "api-key", nil
			default:
				return "", nil
			}
		},
	}

	// First call fails (baseURL read fails)
	_, err := loadConf(context.Background(), reader)
	if err == nil {
		t.Fatal("expected error on first call")
	}

	// Reset counter so next 3 calls succeed
	callCount = 3

	// Second call succeeds
	cfg, err := loadConf(context.Background(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("should return config")
	}
	if cfg.url != "http://example.com/rerank" {
		t.Errorf("expected url http://example.com/rerank, got %s", cfg.url)
	}
	if cfg.Model != "model-name" {
		t.Errorf("expected model model-name, got %s", cfg.Model)
	}
	if cfg.apiKey != "api-key" {
		t.Errorf("expected apiKey api-key, got %s", cfg.apiKey)
	}
}

func TestLoadConf_Success(t *testing.T) {
	reader := &fakeConfigReader{
		values: map[string]string{
			"rerank.baseURL": "http://example.com",
			"rerank.model":   "model-name",
			"rerank.apiKey":  "api-key",
		},
	}
	cfg, err := loadConf(context.Background(), reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("should return config")
	}
	if cfg.url != "http://example.com/rerank" {
		t.Errorf("baseURL mismatch: got %s", cfg.url)
	}
	if cfg.Model != "model-name" {
		t.Errorf("model mismatch: got %s", cfg.Model)
	}
	if cfg.apiKey != "api-key" {
		t.Errorf("apiKey mismatch: got %s", cfg.apiKey)
	}
	// Verify defaults
	if cfg.ReturnDocuments != false {
		t.Errorf("ReturnDocuments should default to false")
	}
	if cfg.MaxChunksPerDoc != 1024 {
		t.Errorf("MaxChunksPerDoc should default to 1024, got %d", cfg.MaxChunksPerDoc)
	}
	if cfg.OverlapTokens != 80 {
		t.Errorf("OverlapTokens should default to 80, got %d", cfg.OverlapTokens)
	}
}

func TestLoadConf_ConcurrentCalls(t *testing.T) {
	reader := &fakeConfigReader{
		values: map[string]string{
			"rerank.baseURL": "http://example.com",
			"rerank.model":   "model-name",
			"rerank.apiKey":  "api-key",
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg, err := loadConf(context.Background(), reader)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Error("should return config")
			}
		}()
	}
	wg.Wait()
}
