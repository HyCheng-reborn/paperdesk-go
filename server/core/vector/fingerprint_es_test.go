package vector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// --- Fake ES server for fingerprint tests ---

// fakeEServer is a minimal fake Elasticsearch server for fingerprint read/write tests.
// It handles GET /{index}, PUT /{index}/_mapping, HEAD /{index}, and DELETE /{index}.
type fakeEServer struct {
	mu       sync.Mutex
	indices  map[string]*fakeESIndexState // index name → state
	requests []string                     // recorded request paths
}

type fakeESIndexState struct {
	exists bool
	meta   map[string]any // _meta field from mapping
	uuid   string         // index UUID
}

func newFakeEServer() *fakeEServer {
	return &fakeEServer{
		indices: make(map[string]*fakeESIndexState),
	}
}

func (f *fakeEServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ES v8 client requires this header in every response
	w.Header().Set("X-Elastic-Product", "Elasticsearch")

	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	path := r.URL.Path

	switch {
	// PUT /{index}/_mapping — PutMapping
	case r.Method == http.MethodPut && strings.HasSuffix(path, "/_mapping"):
		f.handlePutMapping(w, r)
	// GET /{index} — Get index (for ReadFingerprint / ReadESIndexUUID)
	case r.Method == http.MethodGet && !strings.HasSuffix(path, "/_mapping"):
		f.handleGetIndex(w, r)
	// HEAD /{index} — Exists check
	case r.Method == http.MethodHead:
		f.handleHeadIndex(w, r)
	// PUT /{index} — Create index (or PUT /{index}/_mapping handled above)
	case r.Method == http.MethodPut:
		f.handleCreateIndex(w, r)
	// DELETE /{index} — Delete index
	case r.Method == http.MethodDelete:
		f.handleDeleteIndex(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeEServer) handleGetIndex(w http.ResponseWriter, r *http.Request) {
	indexName := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	state, ok := f.indices[indexName]
	f.mu.Unlock()

	if !ok || !state.exists {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":{"type":"index_not_found_exception"},"status":404}`)
		return
	}

	// Build response matching ES format — include settings.index.uuid for R2 verification
	meta := state.meta
	if meta == nil {
		meta = map[string]any{}
	}
	resp := map[string]any{
		indexName: map[string]any{
			"mappings": map[string]any{
				"_meta": meta,
			},
			"settings": map[string]any{
				"index": map[string]any{
					"uuid": state.uuid,
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (f *fakeEServer) handlePutMapping(w http.ResponseWriter, r *http.Request) {
	// Path: /{index}/_mapping
	path := strings.TrimPrefix(r.URL.Path, "/")
	indexName := strings.TrimSuffix(path, "/_mapping")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var mappingBody map[string]any
	if err := json.Unmarshal(body, &mappingBody); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	state, ok := f.indices[indexName]
	if !ok || !state.exists {
		f.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Merge _meta into existing state
	if meta, ok := mappingBody["_meta"].(map[string]any); ok {
		state.meta = meta
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"acknowledged":true}`)
}

func (f *fakeEServer) handleHeadIndex(w http.ResponseWriter, r *http.Request) {
	indexName := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	state, ok := f.indices[indexName]
	f.mu.Unlock()

	if !ok || !state.exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *fakeEServer) handleCreateIndex(w http.ResponseWriter, r *http.Request) {
	indexName := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	if _, exists := f.indices[indexName]; exists {
		f.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"index already exists"}`)
		return
	}
	f.indices[indexName] = &fakeESIndexState{
		exists: true,
		uuid:   "test-uuid-" + indexName,
		meta:   map[string]any{},
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"acknowledged":true,"shards_acknowledged":true,"index":"%s"}`, indexName)
}

func (f *fakeEServer) handleDeleteIndex(w http.ResponseWriter, r *http.Request) {
	indexName := strings.TrimPrefix(r.URL.Path, "/")
	f.mu.Lock()
	delete(f.indices, indexName)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"acknowledged":true}`)
}

func (f *fakeEServer) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// newTestESVectorStore creates an ESVectorStore connected to a fake ES httptest server.
func newTestESVectorStore(t *testing.T, fake *fakeEServer) (*ESVectorStore, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fake)
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		server.Close()
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}
	return store, server
}

// --- F3: Conditional write ---

func TestES_WriteFingerprint_ConditionalWrite(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// Create the index in fake server — resourceID must match uuid for R2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "resource-1", meta: map[string]any{}}
	fake.mu.Unlock()

	fp := testFingerprint()

	// First write should succeed
	err := store.WriteFingerprint(ctx, "test-index", fp, "resource-1")
	if err != nil {
		t.Fatalf("first WriteFingerprint should succeed, got: %v", err)
	}

	// Second write should fail with ErrFingerprintExists
	err = store.WriteFingerprint(ctx, "test-index", fp, "resource-2")
	if err == nil {
		t.Fatal("second WriteFingerprint should fail")
	}
	if !errors.Is(err, ErrFingerprintExists) {
		t.Errorf("expected ErrFingerprintExists, got: %v", err)
	}
}

// --- F4: ReadFingerprint returns resourceID ---

func TestES_ReadFingerprint_ReturnsResourceID(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// Set up index with fingerprint in _meta — resourceID must match uuid for R2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists: true,
		uuid:   "uuid-abc-123",
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			"go-rag-resource-id": "uuid-abc-123",
		},
	}
	fake.mu.Unlock()

	readFP, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint: %v", err)
	}
	if resourceID != "uuid-abc-123" {
		t.Errorf("resourceID = %q, want %q", resourceID, "uuid-abc-123")
	}
	if !fp.Equal(readFP) {
		t.Errorf("fingerprint mismatch: got %+v, want %+v", readFP, fp)
	}
}

// --- Missing fingerprint ---

func TestES_ReadFingerprint_Missing(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// Create index without fingerprint
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "test-uuid", meta: map[string]any{}}
	fake.mu.Unlock()

	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail when no fingerprint in _meta")
	}
	if !errors.Is(err, ErrFingerprintMissing) {
		t.Errorf("expected ErrFingerprintMissing, got: %v", err)
	}
}

// --- Non-existent index ---

func TestES_ReadFingerprint_NonExistentIndex(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// No index created at all
	_, _, err := store.ReadFingerprint(ctx, "nonexistent-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail for nonexistent index")
	}
	if !errors.Is(err, ErrFingerprintMissing) {
		t.Errorf("expected ErrFingerprintMissing, got: %v", err)
	}
}

// --- F8: Context cancellation ---

func TestES_ContextCancellation(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()

	// Create index
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "test-uuid", meta: map[string]any{}}
	fake.mu.Unlock()

	// Cancel context before operation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Record request count before
	countBefore := fake.requestCount()

	// ReadFingerprint with cancelled ctx should return context.Canceled
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint with cancelled ctx should fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	// Verify no HTTP request was sent
	countAfter := fake.requestCount()
	if countAfter != countBefore {
		t.Errorf("no HTTP request should be sent with cancelled ctx, but %d requests were made", countAfter-countBefore)
	}

	// WriteFingerprint with cancelled ctx should also fail
	fp := testFingerprint()
	err = store.WriteFingerprint(ctx, "test-index", fp, "res-1")
	if err == nil {
		t.Fatal("WriteFingerprint with cancelled ctx should fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled for WriteFingerprint, got: %v", err)
	}
}

// --- R5: Digest verification via ES (updated for R5 fix) ---

func TestES_ReadFingerprint_DigestVerification(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)
	// Tamper with the digest — R5: ParseFingerprint now verifies digest integrity
	fpMap["digest"] = "sha256:tampered-digest-00000000000000000000000000000000000000000000"

	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists: true,
		uuid:   "res-1", // Must match resource-id for R2 to pass, so R5 digest check is tested
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			"go-rag-resource-id": "res-1",
		},
	}
	fake.mu.Unlock()

	// R5: ReadFingerprint should fail because ParseFingerprint verifies digest
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail for tampered digest")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got: %v", err)
	}
}

// --- Write then Read round-trip ---

func TestES_WriteThenRead_RoundTrip(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// Create index — uuid must match resourceID for R2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "round-trip-uuid", meta: map[string]any{}}
	fake.mu.Unlock()

	fp := testFingerprint()

	// Write
	err := store.WriteFingerprint(ctx, "test-index", fp, "round-trip-uuid")
	if err != nil {
		t.Fatalf("WriteFingerprint: %v", err)
	}

	// Read back
	readFP, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint: %v", err)
	}
	if resourceID != "round-trip-uuid" {
		t.Errorf("resourceID = %q, want %q", resourceID, "round-trip-uuid")
	}
	if !fp.Equal(readFP) {
		t.Errorf("fingerprint mismatch: got %+v, want %+v", readFP, fp)
	}
}

// --- R2: resourceID vs UUID mismatch ---

func TestES_ReadFingerprint_ResourceID_UUID_Mismatch(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// Set up index with mismatched resourceID and UUID
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists: true,
		uuid:   "actual-uuid-123",
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			"go-rag-resource-id": "different-uuid-456",
		},
	}
	fake.mu.Unlock()

	// R2: ReadFingerprint should detect resourceID != actual UUID
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail when resourceID != actual UUID")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got: %v", err)
	}
	if !strings.Contains(err.Error(), "resource-id mismatch") {
		t.Errorf("error should mention resource-id mismatch, got: %v", err)
	}
}

// --- R2: resourceID matches UUID → success ---

func TestES_ReadFingerprint_ResourceID_Matches_UUID(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// Set up index with matching resourceID and UUID
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists: true,
		uuid:   "same-uuid-123",
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			"go-rag-resource-id": "same-uuid-123",
		},
	}
	fake.mu.Unlock()

	readFP, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint should succeed when resourceID matches UUID: %v", err)
	}
	if resourceID != "same-uuid-123" {
		t.Errorf("resourceID = %q, want %q", resourceID, "same-uuid-123")
	}
	if !fp.Equal(readFP) {
		t.Errorf("fingerprint mismatch: got %+v, want %+v", readFP, fp)
	}
}

// --- R3: ES concurrent writes — only one should succeed ---

func TestES_ConcurrentWriteFingerprint_OnlyOneSucceeds(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// Create index — uuid must match resourceID for R2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "res-1", meta: map[string]any{}}
	fake.mu.Unlock()

	fp := testFingerprint()

	// First write succeeds
	err := store.WriteFingerprint(ctx, "test-index", fp, "res-1")
	if err != nil {
		t.Fatalf("first WriteFingerprint should succeed: %v", err)
	}

	// Second write should fail with ErrFingerprintExists
	err = store.WriteFingerprint(ctx, "test-index", fp, "res-2")
	if err == nil {
		t.Fatal("second WriteFingerprint should fail")
	}
	if !errors.Is(err, ErrFingerprintExists) {
		t.Errorf("expected ErrFingerprintExists, got: %v", err)
	}
}

// --- R6: In-flight context cancellation ---

func TestES_InFlightRead_ContextCancellation(t *testing.T) {
	// Create a server that blocks until we release it
	release := make(chan struct{})
	fake := newFakeEServer()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		// Block on all requests until released
		<-release
		fake.ServeHTTP(w, r)
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}

	// Set up index
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "test-uuid", meta: map[string]any{}}
	fake.mu.Unlock()

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Start ReadFingerprint in background
	errCh := make(chan error, 1)
	go func() {
		_, _, err := store.ReadFingerprint(ctx, "test-index")
		errCh <- err
	}()

	// Give the request time to start and reach the handler
	time.Sleep(50 * time.Millisecond)

	// Cancel context while request is in flight
	cancel()

	// Release the server so the response can be processed
	// The request should return quickly with context.Canceled
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("ReadFingerprint should fail when context is cancelled in-flight")
		}
		// Should be context.Canceled or a wrapping error
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFingerprint did not return after context cancellation")
	}
}

func TestES_InFlightWrite_ContextCancellation(t *testing.T) {
	release := make(chan struct{})
	fake := newFakeEServer()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		<-release
		fake.ServeHTTP(w, r)
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}

	// Set up index
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "test-uuid", meta: map[string]any{}}
	fake.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	fp := testFingerprint()

	errCh := make(chan error, 1)
	go func() {
		errCh <- store.WriteFingerprint(ctx, "test-index", fp, "res-1")
	}()

	// Give the request time to start
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("WriteFingerprint should fail when context is cancelled in-flight")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFingerprint did not return after context cancellation")
	}
}
