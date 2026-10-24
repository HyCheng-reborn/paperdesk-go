package vector

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewFingerprint(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		modelID    string
		dims       int
		provider   string
		similarity string
		wantErr    bool
	}{
		{
			name:       "valid fingerprint",
			version:    "v1",
			modelID:    "text-embedding-v4",
			dims:       1024,
			provider:   "bailian",
			similarity: "cosine",
			wantErr:    false,
		},
		{
			name:       "empty version",
			version:    "",
			modelID:    "text-embedding-v4",
			dims:       1024,
			provider:   "bailian",
			similarity: "cosine",
			wantErr:    true,
		},
		{
			name:       "empty modelID",
			version:    "v1",
			modelID:    "",
			dims:       1024,
			provider:   "bailian",
			similarity: "cosine",
			wantErr:    true,
		},
		{
			name:       "zero dimensions",
			version:    "v1",
			modelID:    "text-embedding-v4",
			dims:       0,
			provider:   "bailian",
			similarity: "cosine",
			wantErr:    true,
		},
		{
			name:       "negative dimensions",
			version:    "v1",
			modelID:    "text-embedding-v4",
			dims:       -1,
			provider:   "bailian",
			similarity: "cosine",
			wantErr:    true,
		},
		{
			name:       "empty provider",
			version:    "v1",
			modelID:    "text-embedding-v4",
			dims:       1024,
			provider:   "",
			similarity: "cosine",
			wantErr:    true,
		},
		{
			name:       "empty similarity",
			version:    "v1",
			modelID:    "text-embedding-v4",
			dims:       1024,
			provider:   "bailian",
			similarity: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fp, err := NewFingerprint(tt.version, tt.modelID, tt.dims, tt.provider, tt.similarity)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewFingerprint() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if fp == nil {
					t.Error("NewFingerprint() returned nil fingerprint")
					return
				}
				if fp.Version != tt.version {
					t.Errorf("Version = %q, want %q", fp.Version, tt.version)
				}
				if fp.EmbeddingModelID != tt.modelID {
					t.Errorf("EmbeddingModelID = %q, want %q", fp.EmbeddingModelID, tt.modelID)
				}
				if fp.Dimensions != tt.dims {
					t.Errorf("Dimensions = %d, want %d", fp.Dimensions, tt.dims)
				}
				// Provider and similarity should be lowercased
				if fp.ProviderName != strings.ToLower(tt.provider) {
					t.Errorf("ProviderName = %q, want %q", fp.ProviderName, strings.ToLower(tt.provider))
				}
				if fp.Similarity != strings.ToLower(tt.similarity) {
					t.Errorf("Similarity = %q, want %q", fp.Similarity, strings.ToLower(tt.similarity))
				}
			}
		})
	}
}

func TestFingerprint_Equal(t *testing.T) {
	fp1 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp2 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp3 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       768, // Different
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp4 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	if !fp1.Equal(fp2) {
		t.Error("Equal fingerprints should be equal")
	}
	if fp1.Equal(fp3) {
		t.Error("Different dimensions should not be equal")
	}
	if fp1.Equal(fp4) {
		t.Error("Different model IDs should not be equal")
	}
	if fp1.Equal(nil) {
		t.Error("Non-nil should not equal nil")
	}
	if !(*Fingerprint)(nil).Equal(nil) {
		t.Error("Nil should equal nil")
	}
}

func TestFingerprint_Valid(t *testing.T) {
	tests := []struct {
		name  string
		fp    *Fingerprint
		valid bool
	}{
		{
			name: "valid fingerprint",
			fp: &Fingerprint{
				Version:          "v1",
				EmbeddingModelID: "text-embedding-v4",
				Dimensions:       1024,
				ProviderName:     "bailian",
				Similarity:       "cosine",
			},
			valid: true,
		},
		{
			name:  "nil fingerprint",
			fp:    nil,
			valid: false,
		},
		{
			name: "empty version",
			fp: &Fingerprint{
				Version:          "",
				EmbeddingModelID: "text-embedding-v4",
				Dimensions:       1024,
				ProviderName:     "bailian",
				Similarity:       "cosine",
			},
			valid: false,
		},
		{
			name: "unknown version",
			fp: &Fingerprint{
				Version:          "v99",
				EmbeddingModelID: "text-embedding-v4",
				Dimensions:       1024,
				ProviderName:     "bailian",
				Similarity:       "cosine",
			},
			valid: false,
		},
		{
			name: "zero dimensions",
			fp: &Fingerprint{
				Version:          "v1",
				EmbeddingModelID: "text-embedding-v4",
				Dimensions:       0,
				ProviderName:     "bailian",
				Similarity:       "cosine",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fp.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestFingerprint_CanonicalJSON(t *testing.T) {
	fp := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	canonical, err := fp.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}

	// Verify it's valid JSON
	var m map[string]any
	if err := json.Unmarshal(canonical, &m); err != nil {
		t.Fatalf("CanonicalJSON() produced invalid JSON: %v", err)
	}

	// Verify no whitespace (compact)
	if strings.Contains(string(canonical), " ") {
		t.Error("CanonicalJSON() should not contain whitespace")
	}

	// Verify keys are sorted (check by comparing with expected)
	expected := `{"dimensions":1024,"embedding_model_id":"text-embedding-v4","provider_name":"bailian","similarity":"cosine","version":"v1"}`
	if string(canonical) != expected {
		t.Errorf("CanonicalJSON() = %q, want %q", string(canonical), expected)
	}
}

func TestFingerprint_Digest(t *testing.T) {
	fp1 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp2 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp3 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	digest1 := fp1.Digest()
	digest2 := fp2.Digest()
	digest3 := fp3.Digest()

	// Same fingerprints should have same digest
	if digest1 != digest2 {
		t.Errorf("Same fingerprints should have same digest: %q != %q", digest1, digest2)
	}

	// Different fingerprints should have different digest
	if digest1 == digest3 {
		t.Error("Different fingerprints should have different digest")
	}

	// Digest should start with "sha256:"
	if !strings.HasPrefix(digest1, "sha256:") {
		t.Errorf("Digest should start with 'sha256:', got %q", digest1)
	}

	// Nil fingerprint should return empty digest
	if (*Fingerprint)(nil).Digest() != "" {
		t.Error("Nil fingerprint digest should be empty")
	}
}

func TestFingerprint_MarshalJSON(t *testing.T) {
	fp := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	data, err := fp.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}

	// Verify it's valid JSON
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("MarshalJSON() produced invalid JSON: %v", err)
	}

	// Verify digest is included
	if _, ok := m["digest"]; !ok {
		t.Error("MarshalJSON() should include digest field")
	}

	// Verify all fields are present
	for _, field := range []string{"version", "embedding_model_id", "dimensions", "provider_name", "similarity", "digest"} {
		if _, ok := m[field]; !ok {
			t.Errorf("MarshalJSON() missing field %q", field)
		}
	}
}

func TestParseFingerprint(t *testing.T) {
	original := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	// Marshal and parse round-trip
	data, err := original.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}

	parsed, err := ParseFingerprint(data)
	if err != nil {
		t.Fatalf("ParseFingerprint() error = %v", err)
	}

	if !original.Equal(parsed) {
		t.Errorf("Round-trip failed: original=%+v, parsed=%+v", original, parsed)
	}

	// Test empty data
	if _, err := ParseFingerprint([]byte{}); err != ErrFingerprintMissing {
		t.Errorf("ParseFingerprint(empty) should return ErrFingerprintMissing, got %v", err)
	}

	// Test invalid JSON
	if _, err := ParseFingerprint([]byte("invalid")); err == nil {
		t.Error("ParseFingerprint(invalid) should return error")
	}

	// Test unknown version
	unknownVersion := `{"version":"v99","embedding_model_id":"test","dimensions":1024,"provider_name":"test","similarity":"cosine"}`
	if _, err := ParseFingerprint([]byte(unknownVersion)); err == nil {
		t.Error("ParseFingerprint(unknown version) should return error")
	}
}

func TestVerifyFingerprint(t *testing.T) {
	fp1 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp2 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	fp3 := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "different-model",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}

	// Matching fingerprints
	if err := VerifyFingerprint(fp1, fp2); err != nil {
		t.Errorf("VerifyFingerprint(matching) error = %v", err)
	}

	// Mismatched fingerprints
	if err := VerifyFingerprint(fp1, fp3); err == nil {
		t.Error("VerifyFingerprint(mismatched) should return error")
	} else if !strings.Contains(err.Error(), FINGERPRINT_MISMATCH) {
		t.Errorf("VerifyFingerprint(mismatched) should return FINGERPRINT_MISMATCH error, got %v", err)
	}

	// Nil stored
	if err := VerifyFingerprint(nil, fp1); err == nil {
		t.Error("VerifyFingerprint(nil stored) should return error")
	}

	// Nil expected
	if err := VerifyFingerprint(fp1, nil); err == nil {
		t.Error("VerifyFingerprint(nil expected) should return error")
	}

	// Invalid stored fingerprint
	invalid := &Fingerprint{Version: "v99"}
	if err := VerifyFingerprint(invalid, fp1); err == nil {
		t.Error("VerifyFingerprint(invalid stored) should return error")
	}
}

// --- F7: Strict parsing tests ---

func TestParseFingerprint_RejectsFloatDimensions(t *testing.T) {
	// dimensions as float (1024.9) must be rejected, not silently truncated
	data := `{"version":"v1","embedding_model_id":"test","dimensions":1024.9,"provider_name":"test","similarity":"cosine","digest":"sha256:abc"}`
	_, err := ParseFingerprint([]byte(data))
	if err == nil {
		t.Fatal("ParseFingerprint should reject float dimensions")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got %v", err)
	}
}

func TestParseFingerprint_RejectsBlankProvider(t *testing.T) {
	// empty provider_name
	data := `{"version":"v1","embedding_model_id":"test","dimensions":1024,"provider_name":"","similarity":"cosine","digest":"sha256:abc"}`
	_, err := ParseFingerprint([]byte(data))
	if err == nil {
		t.Fatal("ParseFingerprint should reject blank provider")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got %v", err)
	}

	// whitespace-only provider_name
	data2 := `{"version":"v1","embedding_model_id":"test","dimensions":1024,"provider_name":"   ","similarity":"cosine","digest":"sha256:abc"}`
	_, err2 := ParseFingerprint([]byte(data2))
	if err2 == nil {
		t.Fatal("ParseFingerprint should reject whitespace-only provider")
	}
	if !errors.Is(err2, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted for whitespace provider, got %v", err2)
	}
}

func TestParseFingerprint_RejectsBlankSimilarity(t *testing.T) {
	data := `{"version":"v1","embedding_model_id":"test","dimensions":1024,"provider_name":"test","similarity":"","digest":"sha256:abc"}`
	_, err := ParseFingerprint([]byte(data))
	if err == nil {
		t.Fatal("ParseFingerprint should reject blank similarity")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got %v", err)
	}
}

func TestParseFingerprint_RejectsUnknownVersion(t *testing.T) {
	data := `{"version":"v2","embedding_model_id":"test","dimensions":1024,"provider_name":"test","similarity":"cosine","digest":"sha256:abc"}`
	_, err := ParseFingerprint([]byte(data))
	if err == nil {
		t.Fatal("ParseFingerprint should reject unknown version")
	}
	if !errors.Is(err, ErrFingerprintUnknownVersion) {
		t.Errorf("expected ErrFingerprintUnknownVersion, got %v", err)
	}
}

func TestVerifyDigest_Match(t *testing.T) {
	fp := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	digest := fp.Digest()
	if err := VerifyDigest(fp, digest); err != nil {
		t.Errorf("VerifyDigest should succeed for matching digest, got: %v", err)
	}
}

func TestVerifyDigest_Mismatch(t *testing.T) {
	fp := &Fingerprint{
		Version:          "v1",
		EmbeddingModelID: "text-embedding-v4",
		Dimensions:       1024,
		ProviderName:     "bailian",
		Similarity:       "cosine",
	}
	err := VerifyDigest(fp, "sha256:tampered-digest-value-000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("VerifyDigest should fail for mismatched digest")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got %v", err)
	}
}

func TestVerifyDigest_NilFingerprint(t *testing.T) {
	err := VerifyDigest(nil, "sha256:abc")
	if err == nil {
		t.Fatal("VerifyDigest should fail for nil fingerprint")
	}
	if !errors.Is(err, ErrFingerprintCorrupted) {
		t.Errorf("expected ErrFingerprintCorrupted, got %v", err)
	}
}

func TestNewFingerprint_RejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		modelID    string
		dims       int
		provider   string
		similarity string
		wantErr    string
	}{
		{"zero dims", "v1", "model", 0, "provider", "cosine", "dimensions must be positive"},
		{"negative dims", "v1", "model", -5, "provider", "cosine", "dimensions must be positive"},
		{"empty model", "v1", "", 1024, "provider", "cosine", "embedding_model_id must not be empty"},
		{"empty provider", "v1", "model", 1024, "", "cosine", "provider_name must not be empty"},
		{"whitespace provider", "v1", "model", 1024, "   ", "cosine", "provider_name must not be empty"},
		{"empty similarity", "v1", "model", 1024, "provider", "", "similarity must not be empty"},
		{"invalid similarity", "v1", "model", 1024, "provider", "hamming", "not in"},
		{"unknown version", "v2", "model", 1024, "provider", "cosine", "unknown version"},
		{"empty version", "", "model", 1024, "provider", "cosine", "version must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewFingerprint(tt.version, tt.modelID, tt.dims, tt.provider, tt.similarity)
			if err == nil {
				t.Fatalf("NewFingerprint(%s) should fail", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("NewFingerprint(%s) error = %v, want to contain %q", tt.name, err, tt.wantErr)
			}
		})
	}
}
