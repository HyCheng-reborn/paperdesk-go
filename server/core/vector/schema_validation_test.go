package vector

import (
	"encoding/json"
	"errors"
	"testing"
)

// --- D3: ValidateSchemaStrict unit tests ---

func TestSchema_MissingContentVector_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		// content_vector missing
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for missing content_vector")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_MissingQAContentVector_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
		// qa_content_vector missing
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for missing qa_content_vector")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_WrongFieldType_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "text", Dims: json.Number("1024"), Distance: "cosine"}, // text instead of dense_vector
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for wrong field type")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_Float64Dims_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: float64(1024.9), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for float64 dims")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_StringDims_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: "1024", Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for string dims")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_NilDims_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: nil, Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for nil dims")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_DimsMismatch_Error(t *testing.T) {
	fp := testFingerprint() // 1024 dims
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("512"), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("512"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for dims mismatch (512 vs 1024)")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_WrongDistance_Error(t *testing.T) {
	fp := testFingerprint() // cosine
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "dot_product"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for wrong distance/similarity mapping")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_ValidES_Success(t *testing.T) {
	fp := testFingerprint() // 1024 dims, cosine, bailian
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine", Index: boolPtr(true)},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine", Index: boolPtr(true)},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err != nil {
		t.Fatalf("valid ES schema should succeed, got: %v", err)
	}
}

func TestSchema_ValidES_DotProduct(t *testing.T) {
	fp := &Fingerprint{
		Version: "v1", EmbeddingModelID: "m", Dimensions: 768,
		ProviderName: "p", Similarity: "dot",
	}
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("768"), Distance: "dot_product"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("768"), Distance: "dot_product"},
	}
	if err := ValidateSchemaStrict(fp, fields); err != nil {
		t.Fatalf("dot_product → dot should match, got: %v", err)
	}
}

func TestSchema_ValidES_L2Norm(t *testing.T) {
	fp := &Fingerprint{
		Version: "v1", EmbeddingModelID: "m", Dimensions: 768,
		ProviderName: "p", Similarity: "euclidean",
	}
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("768"), Distance: "l2_norm"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("768"), Distance: "l2_norm"},
	}
	if err := ValidateSchemaStrict(fp, fields); err != nil {
		t.Fatalf("l2_norm → euclidean should match, got: %v", err)
	}
}

func TestSchema_QdrantDistanceMapping(t *testing.T) {
	tests := []struct {
		distance   string
		similarity string
		wantErr    bool
	}{
		{"Cosine", "cosine", false},
		{"Dot", "dot", false},
		{"Euclid", "euclidean", false},
		{"Cosine", "dot", true},      // mismatch
		{"Dot", "cosine", true},       // mismatch
		{"Euclid", "cosine", true},    // mismatch
	}
	for _, tt := range tests {
		t.Run(tt.distance+"→"+tt.similarity, func(t *testing.T) {
			fp := &Fingerprint{
				Version: "v1", EmbeddingModelID: "m", Dimensions: 512,
				ProviderName: "p", Similarity: tt.similarity,
			}
			fields := map[string]FieldInfo{
				"content_vector":     {Dims: json.Number("512"), Distance: tt.distance},
				"qa_content_vector": {Dims: json.Number("512"), Distance: tt.distance},
			}
			err := ValidateSchemaStrict(fp, fields)
			if tt.wantErr && err == nil {
				t.Fatal("expected error for distance mismatch")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSchema_NilFingerprint_Error(t *testing.T) {
	err := ValidateSchemaStrict(nil, map[string]FieldInfo{})
	if err == nil {
		t.Fatal("expected error for nil fingerprint")
	}
}

func TestSchema_EmptyFields_Error(t *testing.T) {
	fp := testFingerprint()
	err := ValidateSchemaStrict(fp, map[string]FieldInfo{})
	if err == nil {
		t.Fatal("expected error for empty fields")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func TestSchema_IntDims_Success(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: int(1024), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: int(1024), Distance: "cosine"},
	}
	if err := ValidateSchemaStrict(fp, fields); err != nil {
		t.Fatalf("int dims should succeed, got: %v", err)
	}
}

func TestSchema_Int64Dims_Success(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: int64(1024), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: int64(1024), Distance: "cosine"},
	}
	if err := ValidateSchemaStrict(fp, fields); err != nil {
		t.Fatalf("int64 dims should succeed, got: %v", err)
	}
}

func TestSchema_NegativeDims_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("-1"), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for negative dims")
	}
}

func TestSchema_ZeroDims_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("0"), Distance: "cosine"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for zero dims")
	}
}

func TestSchema_QdrantEmptyFieldType_Success(t *testing.T) {
	// Qdrant named vectors don't have a type field — empty FieldType should be OK
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "", Dims: json.Number("1024"), Distance: "Cosine"},
		"qa_content_vector": {FieldType: "", Dims: json.Number("1024"), Distance: "Cosine"},
	}
	if err := ValidateSchemaStrict(fp, fields); err != nil {
		t.Fatalf("Qdrant empty field type should succeed, got: %v", err)
	}
}

func TestSchema_UnknownDistance_Error(t *testing.T) {
	fp := testFingerprint()
	fields := map[string]FieldInfo{
		"content_vector":     {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "hamming"},
		"qa_content_vector": {FieldType: "dense_vector", Dims: json.Number("1024"), Distance: "cosine"},
	}
	err := ValidateSchemaStrict(fp, fields)
	if err == nil {
		t.Fatal("expected error for unknown distance")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Errorf("expected ErrFingerprintMismatch, got: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }
