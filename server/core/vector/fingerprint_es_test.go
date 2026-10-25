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
	exists     bool
	meta       map[string]any            // _meta field from mapping
	uuid       string                    // index UUID
	properties map[string]map[string]any // mapping properties (for schema validation C4)
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
	mappings := map[string]any{
		"_meta": meta,
	}
	// C4: Include mapping properties for schema validation
	if state.properties != nil {
		mappings["properties"] = state.properties
	}
	resp := map[string]any{
		indexName: map[string]any{
			"mappings": mappings,
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

	// Parse request body to capture mapping (for C2 atomic _meta and C4 schema)
	var reqBody map[string]any
	if r.Body != nil {
		bodyBytes, _ := io.ReadAll(r.Body)
		if len(bodyBytes) > 0 {
			json.Unmarshal(bodyBytes, &reqBody)
		}
	}

	f.mu.Lock()
	if _, exists := f.indices[indexName]; exists {
		f.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		// C3: Use proper ES error format with resource_already_exists_exception
		fmt.Fprintf(w, `{"error":{"type":"resource_already_exists_exception","reason":"index [%s] already exists"},"status":400}`, indexName)
		return
	}
	state := &fakeESIndexState{
		exists: true,
		uuid:   "test-uuid-" + indexName,
		meta:   map[string]any{},
	}
	// C2: Extract _meta from create request mapping
	if reqBody != nil {
		if mappings, ok := reqBody["mappings"].(map[string]any); ok {
			if meta, ok := mappings["_meta"].(map[string]any); ok {
				state.meta = meta
			}
			// C4: Store mapping properties for schema validation
			if props, ok := mappings["properties"].(map[string]any); ok {
				converted := make(map[string]map[string]any)
				for k, v := range props {
					if fieldMap, isMap := v.(map[string]any); isMap {
						converted[k] = fieldMap
					}
				}
				state.properties = converted
			}
		}
	}
	f.indices[indexName] = state
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"acknowledged":true,"shards_acknowledped":true,"index":"%s"}`, indexName)
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
// defaultTestProperties returns mapping properties matching testFingerprint()
// (1024 dims, cosine) for strict schema validation (D3).
func defaultTestProperties() map[string]map[string]any {
	return map[string]map[string]any{
		"content_vector":     {"type": "dense_vector", "dims": json.Number("1024"), "index": true, "similarity": "cosine"},
		"qa_content_vector": {"type": "dense_vector", "dims": json.Number("1024"), "index": true, "similarity": "cosine"},
	}
}

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
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "resource-1", meta: map[string]any{}, properties: defaultTestProperties()}
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

	// Set up index with fingerprint in _meta — resourceID comes from uuid (D2)
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists:     true,
		uuid:       "uuid-abc-123",
		properties: defaultTestProperties(),
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
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

	// Create index — uuid must match resourceID for D2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "round-trip-uuid", meta: map[string]any{}, properties: defaultTestProperties()}
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

// --- D2: resourceID comes from settings.index.uuid, not _meta ---

func TestES_ReadFingerprint_ResourceID_FromUUID(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// D2: resourceID comes from settings.index.uuid, regardless of _meta go-rag-resource-id
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists:     true,
		uuid:       "actual-uuid-123",
		properties: defaultTestProperties(),
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			// go-rag-resource-id in _meta is ignored by D2
			"go-rag-resource-id": "different-uuid-456",
		},
	}
	fake.mu.Unlock()

	// D2: resourceID should be the UUID from settings, not from _meta
	readFP, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint should succeed: %v", err)
	}
	if resourceID != "actual-uuid-123" {
		t.Errorf("resourceID = %q, want %q (from settings.index.uuid)", resourceID, "actual-uuid-123")
	}
	if !fp.Equal(readFP) {
		t.Errorf("fingerprint mismatch: got %+v, want %+v", readFP, fp)
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
		exists:     true,
		uuid:       "same-uuid-123",
		properties: defaultTestProperties(),
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
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

	// Create index — uuid must match resourceID for D2
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "res-1", meta: map[string]any{}, properties: defaultTestProperties()}
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

// --- C2: ES atomic creation with fingerprint in _meta ---

func TestES_CreateIndex_WithFingerprint_AtomicallyWritesMeta(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()

	// CreateIndex with fingerprint should atomically write _meta
	err := store.CreateIndex(ctx, "test-index", fp)
	if err != nil {
		t.Fatalf("CreateIndex with fingerprint should succeed, got: %v", err)
	}

	// Verify the index was created with fingerprint in _meta
	fake.mu.Lock()
	state, ok := fake.indices["test-index"]
	fake.mu.Unlock()
	if !ok {
		t.Fatal("index should have been created")
	}
	// D2: After creation, no PutMapping is done — only fingerprint in _meta from atomic create
	if _, hasFP := state.meta["go-rag-fingerprint"]; !hasFP {
		t.Error("create request should have included fingerprint in _meta")
	}
	// D2: go-rag-resource-id is NOT written by CreateIndex (no PutMapping supplementation)
	// resourceID comes from settings.index.uuid at read time
}

// --- C2: WriteFingerprint with empty resourceID → error ---

func TestES_WriteFingerprint_EmptyResourceID_Rejected(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	// Create the index in fake server
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{exists: true, uuid: "res-1", meta: map[string]any{}}
	fake.mu.Unlock()

	fp := testFingerprint()

	// C2: WriteFingerprint with empty resourceID should be rejected
	err := store.WriteFingerprint(ctx, "test-index", fp, "")
	if err == nil {
		t.Fatal("WriteFingerprint with empty resourceID should fail")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted for empty resourceID, got: %v", err)
	}
}

// --- C3: ES concurrent creation — one succeeds, one gets ErrIndexAlreadyExists ---

func TestES_ConcurrentCreateIndex_OneSucceeds(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()

	var wg sync.WaitGroup
	errs := make([]error, 2)

	// Two goroutines try to create the same index concurrently
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = store.CreateIndex(ctx, "concurrent-index", fp)
		}(i)
	}
	wg.Wait()

	// Count successes and ErrIndexAlreadyExists
	successes := 0
	alreadyExists := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrIndexAlreadyExists) {
			alreadyExists++
		}
	}
	if successes != 1 {
		t.Errorf("exactly 1 CreateIndex should succeed, got %d successes", successes)
	}
	if alreadyExists != 1 {
		t.Errorf("exactly 1 CreateIndex should get ErrIndexAlreadyExists, got %d", alreadyExists)
	}
}

// --- C4: ES schema validation — dimension mismatch ---

func TestES_ReadFingerprint_SchemaDimensionMismatch(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint() // 1024 dimensions
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// Set up index with fingerprint declaring 1024 dims, but mapping has 512 dims
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists: true,
		uuid:   "res-1",
		meta: map[string]any{
			"go-rag-fingerprint": fpMap,
			"go-rag-resource-id": "res-1",
		},
		properties: map[string]map[string]any{
			"content_vector":     {"type": "dense_vector", "dims": float64(512)}, // Mismatch!
			"qa_content_vector": {"type": "dense_vector", "dims": float64(512)},  // Mismatch!
		},
	}
	fake.mu.Unlock()

	// C4: ReadFingerprint should detect dimension mismatch
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail when mapping dims don't match fingerprint dims")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestES_CreateIndex_SchemaDimensionMismatch_Fails(t *testing.T) {
	// C4: CreateIndex with fingerprint declaring 1024 dims, but ES mapping returns 512
	// We need a custom fake server that returns wrong dimensions in GET
	fake := newFakeEServer()

	// Override the handleGetIndex to return mismatched dimensions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/_mapping") {
			// Return index with mismatched dimensions (512 instead of 1024)
			indexName := strings.TrimPrefix(r.URL.Path, "/")
			fake.mu.Lock()
			state, ok := fake.indices[indexName]
			fake.mu.Unlock()
			if !ok || !state.exists {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprintf(w, `{"error":"not found"}`)
				return
			}
			resp := map[string]any{
				indexName: map[string]any{
					"mappings": map[string]any{
						"_meta": state.meta,
						"properties": map[string]map[string]any{
							"content_vector":     {"type": "dense_vector", "dims": float64(512)},
							"qa_content_vector": {"type": "dense_vector", "dims": float64(512)},
						},
					},
					"settings": map[string]any{
						"index": map[string]any{"uuid": state.uuid},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		// Delegate to fake for other requests
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}
	ctx := context.Background()

	fp := testFingerprint() // 1024 dimensions

	// CreateIndex should fail because GET returns 512 dims
	err = store.CreateIndex(ctx, "test-index", fp)
	if err == nil {
		t.Fatal("CreateIndex should fail when schema dimensions don't match fingerprint")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

// --- D2: ES safety tests ---

// TestES_CreateIndex_NoDeleteOnFailure verifies D2: CreateIndex does NOT delete
// the index when post-creation verification fails (no deleteIndexBestEffort).
func TestES_CreateIndex_NoDeleteOnFailure(t *testing.T) {
	fake := newFakeEServer()
	var deleteCalled bool
	// Use a custom fake server that returns wrong dimensions in GET response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method == http.MethodDelete {
			deleteCalled = true
		}
		// Override GET to return wrong dimensions (512 instead of 1024)
		if r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, "/_mapping") {
			fake.mu.Lock()
			state, ok := fake.indices[strings.TrimPrefix(r.URL.Path, "/")]
			fake.mu.Unlock()
			if ok && state.exists {
				meta := state.meta
				if meta == nil {
					meta = map[string]any{}
				}
				indexName := strings.TrimPrefix(r.URL.Path, "/")
				resp := map[string]any{
					indexName: map[string]any{
						"mappings": map[string]any{
							"_meta": meta,
							"properties": map[string]map[string]any{
								"content_vector":     {"type": "dense_vector", "dims": float64(512)},
								"qa_content_vector": {"type": "dense_vector", "dims": float64(512)},
							},
						},
						"settings": map[string]any{
							"index": map[string]any{"uuid": state.uuid},
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}
	ctx := context.Background()

	fp := testFingerprint() // 1024 dims

	// CreateIndex should fail (GET returns 512 dims, fp declares 1024)
	err = store.CreateIndex(ctx, "test-index", fp)
	if err == nil {
		t.Fatal("CreateIndex should fail on schema mismatch")
	}

	// D2: Index should NOT be deleted after failure
	if deleteCalled {
		t.Fatal("D2: CreateIndex must NOT call DELETE on failure (no deleteIndexBestEffort)")
	}

	// Index should still exist in the fake server
	fake.mu.Lock()
	state, exists := fake.indices["test-index"]
	fake.mu.Unlock()
	if !exists || !state.exists {
		t.Fatal("D2: index should still exist after failed CreateIndex (not deleted)")
	}
}

// TestES_CreateIndex_NoPutMappingAfterCreation verifies D2: CreateIndex does NOT
// call PutMapping after creation (UUID is read-only from settings.index.uuid).
func TestES_CreateIndex_NoPutMappingAfterCreation(t *testing.T) {
	fake := newFakeEServer()
	var putMappingCalledAfterCreate bool
	var createDone bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/_mapping") && createDone {
			putMappingCalledAfterCreate = true
		}
		fake.ServeHTTP(w, r)
		if r.Method == http.MethodPut && !strings.HasSuffix(r.URL.Path, "/_mapping") {
			createDone = true
		}
	}))
	defer server.Close()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("create es client: %v", err)
	}
	store := &ESVectorStore{client: client, dimensions: 1024}
	ctx := context.Background()

	fp := testFingerprint()
	err = store.CreateIndex(ctx, "test-index", fp)
	if err != nil {
		t.Fatalf("CreateIndex should succeed: %v", err)
	}

	// D2: No PutMapping should be called after creation
	if putMappingCalledAfterCreate {
		t.Fatal("D2: CreateIndex must NOT call PutMapping after creation")
	}
}

// TestES_ReadFingerprint_UUID_AsResourceID verifies D2: ReadFingerprint reads
// resourceID from settings.index.uuid, NOT from _meta go-rag-resource-id.
func TestES_ReadFingerprint_UUID_AsResourceID(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()
	fpJSON, _ := fp.MarshalJSON()
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)

	// Set up index where _meta has a different resource-id than the UUID
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists:     true,
		uuid:       "physical-uuid-from-settings",
		properties: defaultTestProperties(),
		meta: map[string]any{
			"go-rag-fingerprint":   fpMap,
			"go-rag-resource-id": "old-meta-resource-id", // D2: this is IGNORED
		},
	}
	fake.mu.Unlock()

	_, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint: %v", err)
	}
	// D2: resourceID must come from settings.index.uuid
	if resourceID != "physical-uuid-from-settings" {
		t.Errorf("D2: resourceID = %q, want %q (from settings.index.uuid)",
			resourceID, "physical-uuid-from-settings")
	}
}

// TestES_CreateIndex_ResourceNotOverwritten verifies D2: after CreateIndex,
// a replacement resource with the same name is NOT overwritten.
func TestES_CreateIndex_ResourceNotOverwritten(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	ctx := context.Background()

	fp := testFingerprint()

	// First creation succeeds
	err := store.CreateIndex(ctx, "test-index", fp)
	if err != nil {
		t.Fatalf("first CreateIndex should succeed: %v", err)
	}

	// Simulate resource replacement: delete and recreate with different UUID
	fake.mu.Lock()
	fake.indices["test-index"] = &fakeESIndexState{
		exists:     true,
		uuid:       "replacement-uuid",
		meta:       map[string]any{},
		properties: defaultTestProperties(),
	}
	fake.mu.Unlock()

	// Second CreateIndex should get ErrIndexAlreadyExists
	err = store.CreateIndex(ctx, "test-index", fp)
	if !errors.Is(err, ErrIndexAlreadyExists) {
		t.Errorf("second CreateIndex should get ErrIndexAlreadyExists, got: %v", err)
	}

	// D2: The replacement resource should NOT be overwritten
	fake.mu.Lock()
	state := fake.indices["test-index"]
	uuid := state.uuid
	fake.mu.Unlock()
	if uuid != "replacement-uuid" {
		t.Errorf("D2: replacement UUID should not be overwritten, got %q", uuid)
	}
}
