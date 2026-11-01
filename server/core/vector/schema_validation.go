package vector

import (
	"encoding/json"
	"fmt"
)

// FieldInfo represents a vector field's schema properties for strict validation.
type FieldInfo struct {
	FieldType string      // "dense_vector" for ES, "" for Qdrant (named vector)
	Dims      interface{} // json.Number, int64, float64, string, or nil — strict type check
	Distance  string      // ES: "cosine"/"dot_product"/"l2_norm"; Qdrant: "Cosine"/"Dot"/"Euclid"/"Manhattan"
	Index     *bool       // ES dense_vector index property (nil = not set)
}

// RequiredVectorFields are the named vectors that must exist.
var RequiredVectorFields = []string{"content_vector", "qa_content_vector"}

// ValidateSchemaStrict validates that the actual schema matches the fingerprint declaration.
// Missing/invalid/unknown fields → error. No silent skipping.
// Used by both ES and Qdrant CreateIndex (post-creation verification) and ReadFingerprint (runtime verification).
// backend parameter distinguishes ES vs Qdrant semantics:
//   - "es": FieldType must be "dense_vector"; Distance uses ES names (cosine/dot_product/l2_norm)
//   - "qdrant": FieldType ignored (named vectors don't have type); Distance uses Qdrant names (Cosine/Dot/Euclid/Manhattan)
func ValidateSchemaStrict(fp *Fingerprint, fields map[string]FieldInfo, backend string) error {
	if fp == nil {
		return fmt.Errorf("ValidateSchemaStrict: nil fingerprint")
	}

	// 1. Check all required fields exist
	for _, name := range RequiredVectorFields {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("%w: required vector field %q missing", ErrFingerprintMismatch, name)
		}
	}

	// 2. Validate each required field
	for _, name := range RequiredVectorFields {
		fi := fields[name]

		// Field type check: ES requires "dense_vector"; Qdrant ignores (named vectors don't have type)
		if backend == "es" {
			if fi.FieldType != "dense_vector" {
				return fmt.Errorf("%w: field %q type %q, expected dense_vector", ErrFingerprintMismatch, name, fi.FieldType)
			}
		}
		// Qdrant: FieldType ignored (named vectors don't have type field)

		// Dimensions — strict integer check
		dims, err := strictDims(fi.Dims)
		if err != nil {
			return fmt.Errorf("%w: field %q dims: %v", ErrFingerprintMismatch, name, err)
		}
		if dims != fp.Dimensions {
			return fmt.Errorf("%w: field %q dims %d, expected %d", ErrFingerprintMismatch, name, dims, fp.Dimensions)
		}

		// Distance/similarity mapping — REQUIRED (no empty bypass)
		if fi.Distance == "" {
			return fmt.Errorf("%w: field %q has no distance/similarity info", ErrFingerprintMismatch, name)
		}
		if !distanceMatchesSimilarity(fi.Distance, fp.Similarity) {
			return fmt.Errorf("%w: field %q distance %q doesn't match similarity %q",
				ErrFingerprintMismatch, name, fi.Distance, fp.Similarity)
		}
	}

	return nil
}

// strictDims converts a raw dimension value to int, rejecting non-integer types.
// No int(float64) truncation — float64 values are rejected outright.
func strictDims(v interface{}) (int, error) {
	if v == nil {
		return 0, fmt.Errorf("dims is nil")
	}
	switch d := v.(type) {
	case int64:
		if d <= 0 {
			return 0, fmt.Errorf("dims %d <= 0", d)
		}
		return int(d), nil
	case int:
		if d <= 0 {
			return 0, fmt.Errorf("dims %d <= 0", d)
		}
		return d, nil
	case float64:
		// Reject — must be integer, not float (no int(float64) truncation)
		return 0, fmt.Errorf("dims is float64 (%v), must be integer", d)
	case string:
		return 0, fmt.Errorf("dims is string (%q), must be integer", d)
	case json.Number:
		n, err := d.Int64()
		if err != nil {
			return 0, fmt.Errorf("dims %q is not integer: %v", d.String(), err)
		}
		if n <= 0 {
			return 0, fmt.Errorf("dims %d <= 0", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("dims has unsupported type %T", v)
	}
}

// distanceMatchesSimilarity checks if a backend-specific distance string matches
// the fingerprint's similarity metric.
// ES distances: "cosine", "dot_product", "l2_norm"
// Qdrant distances: "Cosine", "Dot", "Euclid", "Manhattan"
// Fingerprint similarities: "cosine", "dot", "euclidean"
// Unknown distance values → false (reject).
func distanceMatchesSimilarity(distance, similarity string) bool {
	switch distance {
	// ES names
	case "cosine":
		return similarity == "cosine"
	case "dot_product":
		return similarity == "dot"
	case "l2_norm":
		return similarity == "euclidean"
	// Qdrant names
	case "Cosine":
		return similarity == "cosine"
	case "Dot":
		return similarity == "dot"
	case "Euclid":
		return similarity == "euclidean"
	case "Manhattan":
		// Manhattan doesn't match any currently supported similarity metric
		return false
	default:
		// Unknown distance → reject
		return false
	}
}
