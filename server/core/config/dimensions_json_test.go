package config

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

func TestDimensions_JSONNumber(t *testing.T) {
	max := uint64(^uint(0) >> 1)
	for _, n := range []string{"768", strconv.FormatUint(max, 10)} {
		got, err := ParseEmbeddingDimensions(json.Number(n))
		want, _ := strconv.ParseInt(n, 10, strconv.IntSize)
		if err != nil || got != int(want) {
			t.Errorf("json.Number(%q): got %d, %v", n, got, err)
		}
	}
	for _, n := range []string{"", "0", "-1", "+768", "0768", " 768", "768 ", "768.0", "1.5", "1e3", "NaN", "Inf", strconv.FormatUint(max+1, 10), "999999999999999999999999999"} {
		t.Run(n, func(t *testing.T) {
			if _, err := ParseEmbeddingDimensions(json.Number(n)); !errors.Is(err, ErrEmbeddingDimensions) {
				t.Fatalf("accepted invalid json.Number %q: %v", n, err)
			}
		})
	}
	if _, err := ParseEmbeddingDimensions("768"); !errors.Is(err, ErrEmbeddingDimensions) {
		t.Fatal("ordinary string accepted", err)
	}
}
