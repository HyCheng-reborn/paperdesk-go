package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
)

// DefaultEmbeddingDimensions is the legacy configuration default, not a schema default.
const DefaultEmbeddingDimensions = 1024

var ErrEmbeddingDimensions = errors.New("invalid embedding dimensions")
var ErrEmbeddingDimensionConflict = errors.New("conflicting embedding dimensions")

// ParseEmbeddingDimensions distinguishes an absent key from an explicit invalid value.
// Configuration strings, floats, booleans, zero and negatives are not dimensions.
func ParseEmbeddingDimensions(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	// GoFrame's YAML/file adapter preserves numeric values as json.Number.
	// Keep ordinary strings invalid and do not coerce decimal/exponent forms.
	if number, ok := value.(json.Number); ok {
		text := string(number)
		if len(text) == 0 || text[0] < '1' || text[0] > '9' {
			return 0, fmt.Errorf("%w: expected positive integer json.Number", ErrEmbeddingDimensions)
		}
		for i := 1; i < len(text); i++ {
			if text[i] < '0' || text[i] > '9' {
				return 0, fmt.Errorf("%w: expected integer json.Number", ErrEmbeddingDimensions)
			}
		}
		n, err := strconv.ParseInt(text, 10, strconv.IntSize)
		if err != nil {
			return 0, fmt.Errorf("%w: json.Number outside int range", ErrEmbeddingDimensions)
		}
		return int(n), nil
	}
	v := reflect.ValueOf(value)
	var n int64
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n = v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() > uint64(^uint(0)>>1) {
			return 0, ErrEmbeddingDimensions
		}
		n = int64(v.Uint())
	default:
		return 0, fmt.Errorf("%w: must be a positive integer, got %T", ErrEmbeddingDimensions, value)
	}
	if n <= 0 || uint64(n) > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: %v", ErrEmbeddingDimensions, value)
	}
	return int(n), nil
}

// ResolveEmbeddingDimensions is the single configuration boundary. In Go structs
// zero means unspecified. An active Provider declaration takes precedence, but
// conflicting explicit legacy dimensions are rejected rather than overwritten.
func ResolveEmbeddingDimensions(legacy int, provider *ProviderConfig) (int, error) {
	if legacy < 0 {
		return 0, ErrEmbeddingDimensions
	}
	declared := 0
	if provider != nil {
		if provider.EmbeddingDimensions > 0 && provider.EmbeddingModel == "" {
			return 0, fmt.Errorf("%w: provider dimensions without embedding model", ErrEmbeddingDimensions)
		}
		if provider.EmbeddingDimensions < 0 {
			return 0, ErrEmbeddingDimensions
		}
		if provider.EmbeddingModel != "" {
			declared = provider.EmbeddingDimensions
		}
	}
	if declared > 0 {
		if legacy > 0 && legacy != declared {
			return 0, fmt.Errorf("%w: embedding=%d provider=%d", ErrEmbeddingDimensionConflict, legacy, declared)
		}
		return declared, nil
	}
	if legacy > 0 {
		return legacy, nil
	}
	// A new model must declare its size or have an explicit fallback. Do not
	// assign the old model's default to an unknown Provider model.
	if provider != nil && provider.EmbeddingModel != "" {
		return 0, fmt.Errorf("%w: provider embedding requires a dimension declaration", ErrEmbeddingDimensions)
	}
	return DefaultEmbeddingDimensions, nil
}

func (c *Config) ResolveDimensions() (int, error) {
	if c == nil {
		return 0, ErrEmbeddingDimensions
	}
	return ResolveEmbeddingDimensions(c.EmbeddingDimensions, c.Provider)
}
