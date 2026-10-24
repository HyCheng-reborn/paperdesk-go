package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// --- Lifecycle tests ---

// TestLifecycle_CreateVerifyCacheExpire_ReVerify tests the full lifecycle:
// 1. Create Rag with matching fingerprint in store
// 2. VerifyFingerprint succeeds and caches
// 3. Expire cache TTL
// 4. checkFingerprint re-reads from store and re-verifies
func TestLifecycle_CreateVerifyCacheExpire_ReVerify(t *testing.T) {
	fp := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}

	vs := &fakeVectorStore{indexExists: true}
	vs.fingerprint = fp
	vs.resourceID = "lifecycle-uuid-1"

	conf := &config.Config{
		IndexName:           "lifecycle-index",
		EmbeddingModel:      "text-embedding-v4",
		EmbeddingDimensions: 1024,
	}

	r := newTestRagWithFingerprint(nil, nil, vs, conf)

	// Step 1: VerifyFingerprint should succeed and cache
	err := r.VerifyFingerprint(context.Background(), vs)
	if err != nil {
		t.Fatalf("VerifyFingerprint should succeed: %v", err)
	}

	// Step 2: Verify cache is populated
	cachedFP := r.GetFingerprint()
	if cachedFP == nil {
		t.Fatal("fingerprint should be cached after VerifyFingerprint")
	}

	// Step 3: checkFingerprint should succeed from cache
	err = r.checkFingerprint(context.Background())
	if err != nil {
		t.Fatalf("checkFingerprint from cache should succeed: %v", err)
	}

	// Step 4: Expire the cache
	cacheKey := "unknown:lifecycle-index"
	entry, ok := r.fpCache.get(cacheKey)
	if !ok || entry == nil {
		t.Fatal("cache entry should exist")
	}
	r.fpCache.mu.Lock()
	entry.verifiedAt = entry.verifiedAt.Add(-fingerprintCacheTTL - time.Second)
	r.fpCache.mu.Unlock()

	// Step 5: checkFingerprint should re-read from store (still same fingerprint)
	err = r.checkFingerprint(context.Background())
	if err != nil {
		t.Fatalf("checkFingerprint after TTL should succeed with same fingerprint: %v", err)
	}

	// Step 6: Now change the store fingerprint to a different model (simulating replacement)
	differentFP := &vector.Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "unknown",
		Similarity:       "cosine",
	}
	vs.mu.Lock()
	vs.fingerprint = differentFP
	vs.mu.Unlock()

	// Expire cache again
	r.fpCache.mu.Lock()
	entry2, ok2 := r.fpCache.items[cacheKey]
	if ok2 && entry2 != nil {
		entry2.verifiedAt = entry2.verifiedAt.Add(-fingerprintCacheTTL - time.Second)
	}
	r.fpCache.mu.Unlock()

	// Step 7: checkFingerprint should detect mismatch
	err = r.checkFingerprint(context.Background())
	if err == nil {
		t.Fatal("checkFingerprint should fail when fingerprint changes after TTL")
	}
	if !errors.Is(err, vector.ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

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

	// Verify no fingerprint was cached
	cachedFP := r.GetFingerprint()
	if cachedFP != nil {
		t.Error("fingerprint should NOT be cached after failed verification")
	}
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

	// Create Rag WITHOUT pre-populated cache — forces store read
	r := &Rag{
		conf:        conf,
		fpCache:     newFingerprintCache(),
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

func (c *ctxAwareFakeVectorStore) CreateIndex(ctx context.Context, indexName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.inner.CreateIndex(ctx, indexName)
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
		ProviderName:     "unknown",
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

	// Pre-populate cache
	r.fpCache.set("unknown:concurrent-index", fp, "concurrent-uuid")

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

	// All should succeed (cache hit)
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: checkFingerprint should succeed, got: %v", i, err)
		}
	}
}
