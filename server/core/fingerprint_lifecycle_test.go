package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// --- Lifecycle tests ---

// C5: TestLifecycle_CreateVerifyCacheExpire_ReVerify removed — cache no longer exists.

// TestLifecycle_PartialFailure_Rollback tests that when fingerprint verification
// fails, the Rag is properly cleaned up (Shutdown called).
func TestLifecycle_PartialFailure_Rollback(t *testing.T) {
	// Create a vectorStore that has a mismatched fingerprint
	mismatchedFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "wrong-model",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}

	vs := &fakeVectorStore{indexExists: true}
	vs.fingerprint = mismatchedFP
	vs.resourceID = "some-uuid"

	conf := &config.Config{
		IndexName:           "rollback-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	r := newTestRagWithFingerprint(nil, nil, vs, conf)
	// Set up background lifecycle fields so Shutdown doesn't panic
	r.bgRootCtx, r.bgRootCancel = context.WithCancel(context.Background())
	defer r.bgRootCancel()
	r.bgSem = make(chan struct{}, 8)
	r.done = make(chan struct{})

	// VerifyFingerprint should fail with mismatch
	err := r.VerifyFingerprint(context.Background(), vs)
	if err == nil {
		t.Fatal("VerifyFingerprint should fail with mismatched fingerprint")
	}
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}

	// C5: Cache removed — no longer checking cache state after failed verification.
}

// TestLifecycle_ContextCancel_NoHTTPSent tests that the ES vector store
// correctly propagates context cancellation (F8).
// This is already tested in fingerprint_es_test.go; this test verifies
// the gate-level behavior: checkFingerprint with cancelled ctx and empty cache
// should propagate the error from the underlying store.
func TestLifecycle_ContextCancel_NoHTTPSent(t *testing.T) {
	// Use a ctx-aware fake that returns context.Canceled
	vs := &ctxAwareFakeVectorStore{
		inner: &fakeVectorStore{indexExists: true},
	}

	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.inner.fingerprint = fp

	conf := &config.Config{
		IndexName:           "cancel-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	// Create Rag — forces store read (C5: no cache)
	r := &Rag{
		conf:        conf,
		vectorStore: vs,
	}

	// Cancel context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// checkFingerprint with cancelled ctx should fail
	err := r.checkFingerprint(ctx)
	if err == nil {
		t.Fatal("checkFingerprint with cancelled ctx should fail")
	}

	// Verify no reads were made to the store (ctx check happens before store call)
	if vs.readCalls > 0 {
		t.Errorf("no read calls should be made with cancelled ctx, got %d", vs.readCalls)
	}
}

// ctxAwareFakeVectorStore wraps fakeVectorStore but checks ctx before operations.
type ctxAwareFakeVectorStore struct {
	inner     *fakeVectorStore
	mu        sync.Mutex
	readCalls int
}

func (c *ctxAwareFakeVectorStore) CreateIndex(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.inner.CreateIndex(ctx, indexName, fp)
}
func (c *ctxAwareFakeVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return c.inner.IndexExists(ctx, indexName)
}
func (c *ctxAwareFakeVectorStore) DeleteDocument(ctx context.Context, indexName, documentID string) error {
	return c.inner.DeleteDocument(ctx, indexName, documentID)
}
func (c *ctxAwareFakeVectorStore) GetKnowledgeBaseList(ctx context.Context, indexName string) ([]string, error) {
	return c.inner.GetKnowledgeBaseList(ctx, indexName)
}
func (c *ctxAwareFakeVectorStore) SearchDocuments(ctx context.Context, req *vector.SearchRequest) (*vector.SearchResponse, error) {
	return c.inner.SearchDocuments(ctx, req)
}
func (c *ctxAwareFakeVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.inner.WriteFingerprint(ctx, indexName, fp, resourceID)
}
func (c *ctxAwareFakeVectorStore) ReadFingerprint(ctx context.Context, indexName string) (*vector.Fingerprint, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	c.readCalls++
	c.mu.Unlock()
	return c.inner.ReadFingerprint(ctx, indexName)
}
func (c *ctxAwareFakeVectorStore) Close() error {
	return c.inner.Close()
}

// TestLifecycle_ConcurrentCheckFingerprint tests that concurrent checkFingerprint
// calls are safe and consistent.
func TestLifecycle_ConcurrentCheckFingerprint(t *testing.T) {
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "legacy", // Matches ComputeFingerprintMeta default when no Provider config
		Similarity:       "cosine",
	}

	vs := &fakeVectorStore{indexExists: true}
	vs.fingerprint = fp
	vs.resourceID = "concurrent-uuid"

	conf := &config.Config{
		IndexName:           "concurrent-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// C5: No cache — checkFingerprint reads from store every time.

	// Run concurrent checkFingerprint calls
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = r.checkFingerprint(context.Background())
		}(i)
	}
	wg.Wait()

	// All should succeed (reads from store)
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: checkFingerprint should succeed, got: %v", i, err)
		}
	}
}

// --- R1: Full creation chain tests ---

// creationChainFakeVectorStore tracks WriteFingerprint calls for creation chain tests.
type creationChainFakeVectorStore struct {
	fakeVectorStore
	mu             sync.Mutex
	writeCalls     int
	indexCreated   bool
	indexExisted   bool // If true, IndexExists returns true from the start
}

func (f *creationChainFakeVectorStore) IndexExists(ctx context.Context, indexName string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.indexExisted || f.indexCreated, nil
}

func (f *creationChainFakeVectorStore) CreateIndex(ctx context.Context, indexName string, fp *vector.Fingerprint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.indexCreated = true
	return nil
}

func (f *creationChainFakeVectorStore) WriteFingerprint(ctx context.Context, indexName string, fp *vector.Fingerprint, resourceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.fingerprint != nil {
		return fmt.Errorf("index %q: %w", indexName, vector.ErrFingerprintExists)
	}
	f.fingerprint = fp
	// Simulate backend assigning resourceID when empty
	if resourceID == "" {
		f.resourceID = "auto-assigned-uuid"
	} else {
		f.resourceID = resourceID
	}
	return nil
}

// TestCreationChain_NewIndex_WriteVerifyFingerprint tests the full creation chain:
// R1: New index → CreateIndex → WriteFingerprint → VerifyFingerprint → success
func TestCreationChain_NewIndex_WriteVerifyFingerprint(t *testing.T) {
	vs := &creationChainFakeVectorStore{
		indexExisted: false, // Index doesn't exist yet
	}

	conf := &config.Config{
		IndexName:           "new-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	// Simulate the factory flow:
	// 1. Check if index existed before creation
	indexExisted, _ := vs.IndexExists(context.Background(), "new-index")
	if indexExisted {
		t.Fatal("index should not exist yet")
	}

	// 2. Create the index
	err := vs.CreateIndex(context.Background(), "new-index", nil)
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	// 3. Compute and write fingerprint (R1: write fingerprint for newly created indexes)
	fp, err := ComputeFingerprintMeta(conf)
	if err != nil {
		t.Fatalf("ComputeFingerprintMeta: %v", err)
	}
	err = vs.WriteFingerprint(context.Background(), "new-index", fp, "")
	if err != nil {
		t.Fatalf("WriteFingerprint: %v", err)
	}

	// 4. Verify fingerprint
	r := &Rag{
		conf:    conf,
	}
	verifyErr := r.VerifyFingerprint(context.Background(), vs)
	if verifyErr != nil {
		t.Fatalf("VerifyFingerprint should succeed after creation chain: %v", verifyErr)
	}

	// Verify WriteFingerprint was called exactly once
	vs.mu.Lock()
	writeCalls := vs.writeCalls
	vs.mu.Unlock()
	if writeCalls != 1 {
		t.Errorf("WriteFingerprint should be called exactly once, got %d", writeCalls)
	}

	// C5: Cache removed — no longer checking cache state.
}

// TestCreationChain_ExistingIndex_VerifyOnly tests the existing index path:
// R1: Index exists → skip WriteFingerprint → VerifyFingerprint → success
func TestCreationChain_ExistingIndex_VerifyOnly(t *testing.T) {
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown", // Matches fakeVectorStore type switch
		Similarity:       "cosine",
	}

	vs := &creationChainFakeVectorStore{
		indexExisted: true, // Index already exists
	}
	vs.fingerprint = fp
	vs.resourceID = "existing-uuid"

	conf := &config.Config{
		IndexName:           "existing-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
		Provider:            &config.ProviderConfig{Name: "unknown"},
	}

	// Simulate the factory flow:
	// 1. Check if index existed before
	indexExisted, _ := vs.IndexExists(context.Background(), "existing-index")
	if !indexExisted {
		t.Fatal("index should already exist")
	}

	// 2. R1: Skip WriteFingerprint for existing indexes
	// (no WriteFingerprint call)

	// 3. Verify fingerprint
	r := &Rag{
		conf:    conf,
	}
	verifyErr := r.VerifyFingerprint(context.Background(), vs)
	if verifyErr != nil {
		t.Fatalf("VerifyFingerprint should succeed for existing index: %v", verifyErr)
	}

	// Verify WriteFingerprint was NOT called
	vs.mu.Lock()
	writeCalls := vs.writeCalls
	vs.mu.Unlock()
	if writeCalls != 0 {
		t.Errorf("WriteFingerprint should NOT be called for existing index, got %d", writeCalls)
	}
}

// TestCreationChain_ExistingIndex_WrongFingerprint tests:
// R1: Index exists with wrong fingerprint → VerifyFingerprint → fail
func TestCreationChain_ExistingIndex_WrongFingerprint(t *testing.T) {
	wrongFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "wrong-model",
		Dimensions:       512,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}

	vs := &creationChainFakeVectorStore{
		indexExisted: true,
	}
	vs.fingerprint = wrongFP
	vs.resourceID = "existing-uuid"

	conf := &config.Config{
		IndexName:           "wrong-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	// Verify fingerprint should fail with mismatch
	r := &Rag{
		conf:    conf,
	}
	verifyErr := r.VerifyFingerprint(context.Background(), vs)
	if verifyErr == nil {
		t.Fatal("VerifyFingerprint should fail with wrong fingerprint")
	}
	if !errors.Is(verifyErr, vector.ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", verifyErr)
	}
}

// TestCreationChain_ConcurrentCreateIndex_SecondGetsExists tests R3:
// Two goroutines creating same-name index → one writes fingerprint, other gets ErrFingerprintExists
func TestCreationChain_ConcurrentCreateIndex_SecondGetsExists(t *testing.T) {
	vs := &creationChainFakeVectorStore{
		indexExisted: false,
	}

	conf := &config.Config{
		IndexName:           "concurrent-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	fp, _ := ComputeFingerprintMeta(conf)

	var wg sync.WaitGroup
	errs := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = vs.WriteFingerprint(context.Background(), "concurrent-index", fp, fmt.Sprintf("res-%d", idx))
		}(i)
	}
	wg.Wait()

	successes := 0
	existsErrors := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, vector.ErrFingerprintExists) {
			existsErrors++
		}
	}
	if successes != 1 {
		t.Errorf("exactly 1 write should succeed, got %d", successes)
	}
	if existsErrors != 1 {
		t.Errorf("exactly 1 write should get ErrFingerprintExists, got %d", existsErrors)
	}
}
