package vector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Fingerprint error codes (for programmatic consumption).
const (
	FINGERPRINT_MISSING   = "FINGERPRINT_MISSING"
	FINGERPRINT_MISMATCH  = "FINGERPRINT_MISMATCH"
	FINGERPRINT_CORRUPTED = "FINGERPRINT_CORRUPTED"
)

// Fingerprint errors.
var (
	ErrFingerprintMissing        = errors.New(FINGERPRINT_MISSING)
	ErrFingerprintMismatch       = errors.New(FINGERPRINT_MISMATCH)
	ErrFingerprintCorrupted      = errors.New(FINGERPRINT_CORRUPTED)
	ErrFingerprintUnknownVersion = errors.New("fingerprint: unknown version")
)

// FingerprintVersion is the current fingerprint format version.
const FingerprintVersion = "v1"

// Fingerprint records the vector space identity of an index.
// All fields must be non-empty for a valid fingerprint.
type Fingerprint struct {
	Version          string `json:"version"`
	EmbeddingModelID string `json:"embedding_model_id"`
	Dimensions       int    `json:"dimensions"`
	ProviderName     string `json:"provider_name"`
	Similarity       string `json:"similarity"`
}

// NewFingerprint creates a validated Fingerprint.
// Empty fields cause an error. ProviderName and Similarity are lowercased.
func NewFingerprint(version, modelID string, dims int, provider, similarity string) (*Fingerprint, error) {
	if version == "" {
		return nil, fmt.Errorf("fingerprint: version must not be empty")
	}
	if modelID == "" {
		return nil, fmt.Errorf("fingerprint: embedding_model_id must not be empty")
	}
	if dims <= 0 {
		return nil, fmt.Errorf("fingerprint: dimensions must be positive")
	}
	if provider == "" {
		return nil, fmt.Errorf("fingerprint: provider_name must not be empty")
	}
	if similarity == "" {
		return nil, fmt.Errorf("fingerprint: similarity must not be empty")
	}
	return &Fingerprint{
		Version:          version,
		EmbeddingModelID: modelID,
		Dimensions:       dims,
		ProviderName:     strings.ToLower(strings.TrimSpace(provider)),
		Similarity:       strings.ToLower(strings.TrimSpace(similarity)),
	}, nil
}

// Equal returns true if all fields match exactly.
func (f *Fingerprint) Equal(other *Fingerprint) bool {
	if f == nil || other == nil {
		return f == other
	}
	return f.Version == other.Version &&
		f.EmbeddingModelID == other.EmbeddingModelID &&
		f.Dimensions == other.Dimensions &&
		f.ProviderName == other.ProviderName &&
		f.Similarity == other.Similarity
}

// Valid returns true if all required fields are non-empty and version is known.
func (f *Fingerprint) Valid() bool {
	if f == nil {
		return false
	}
	if f.Version == "" || f.EmbeddingModelID == "" || f.Dimensions <= 0 ||
		f.ProviderName == "" || f.Similarity == "" {
		return false
	}
	if f.Version != FingerprintVersion {
		return false
	}
	return true
}

// CanonicalJSON returns the JSON encoding with sorted keys and no whitespace.
// This is the canonical form used for digest computation.
func (f *Fingerprint) CanonicalJSON() ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("fingerprint: nil receiver")
	}
	// Use map to ensure sorted keys (json.Marshal sorts map keys)
	m := map[string]any{
		"version":            f.Version,
		"embedding_model_id": f.EmbeddingModelID,
		"dimensions":         f.Dimensions,
		"provider_name":      f.ProviderName,
		"similarity":         f.Similarity,
	}
	// json.Marshal sorts map keys alphabetically
	return json.Marshal(m)
}

// Digest returns "sha256:" + hex(SHA-256(canonical JSON)).
func (f *Fingerprint) Digest() string {
	if f == nil {
		return ""
	}
	canonical, err := f.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MarshalJSON implements json.Marshaler with digest included.
func (f Fingerprint) MarshalJSON() ([]byte, error) {
	// Build a map with all fields plus digest
	m := map[string]any{
		"version":            f.Version,
		"embedding_model_id": f.EmbeddingModelID,
		"dimensions":         f.Dimensions,
		"provider_name":      f.ProviderName,
		"similarity":         f.Similarity,
		"digest":             f.Digest(),
	}
	// Sort keys for deterministic output
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, _ := json.Marshal(m[k])
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return []byte(buf.String()), nil
}

// ParseFingerprint parses a stored fingerprint JSON.
// It validates the version and required fields.
func ParseFingerprint(data []byte) (*Fingerprint, error) {
	if len(data) == 0 {
		return nil, ErrFingerprintMissing
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFingerprintCorrupted, err)
	}
	fp := &Fingerprint{}
	if v, ok := raw["version"].(string); ok {
		fp.Version = v
	}
	if v, ok := raw["embedding_model_id"].(string); ok {
		fp.EmbeddingModelID = v
	}
	if v, ok := raw["provider_name"].(string); ok {
		fp.ProviderName = v
	}
	if v, ok := raw["similarity"].(string); ok {
		fp.Similarity = v
	}
	if v, ok := raw["dimensions"].(float64); ok {
		fp.Dimensions = int(v)
	}
	// Validate
	if !fp.Valid() {
		if fp.Version != "" && fp.Version != FingerprintVersion {
			return nil, fmt.Errorf("%w: %q", ErrFingerprintUnknownVersion, fp.Version)
		}
		return nil, ErrFingerprintCorrupted
	}
	return fp, nil
}

// VerifyFingerprint compares stored vs expected fingerprint.
// Returns nil on match, typed error on mismatch/missing/corrupted/unknown version.
func VerifyFingerprint(stored, expected *Fingerprint) error {
	if stored == nil {
		return fmt.Errorf("%w: stored fingerprint is nil", ErrFingerprintMissing)
	}
	if expected == nil {
		return fmt.Errorf("%w: expected fingerprint is nil", ErrFingerprintMissing)
	}
	if !stored.Valid() {
		if stored.Version != "" && stored.Version != FingerprintVersion {
			return fmt.Errorf("%w: stored version %q", ErrFingerprintUnknownVersion, stored.Version)
		}
		return fmt.Errorf("%w: stored fingerprint invalid", ErrFingerprintCorrupted)
	}
	if !expected.Valid() {
		return fmt.Errorf("expected fingerprint invalid: version=%q model=%q dims=%d provider=%q similarity=%q",
			expected.Version, expected.EmbeddingModelID, expected.Dimensions,
			expected.ProviderName, expected.Similarity)
	}
	if !stored.Equal(expected) {
		return fmt.Errorf("%w: stored={version:%q model:%q dims:%d provider:%q similarity:%q} expected={version:%q model:%q dims:%d provider:%q similarity:%q}",
			ErrFingerprintMismatch,
			stored.Version, stored.EmbeddingModelID, stored.Dimensions, stored.ProviderName, stored.Similarity,
			expected.Version, expected.EmbeddingModelID, expected.Dimensions, expected.ProviderName, expected.Similarity)
	}
	return nil
}
