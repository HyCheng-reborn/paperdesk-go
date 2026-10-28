package indexer

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/qdrant/go-client/qdrant"
)

// fakeEmbedder implements embedding.Embedder for testing.
type fakeEmbedder struct{}

func (f *fakeEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i := range texts {
		result[i] = make([]float64, 1024)
	}
	return result, nil
}

// --- E2: SDK auto-creation elimination tests ---

// TestE2_NewQdrantIndexer_NoEnsureCollection verifies that NewQdrantIndexer
// does NOT trigger collection creation (no ensureCollection side effect).
// E2 fix: einoIndexer field removed from QdrantIndexer struct.
// The constructor only validates config and returns the struct — no network calls.
func TestE2_NewQdrantIndexer_NoEnsureCollection(t *testing.T) {
	// E2: After removing einoIndexer, the constructor no longer creates an SDK indexer
	// that would call ensureCollection. Only config validation happens.
	// We use a non-nil client (required by validation) but it's never used during construction.
	// The key assertion: construction succeeds without any network calls.
	config := &QdrantIndexerConfig{
		Client:     &qdrant.Client{}, // Non-nil (validation requires it) but not connected
		Collection: "test-collection",
		VectorDim:  1024,
		Distance:   qdrant.Distance_Cosine,
		BatchSize:  10,
		Embedding:  &fakeEmbedder{},
	}

	idx, err := NewQdrantIndexer(context.Background(), config)
	if err != nil {
		t.Fatalf("E2: NewQdrantIndexer should succeed (no ensureCollection): %v", err)
	}
	if idx == nil {
		t.Fatal("E2: NewQdrantIndexer should return non-nil indexer")
	}

	// Verify the returned type is *QdrantIndexer (our custom implementation)
	qIdx, ok := idx.(*QdrantIndexer)
	if !ok {
		t.Fatal("E2: returned indexer should be *QdrantIndexer type")
	}
	if qIdx.config != config {
		t.Fatal("E2: config should be preserved")
	}
	// E2: asyncIndexer should be nil (not constructed in basic NewQdrantIndexer)
	if qIdx.asyncIndexer != nil {
		t.Fatal("E2: asyncIndexer should be nil for non-async mode")
	}
}

// TestE2_NewQdrantIndexer_ValidationErrors verifies that NewQdrantIndexer
// still validates required config fields.
func TestE2_NewQdrantIndexer_ValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		config *QdrantIndexerConfig
	}{
		{
			name:   "nil client",
			config: &QdrantIndexerConfig{Collection: "c", Embedding: &fakeEmbedder{}},
		},
		{
			name:   "empty collection",
			config: &QdrantIndexerConfig{Client: &qdrant.Client{}, Embedding: &fakeEmbedder{}},
		},
		{
			name:   "nil embedding",
			config: &QdrantIndexerConfig{Client: &qdrant.Client{}, Collection: "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewQdrantIndexer(context.Background(), tt.config)
			if err == nil {
				t.Errorf("E2: NewQdrantIndexer with %s should fail", tt.name)
			}
		})
	}
}

// TestE2_NewQdrantIndexer_Defaults verifies that NewQdrantIndexer
// applies default values correctly.
func TestE2_NewQdrantIndexer_Defaults(t *testing.T) {
	// Use a non-nil client placeholder to pass validation
	// (we can't use nil client for defaults test since validation checks it)
	// Actually, E2 removed the client requirement for construction.
	// But validation still requires non-nil client. Let me re-check the code.
	// The code at line 35: if config.Client == nil { return nil, error }
	// So client IS required. But no network calls are made.
	// We need a real client object but it won't be used during construction.
	config := &QdrantIndexerConfig{
		Client:     &qdrant.Client{}, // Non-nil but not connected
		Collection: "test-collection",
		Embedding:  &fakeEmbedder{},
		// BatchSize=0 → should default to 10
		// Distance=0 → should default to Cosine
	}

	idx, err := NewQdrantIndexer(context.Background(), config)
	if err != nil {
		t.Fatalf("E2: NewQdrantIndexer should succeed: %v", err)
	}

	qIdx := idx.(*QdrantIndexer)
	if qIdx.config.BatchSize != 10 {
		t.Errorf("E2: default BatchSize should be 10, got %d", qIdx.config.BatchSize)
	}
	if qIdx.config.Distance != qdrant.Distance_Cosine {
		t.Errorf("E2: default Distance should be Cosine, got %v", qIdx.config.Distance)
	}
}
