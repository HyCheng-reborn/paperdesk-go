package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// fakeVectorStore implements vector.VectorStore for testing fingerprint gates.
type fakeVectorStore struct {
	fingerprint *vector.Fingerprint
	readErr     error
	writeErr    error
	indexExists bool
}

func (f *fakeVectorStore) CreateIndex(ctx context.Context, indexName string) error {
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

func (f *fakeVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.fingerprint = fp
	return nil
}

func (f *fakeVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*vector.Fingerprint, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.fingerprint == nil {
		return nil, vector.ErrFingerprintMissing
	}
	return f.fingerprint, nil
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
		fpCache:     newFingerprintCache(),
		vectorStore: vs,
	}
	return r
}

// newTestRagWithFingerprintAndCreationFlag creates a Rag with fingerprint infrastructure and indexJustCreated flag.
func newTestRagWithFingerprintAndCreationFlag(
	cm *fakeChatModel,
	dr *fakeDocRetriever,
	vs *fakeVectorStore,
	conf *config.Config,
	indexJustCreated bool,
) *Rag {
	r := &Rag{
		docRet:           dr,
		rwModel:          cm,
		cm:               cm,
		conf:             conf,
		fpCache:          newFingerprintCache(),
		vectorStore:      vs,
		indexJustCreated: indexJustCreated,
	}
	return r
}

func TestCheckFingerprint_ValidCacheHit(t *testing.T) {
	// Setup: valid fingerprint in cache
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Pre-populate cache with valid fingerprint
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	r.fpCache.set("unknown:test-index", fp)

	// Test: checkFingerprint should succeed
	err := r.checkFingerprint(context.Background())
	if err != nil {
		t.Errorf("checkFingerprint() with valid cache should succeed, got error: %v", err)
	}
}

func TestCheckFingerprint_MissingCache_ReadFromStore(t *testing.T) {
	// Setup: no cache, but valid fingerprint in store
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "test"},
	}
	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Put valid fingerprint in store
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "test",
		Similarity:       "cosine",
	}
	vs.fingerprint = fp

	// Test: checkFingerprint should read from store and succeed
	err := r.checkFingerprint(context.Background())
	if err != nil {
		t.Errorf("checkFingerprint() with valid store fingerprint should succeed, got error: %v", err)
	}

	// Verify cache was populated
	cachedFP, ok := r.fpCache.get("unknown:test-index")
	if !ok || cachedFP == nil {
		t.Error("checkFingerprint() should populate cache after reading from store")
	}
}

func TestCheckFingerprint_Mismatch(t *testing.T) {
	// Setup: mismatched fingerprint in store
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
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
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("checkFingerprint() error should mention 'not initialized', got: %v", err)
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

// TestVerifyFingerprint_NewIndex_WritesFingerprint verifies that
// a newly created index (indexJustCreated=true) without fingerprint gets it written.
func TestVerifyFingerprint_NewIndex_WritesFingerprint(t *testing.T) {
	vs := &fakeVectorStore{indexExists: true}
	conf := &config.Config{
		IndexName:           "test-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}
	// New index (indexJustCreated=true)
	r := newTestRagWithFingerprintAndCreationFlag(nil, nil, vs, conf, true)

	// No fingerprint in store (vs.fingerprint is nil)

	// Test: VerifyFingerprint should succeed and write fingerprint
	err := r.VerifyFingerprint(context.Background(), vs)
	if err != nil {
		t.Fatalf("VerifyFingerprint() on new index should succeed, got: %v", err)
	}

	// Verify fingerprint was written
	if vs.fingerprint == nil {
		t.Fatal("VerifyFingerprint() should write fingerprint to new index")
	}

	// Verify the written fingerprint matches expected
	if vs.fingerprint.EmbeddingModelID != "text-embedding-v4" {
		t.Errorf("Written fingerprint model = %q, want %q",
			vs.fingerprint.EmbeddingModelID, "text-embedding-v4")
	}
	if vs.fingerprint.Dimensions != 1024 {
		t.Errorf("Written fingerprint dimensions = %d, want %d",
			vs.fingerprint.Dimensions, 1024)
	}

	// Verify fingerprint was cached
	cachedFP := r.GetFingerprint()
	if cachedFP == nil {
		t.Error("VerifyFingerprint() should cache the verified fingerprint")
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

	// Test: VerifyFingerprint should succeed
	err := r.VerifyFingerprint(context.Background(), vs)
	if err != nil {
		t.Fatalf("VerifyFingerprint() on existing index with matching fingerprint should succeed, got: %v", err)
	}

	// Verify fingerprint was cached
	cachedFP := r.GetFingerprint()
	if cachedFP == nil {
		t.Error("VerifyFingerprint() should cache the verified fingerprint")
	}
}
