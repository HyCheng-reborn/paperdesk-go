package vector

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	gormmodel "github.com/wangle201210/go-rag/server/internal/model/gorm"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newTestQdrantStore creates a QdrantVectorStore backed by SQLite in-memory for testing.
// Only WriteFingerprint/ReadFingerprint are tested (they only use s.db, not s.client).
func newTestQdrantStore(t *testing.T) *QdrantVectorStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&gormmodel.IndexFingerprint{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return &QdrantVectorStore{db: db}
}

func testFingerprint() *Fingerprint {
	return &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
}

// --- F3: Conditional write ---

func TestQdrant_WriteFingerprint_ConditionalWrite(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
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

// --- F2: ResourceID ---

func TestQdrant_ReadFingerprint_ReturnsResourceID(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Write with specific resourceID
	err := store.WriteFingerprint(ctx, "test-index", fp, "uuid-abc-123")
	if err != nil {
		t.Fatalf("WriteFingerprint: %v", err)
	}

	// Read back should return same resourceID
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

// --- F2: Ready flag ---

func TestQdrant_ReadFingerprint_NotReady(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Manually insert a record with Ready=false
	fpJSON, _ := fp.MarshalJSON()
	record := gormmodel.IndexFingerprint{
		VectorStoreType:   "qdrant",
		IndexName:         "test-index",
		ResourceID:        "resource-x",
		FingerprintJSON:   string(fpJSON),
		FingerprintDigest: fp.Digest(),
		EmbeddingModelID:  fp.EmbeddingModelID,
		Dimensions:        fp.Dimensions,
		ProviderName:      fp.ProviderName,
		Similarity:        fp.Similarity,
		Ready:             false, // Not ready
	}
	if result := store.db.Create(&record); result.Error != nil {
		t.Fatalf("create record: %v", result.Error)
	}

	// ReadFingerprint should fail because Ready=false
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail for Ready=false record")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted for incomplete creation, got: %v", err)
	}
}

// --- F7: Digest verification ---

func TestQdrant_ReadFingerprint_DigestVerification(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Manually insert a record with tampered digest IN THE JSON (R5)
	fpJSON, _ := fp.MarshalJSON()
	// Tamper with the digest inside the JSON
	var fpMap map[string]any
	json.Unmarshal(fpJSON, &fpMap)
	fpMap["digest"] = "sha256:tampered-digest-00000000000000000000000000000000000000000000"
	tamperedJSON, _ := json.Marshal(fpMap)

	record := gormmodel.IndexFingerprint{
		VectorStoreType:   "qdrant",
		IndexName:         "test-index",
		ResourceID:        "resource-y",
		FingerprintJSON:   string(tamperedJSON),
		FingerprintDigest: "sha256:tampered-digest-00000000000000000000000000000000000000000000",
		EmbeddingModelID:  fp.EmbeddingModelID,
		Dimensions:        fp.Dimensions,
		ProviderName:      fp.ProviderName,
		Similarity:        fp.Similarity,
		Ready:             true,
	}
	if result := store.db.Create(&record); result.Error != nil {
		t.Fatalf("create record: %v", result.Error)
	}

	// ReadFingerprint should fail with ErrFingerprintCorrupted
	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail for tampered digest")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got: %v", err)
	}
}

// --- F2: ResourceID isolation ---

func TestQdrant_ResourceID_Isolation(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Write for index "a" with resourceID "res-a"
	if err := store.WriteFingerprint(ctx, "index-a", fp, "res-a"); err != nil {
		t.Fatalf("WriteFingerprint index-a: %v", err)
	}

	// Write for index "b" with resourceID "res-b"
	if err := store.WriteFingerprint(ctx, "index-b", fp, "res-b"); err != nil {
		t.Fatalf("WriteFingerprint index-b: %v", err)
	}

	// Read index-a → should get res-a
	_, ridA, err := store.ReadFingerprint(ctx, "index-a")
	if err != nil {
		t.Fatalf("ReadFingerprint index-a: %v", err)
	}
	if ridA != "res-a" {
		t.Errorf("index-a resourceID = %q, want %q", ridA, "res-a")
	}

	// Read index-b → should get res-b
	_, ridB, err := store.ReadFingerprint(ctx, "index-b")
	if err != nil {
		t.Fatalf("ReadFingerprint index-b: %v", err)
	}
	if ridB != "res-b" {
		t.Errorf("index-b resourceID = %q, want %q", ridB, "res-b")
	}
}

// --- F6: varchar(128) digest column ---

func TestQdrant_Varchar128_Digest(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Digest is "sha256:" + 64 hex chars = 71 chars
	digest := fp.Digest()
	if len(digest) != 71 {
		t.Fatalf("expected digest length 71, got %d: %q", len(digest), digest)
	}

	// Write should succeed (varchar(128) can hold 71 chars)
	if err := store.WriteFingerprint(ctx, "test-index", fp, "res-1"); err != nil {
		t.Fatalf("WriteFingerprint: %v", err)
	}

	// Read back and verify digest fits
	readFP, _, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint: %v", err)
	}
	if readFP.Digest() != digest {
		t.Errorf("digest mismatch after round-trip: got %q, want %q", readFP.Digest(), digest)
	}

	// Verify the column can hold the full digest by reading raw record
	var rawRecord gormmodel.IndexFingerprint
	if result := store.db.Where("index_name = ?", "test-index").First(&rawRecord); result.Error != nil {
		t.Fatalf("read raw record: %v", result.Error)
	}
	if len(rawRecord.FingerprintDigest) != 71 {
		t.Errorf("stored digest length = %d, want 71", len(rawRecord.FingerprintDigest))
	}
	if !strings.HasPrefix(rawRecord.FingerprintDigest, "sha256:") {
		t.Errorf("stored digest should start with sha256:, got %q", rawRecord.FingerprintDigest)
	}
}

// --- Missing fingerprint ---

func TestQdrant_ReadFingerprint_Missing(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()

	_, _, err := store.ReadFingerprint(ctx, "nonexistent-index")
	if err == nil {
		t.Fatal("ReadFingerprint should fail for nonexistent index")
	}
	if !errors.Is(err, ErrFingerprintMissing) {
		t.Errorf("expected ErrFingerprintMissing, got: %v", err)
	}
}

// --- Nil fingerprint ---

func TestQdrant_WriteFingerprint_NilFingerprint(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()

	err := store.WriteFingerprint(ctx, "test-index", nil, "res-1")
	if err == nil {
		t.Fatal("WriteFingerprint with nil fingerprint should fail")
	}
}

// --- Nil database ---

func TestQdrant_WriteFingerprint_NilDB(t *testing.T) {
	store := &QdrantVectorStore{} // no db
	ctx := context.Background()

	err := store.WriteFingerprint(ctx, "test-index", testFingerprint(), "res-1")
	if err == nil {
		t.Fatal("WriteFingerprint with nil db should fail")
	}
}

func TestQdrant_ReadFingerprint_NilDB(t *testing.T) {
	store := &QdrantVectorStore{} // no db
	ctx := context.Background()

	_, _, err := store.ReadFingerprint(ctx, "test-index")
	if err == nil {
		t.Fatal("ReadFingerprint with nil db should fail")
	}
}

// --- Auto-generate resourceID when empty ---

func TestQdrant_WriteFingerprint_AutoGenerateResourceID(t *testing.T) {
	store := newTestQdrantStore(t)
	ctx := context.Background()
	fp := testFingerprint()

	// Write with empty resourceID — should auto-generate
	if err := store.WriteFingerprint(ctx, "test-index", fp, ""); err != nil {
		t.Fatalf("WriteFingerprint: %v", err)
	}

	// Read back — resourceID should be non-empty (auto-generated UUID)
	_, resourceID, err := store.ReadFingerprint(ctx, "test-index")
	if err != nil {
		t.Fatalf("ReadFingerprint: %v", err)
	}
	if resourceID == "" {
		t.Error("auto-generated resourceID should not be empty")
	}
}

// --- E1: Qdrant mapQdrantDistance tests ---

func TestQdrant_MapDistance_UnknownEnum0_Error(t *testing.T) {
	_, err := mapQdrantDistance(qdrant.Distance(0))
	if err == nil {
		t.Fatal("E1: unknown distance enum 0 should return error")
	}
	if !strings.Contains(err.Error(), "unknown Qdrant distance enum") {
		t.Errorf("E1: expected 'unknown Qdrant distance enum' error, got: %v", err)
	}
}

func TestQdrant_MapDistance_UnknownEnum99_Error(t *testing.T) {
	_, err := mapQdrantDistance(qdrant.Distance(99))
	if err == nil {
		t.Fatal("E1: unknown distance enum 99 should return error")
	}
	if !strings.Contains(err.Error(), "unknown Qdrant distance enum") {
		t.Errorf("E1: expected 'unknown Qdrant distance enum' error, got: %v", err)
	}
}

func TestQdrant_MapDistance_AllKnown(t *testing.T) {
	tests := []struct {
		dist qdrant.Distance
		want string
	}{
		{qdrant.Distance_Cosine, "Cosine"},
		{qdrant.Distance_Dot, "Dot"},
		{qdrant.Distance_Euclid, "Euclid"},
		{qdrant.Distance_Manhattan, "Manhattan"},
	}
	for _, tt := range tests {
		got, err := mapQdrantDistance(tt.dist)
		if err != nil {
			t.Errorf("mapQdrantDistance(%v) unexpected error: %v", tt.dist, err)
		}
		if got != tt.want {
			t.Errorf("mapQdrantDistance(%v) = %q, want %q", tt.dist, got, tt.want)
		}
	}
}

// --- E1: Qdrant extractQdrantFieldInfo tests ---

func TestQdrant_ExtractFieldInfo_NilInfo_Error(t *testing.T) {
	_, err := extractQdrantFieldInfo(nil)
	if err == nil {
		t.Fatal("E1: nil CollectionInfo should return error")
	}
}

func TestQdrant_ExtractFieldInfo_Manhattan_CosineFingerprint_Mismatch(t *testing.T) {
	// E1: Manhattan distance should not match cosine similarity
	info := &qdrant.CollectionInfo{
		Config: &qdrant.CollectionConfig{
			Params: &qdrant.CollectionParams{
				VectorsConfig: &qdrant.VectorsConfig{
					Config: &qdrant.VectorsConfig_ParamsMap{
						ParamsMap: &qdrant.VectorParamsMap{
							Map: map[string]*qdrant.VectorParams{
								"content_vector": {
									Size:     1024,
									Distance: qdrant.Distance_Manhattan,
								},
								"qa_content_vector": {
									Size:     1024,
									Distance: qdrant.Distance_Manhattan,
								},
							},
						},
					},
				},
			},
		},
	}

	fields, err := extractQdrantFieldInfo(info)
	if err != nil {
		t.Fatalf("extractQdrantFieldInfo: %v", err)
	}

	// ValidateSchemaStrict should reject Manhattan with cosine fingerprint
	fp := testFingerprint() // similarity=cosine
	schemaErr := ValidateSchemaStrict(fp, fields, "qdrant")
	if schemaErr == nil {
		t.Fatal("E1: Manhattan distance with cosine fingerprint should be rejected")
	}
	if !errors.Is(schemaErr, ErrFingerprintMismatch) {
		t.Errorf("E1: expected ErrFingerprintMismatch, got: %v", schemaErr)
	}
}

func TestQdrant_ExtractFieldInfo_ValidCosine_Success(t *testing.T) {
	// E1: Valid Cosine distance with cosine fingerprint should succeed
	info := &qdrant.CollectionInfo{
		Config: &qdrant.CollectionConfig{
			Params: &qdrant.CollectionParams{
				VectorsConfig: &qdrant.VectorsConfig{
					Config: &qdrant.VectorsConfig_ParamsMap{
						ParamsMap: &qdrant.VectorParamsMap{
							Map: map[string]*qdrant.VectorParams{
								"content_vector": {
									Size:     1024,
									Distance: qdrant.Distance_Cosine,
								},
								"qa_content_vector": {
									Size:     1024,
									Distance: qdrant.Distance_Cosine,
								},
							},
						},
					},
				},
			},
		},
	}

	fields, err := extractQdrantFieldInfo(info)
	if err != nil {
		t.Fatalf("extractQdrantFieldInfo: %v", err)
	}

	fp := testFingerprint() // similarity=cosine, dims=1024
	schemaErr := ValidateSchemaStrict(fp, fields, "qdrant")
	if schemaErr != nil {
		t.Fatalf("E1: valid Cosine distance with cosine fingerprint should succeed, got: %v", schemaErr)
	}
}

func TestQdrant_ExtractFieldInfo_UnknownDistance_Error(t *testing.T) {
	// E1: Unknown distance enum (0) should return error from extractQdrantFieldInfo
	info := &qdrant.CollectionInfo{
		Config: &qdrant.CollectionConfig{
			Params: &qdrant.CollectionParams{
				VectorsConfig: &qdrant.VectorsConfig{
					Config: &qdrant.VectorsConfig_ParamsMap{
						ParamsMap: &qdrant.VectorParamsMap{
							Map: map[string]*qdrant.VectorParams{
								"content_vector": {
									Size:     1024,
									Distance: qdrant.Distance(0), // Unknown
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := extractQdrantFieldInfo(info)
	if err == nil {
		t.Fatal("E1: unknown distance enum 0 should return error from extractQdrantFieldInfo")
	}
	if !strings.Contains(err.Error(), "unknown Qdrant distance enum") {
		t.Errorf("E1: expected 'unknown Qdrant distance enum' error, got: %v", err)
	}
}

// --- E1: Qdrant distanceMatchesSimilarity integration ---

func TestQdrant_DistanceMatchesSimilarity_Manhattan_AlwaysFalse(t *testing.T) {
	// E1: Manhattan doesn't match any supported similarity
	for _, sim := range []string{"cosine", "dot", "euclidean"} {
		if distanceMatchesSimilarity("Manhattan", sim) {
			t.Errorf("E1: Manhattan should not match %q", sim)
		}
	}
}

func TestQdrant_DistanceMatchesSimilarity_Unknown_AlwaysFalse(t *testing.T) {
	// E1: Unknown distances always rejected
	for _, dist := range []string{"hamming", "angular", ""} {
		if distanceMatchesSimilarity(dist, "cosine") {
			t.Errorf("E1: unknown distance %q should not match cosine", dist)
		}
	}
}
