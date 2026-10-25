package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// fakeVectorStore implements vector.VectorStore for testing fingerprint gates.
type fakeVectorStore struct {
	mu          sync.Mutex
	fingerprint *vector.Fingerprint
	resourceID  string
	readErr     error
	writeErr    error
	indexExists bool
	writeCalls  int
	readCalls   int // C5: track storage reads for no-cache verification
}

func (f *fakeVectorStore) CreateIndex(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	return nil
}

func (f *fakeVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	return f.indexExists, nil
}

func (f *fakeVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	return nil
}

func (f *fakeVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	return nil, nil
}

func (f *fakeVectorStore) SearchDocuments(ctx context.Context, req *vector.SearchRequest) (*vector.SearchResponse, error) {
	return &vector.SearchResponse{}, nil
}

func (f *fakeVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint, resourceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	if f.writeErr != nil {
		return f.writeErr
	}
	// F3: Conditional write — refuse if already exists
	if f.fingerprint != nil {
		return fmt.Errorf("index %q: %w", indexName, vector.ErrFingerprintExists)
	}
	f.fingerprint = fp
	f.resourceID = resourceID
	return nil
}

func (f *fakeVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*vector.Fingerprint, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCalls++ // C5: count every storage read
	if f.readErr != nil {
		return nil, "", f.readErr
	}
	if f.fingerprint == nil {
		return nil, "", vector.ErrFingerprintMissing
	}
	return f.fingerprint, f.resourceID, nil
}

func (f *fakeVectorStore) Close() error {
	return nil
}

// newTestRagWithFingerprint creates a Rag with fingerprint infrastructure for testing.
func newTestRagWithFingerprint(
	cm *fakeChatModel,
	dr *fakeDocRetriever,
	vs *fakeVectorStore,
	conf *config.Config,
) *Rag {
	r := &Rag{
		docRet:      dr,
		rwModel:     cm,
		cm:          cm,
		conf:        conf,
		vectorStore: vs,
	}
	return r
}

// newTestRagWithFingerprintAndCreationFlag creates a Rag with fingerprint infrastructure.
// NOTE: indexJustCreated parameter is retained for test compatibility but no longer used (F1).
func newTestRagWithFingerprintAndCreationFlag(
	cm *fakeChatModel,
	dr *fakeDocRetriever,
	vs *fakeVectorStore,
	conf *config.Config,
	_ bool, // indexJustCreated — no longer used (F1)
) *Rag {
	r := &Rag{
		docRet:      dr,
		rwModel:     cm,
		cm:          cm,
		conf:        conf,
		vectorStore: vs,
	}
	return r
}

// C5: TestCheckFingerprint_ValidCacheHit removed — cache no longer exists.

func TestCheckFingerprint_MissingCache_ReadFromStore(t *testing.T) {
	// Setup: no cache, but valid fingerprint in store
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Put valid fingerprint in store
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp
	vs.resourceID = "test-resource-uuid"

	// Test: checkFingerprint should read from store and succeed
	err := r.checkFingerprint(context.Background())
	if err != nil {
		t.Errorf("checkFingerprint() with valid store fingerprint should succeed, got error: %v", err)
	}

	// C5: Cache removed — no longer verifying cache population.
}

func TestCheckFingerprint_Mismatch(t *testing.T) {
	// Setup: mismatched fingerprint in store
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Put mismatched fingerprint in store (different model)
	storedFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = storedFP
	vs.resourceID = "test-resource-uuid"

	// Test: checkFingerprint should fail with mismatch error
	err := r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("checkFingerprint() with mismatched fingerprint should fail")
	}
	if !strings.Contains(err.Error(), vector.FINGERPRINT_MISMATCH) {
		t.Errorf("checkFingerprint() error should contain %q, got: %v", vector.FINGERPRINT_MISMATCH, err)
	}
}

func TestCheckFingerprint_Missing(t *testing.T) {
	// Setup: no fingerprint in store
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// No fingerprint in store (vs.fingerprint is nil)

	// Test: checkFingerprint should fail with missing error
	err := r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("checkFingerprint() with missing fingerprint should fail")
	}
	if !strings.Contains(err.Error(), vector.FINGERPRINT_MISSING) {
		t.Errorf("checkFingerprint() error should contain %q, got: %v", vector.FINGERPRINT_MISSING, err)
	}
}

func TestCheckFingerprint_NilInfrastructure(t *testing.T) {
	// Setup: Rag without fingerprint infrastructure
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := &Rag{conf: conf}

	// Test: checkFingerprint should fail
	err := r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("checkFingerprint() without fingerprint infrastructure should fail")
	}
	if !strings.Contains(err.Error(), "vector store not set") {
		t.Errorf("checkFingerprint() error should mention 'vector store not set', got: %v", err)
	}
}

func TestRetrieve_FingerprintGate(t *testing.T) {
	// Setup: Rag with fingerprint mismatch
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	cm := &fakeChatModel{queries: []string{"query-1"}}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"query-1": {{ID: "a", Content: "a"}},
		},
	}
	r := newTestRagWithFingerprint(cm, dr, vs, conf)

	// Put mismatched fingerprint in store
	storedFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = storedFP

	// Test: Retrieve should fail before calling docRetriever
	_, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err == nil {
		t.Fatal("Retrieve() with fingerprint mismatch should fail")
	}
	if !strings.Contains(err.Error(), "fingerprint check failed") {
		t.Errorf("Retrieve() error should mention fingerprint check, got: %v", err)
	}

	// Verify docRetriever was NOT called
	dr.mu.Lock()
	callCount := dr.callCount
	dr.mu.Unlock()
	if callCount != 0 {
		t.Errorf("docRetriever should not be called when fingerprint check fails, got %d calls", callCount)
	}
}

func TestIndex_FingerprintGate(t *testing.T) {
	// Setup: Rag with missing fingerprint
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		MaxBackgroundTasks:  8,
		BackgroundTaskTimeout: 5 * 60 * 1000000000, // 5min in nanoseconds
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)
	r.bgSem = make(chan struct{}, 8)
	r.bgRootCtx, r.bgRootCancel = context.WithCancel(context.Background())
	defer r.bgRootCancel()

	// No fingerprint in store (vs.fingerprint is nil)

	// Test: Index should fail before actual indexing
	_, err := r.Index(context.Background(), &IndexReq{
		URI:           "test.txt",
		KnowledgeName: "kb",
		DocumentsId:   1,
	})
	if err == nil {
		t.Fatal("Index() with missing fingerprint should fail")
	}
	if !strings.Contains(err.Error(), "fingerprint check failed") {
		t.Errorf("Index() error should mention fingerprint check, got: %v", err)
	}
	if !errors.Is(err, vector.ErrFingerprintMissing) {
		// The error should wrap ErrFingerprintMissing
		if !strings.Contains(err.Error(), vector.FINGERPRINT_MISSING) {
			t.Errorf("Index() error should contain %q, got: %v", vector.FINGERPRINT_MISSING, err)
		}
	}
}

func TestIndexAsync_FingerprintGate(t *testing.T) {
	// Setup: Rag with corrupted fingerprint
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Put corrupted fingerprint in store (invalid version)
	storedFP := &vector.Fingerprint{
		Version:          "v99",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = storedFP

	// Test: IndexAsync should fail before actual indexing
	_, err := r.IndexAsync(context.Background(), &IndexAsyncReq{
		Docs:          []*schema.Document{{ID: "a", Content: "a"}},
		KnowledgeName: "kb",
		DocumentsId:   1,
	})
	if err == nil {
		t.Fatal("IndexAsync() with corrupted fingerprint should fail")
	}
	if !strings.Contains(err.Error(), "fingerprint check failed") {
		t.Errorf("IndexAsync() error should mention fingerprint check, got: %v", err)
	}
}

// TestVerifyFingerprint_ExistingIndexMissingFingerprint_FailClosed verifies that
// an existing index (indexJustCreated=false) without fingerprint is rejected.
func TestVerifyFingerprint_ExistingIndexMissingFingerprint_FailClosed(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	// Existing index (indexJustCreated=false)
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, false)

	// No fingerprint in store (vs.fingerprint is nil)

	// Test: VerifyFingerprint should fail with ErrFingerprintMissing
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint() on existing index without fingerprint should fail")
	}
	if !errors.Is(err, vector.ErrFingerprintMissing) {
		t.Errorf("VerifyFingerprint() should return ErrFingerprintMissing, got: %v", err)
	}

	// CRITICAL: Verify fingerprint was NOT written (no auto-backfill)
	if vs.fingerprint != nil {
		t.Error("VerifyFingerprint() must NOT auto-write fingerprint to existing index")
	}
}

// TestVerifyFingerprint_ExistingIndexCorruptedFingerprint_FailClosed verifies that
// an existing index with corrupted fingerprint is rejected.
func TestVerifyFingerprint_ExistingIndexCorruptedFingerprint_FailClosed(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	// Existing index (indexJustCreated=false)
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, false)

	// Put corrupted fingerprint in store (invalid version)
	corruptedFP := &vector.Fingerprint{
		Version:          "v99",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = corruptedFP

	// Test: VerifyFingerprint should fail with ErrFingerprintCorrupted or ErrFingerprintUnknownVersion
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint() on existing index with corrupted fingerprint should fail")
	}
	// The error should be about corrupted/unknown version fingerprint
	if !strings.Contains(err.Error(), "FINGERPRINT_CORRUPTED") &&
		!strings.Contains(err.Error(), "unknown version") &&
		!strings.Contains(err.Error(), "verification failed") {
		t.Errorf("VerifyFingerprint() should return corruption-related error, got: %v", err)
	}

	// CRITICAL: Verify fingerprint was NOT overwritten
	if !vs.fingerprint.Equal(corruptedFP) {
		t.Error("VerifyFingerprint() must NOT overwrite corrupted fingerprint")
	}
}

// TestVerifyFingerprint_ExistingIndexMismatchedFingerprint_NotOverwritten verifies that
// an existing index with mismatched fingerprint is rejected and NOT overwritten.
func TestVerifyFingerprint_ExistingIndexMismatchedFingerprint_NotOverwritten(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	// Existing index (indexJustCreated=false)
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, false)

	// Put mismatched fingerprint in store (different model)
	mismatchedFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = mismatchedFP
	vs.resourceID = "test-resource-uuid"

	// Test: VerifyFingerprint should fail with mismatch
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint() on existing index with mismatched fingerprint should fail")
	}
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Errorf("VerifyFingerprint() should return ErrFingerprintMismatch, got: %v", err)
	}

	// CRITICAL: Verify fingerprint was NOT overwritten
	if !vs.fingerprint.Equal(mismatchedFP) {
		t.Error("VerifyFingerprint() must NOT overwrite mismatched fingerprint")
	}
}

// TestVerifyFingerprint_NewIndexMissingFingerprint_FailClosed verifies that
// F1: a newly created index without fingerprint fails closed (no auto-backfill).
func TestVerifyFingerprint_NewIndexMissingFingerprint_FailClosed(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, true)

	// No fingerprint in store (vs.fingerprint is nil)

	// Test: VerifyFingerprint should fail with ErrFingerprintMissing (F1: no auto-backfill)
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint() on new index without fingerprint should fail")
	}
	if !errors.Is(err, vector.ErrFingerprintMissing) {
		t.Errorf("VerifyFingerprint() should return ErrFingerprintMissing, got: %v", err)
	}

	// CRITICAL: Verify fingerprint was NOT written (no auto-backfill)
	if vs.fingerprint != nil {
		t.Error("VerifyFingerprint() must NOT auto-write fingerprint")
	}
}

// TestVerifyFingerprint_ExistingIndexMatchingFingerprint_Succeeds verifies that
// an existing index with matching fingerprint succeeds.
func TestVerifyFingerprint_ExistingIndexMatchingFingerprint_Succeeds(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	// Existing index (indexJustCreated=false)
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, false)

	// Put matching fingerprint in store
	matchingFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = matchingFP
	vs.resourceID = "test-resource-uuid"

	// Test: VerifyFingerprint should succeed
	err := r.VerifyFingerprint(context.Background(), vs)
	if err != nil {
		t.Fatalf("VerifyFingerprint() on existing index with matching fingerprint should succeed, got: %v", err)
	}

	// C5: Cache removed — GetFingerprint always returns nil now.
}

// C5: Cache TTL tests removed — checkFingerprint now reads from storage every time.
// These tests will be rewritten in the test task.

// TestVerifyFingerprint_ConcurrentWrite_OnlyOneSucceeds verifies that
// concurrent writes to the same index result in only one succeeding.
func TestVerifyFingerprint_ConcurrentWrite_OnlyOneSucceeds(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = nil // No fingerprint yet

	// Two goroutines try to write simultaneously
	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = vs.WriteFingerprint(context.Background(), "test-index", fp, fmt.Sprintf("res-%d", idx))
		}(i)
	}
	wg.Wait()

	// Count successes and failures
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("exactly 1 write should succeed, got %d successes", successes)
	}

	// The failing one should get ErrFingerprintExists
	failures := 0
	for _, err := range errs {
		if err != nil && errors.Is(err, vector.ErrFingerprintExists) {
			failures++
		}
	}
	if failures != 1 {
		t.Errorf("exactly 1 write should get ErrFingerprintExists, got %d", failures)
	}
}

// --- R4: Empty resourceID fail-closed ---

// TestVerifyFingerprint_EmptyResourceID_FailClosed verifies that
// VerifyFingerprint with empty resourceID fails closed (R4).
func TestVerifyFingerprint_EmptyResourceID_FailClosed(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Put fingerprint with empty resourceID
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp
	vs.resourceID = "" // Empty resourceID

	// R4: VerifyFingerprint should fail with empty resourceID
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint with empty resourceID should fail")
	}
	if !errors.Is(err, vector.ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted for empty resourceID, got: %v", err)
	}
}

// TestCheckFingerprint_EmptyStoredResourceID_FailClosed verifies that
// checkFingerprint with empty stored resourceID fails closed (R4).
func TestCheckFingerprint_EmptyStoredResourceID_FailClosed(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp
	vs.resourceID = "" // Empty stored resourceID

	// R4: checkFingerprint should fail with empty stored resourceID
	err := r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("checkFingerprint with empty stored resourceID should fail")
	}
	if !errors.Is(err, vector.ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got: %v", err)
	}
}

// C5: TestCheckFingerprint_CachedResourceID_CannotDowngrade removed — cache no longer exists.

// --- C5: No-cache verification tests ---

// TestCheckFingerprint_ReadsFromStorageEveryTime verifies that checkFingerprint
// reads from storage on EVERY call (no caching).
func TestCheckFingerprint_ReadsFromStorageEveryTime(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "nocache-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp
	vs.resourceID = "test-uuid"

	// Call checkFingerprint 5 times
	for i := 0; i < 5; i++ {
		err := r.checkFingerprint(context.Background())
		if err != nil {
			t.Fatalf("call %d: checkFingerprint() unexpected error: %v", i, err)
		}
	}

	// Verify all 5 calls reached storage
	vs.mu.Lock()
	reads := vs.readCalls
	vs.mu.Unlock()
	if reads != 5 {
		t.Errorf("expected 5 storage reads, got %d (cache may still exist)", reads)
	}
}

// TestCheckFingerprint_StorageChangeDetectedImmediately verifies that if storage
// changes between two checkFingerprint calls, the second call detects the change.
func TestCheckFingerprint_StorageChangeDetectedImmediately(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "change-detect-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	matchingFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = matchingFP
	vs.resourceID = "test-uuid"

	// First call should succeed
	err := r.checkFingerprint(context.Background())
	if err != nil {
		t.Fatalf("first checkFingerprint() should succeed: %v", err)
	}

	// Simulate storage change: replace fingerprint with mismatched one
	mismatchedFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.mu.Lock()
	vs.fingerprint = mismatchedFP
	vs.mu.Unlock()

	// Second call should immediately detect the change
	err = r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("second checkFingerprint() should detect storage change")
	}

	// Verify both calls reached storage (no stale cache)
	vs.mu.Lock()
	reads := vs.readCalls
	vs.mu.Unlock()
	if reads < 2 {
		t.Errorf("expected at least 2 storage reads, got %d", reads)
	}
}

// TestCheckFingerprint_ConcurrentReadsAllFromStorage verifies that concurrent
// checkFingerprint calls all read from storage (no shared cache).
func TestCheckFingerprint_ConcurrentReadsAllFromStorage(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "concurrent-nocache-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp
	vs.resourceID = "concurrent-uuid"

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = r.checkFingerprint(context.Background())
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	// All n calls should have reached storage
	vs.mu.Lock()
	reads := vs.readCalls
	vs.mu.Unlock()
	if reads != n {
		t.Errorf("expected %d storage reads (one per goroutine), got %d", n, reads)
	}
}
