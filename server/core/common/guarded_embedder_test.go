package common

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/wangle201210/go-rag/server/core/config"
)

// fakeEmbedder implements embedding.Embedder for testing guardedEmbedder.
type fakeEmbedder struct {
	vectors [][]float64
	err     error
}

func (f *fakeEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vectors, nil
}

func (f *fakeEmbedder) Strings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	return f.EmbedStrings(ctx, texts, opts...)
}

// TestGuardedEmbedder_GuardBeforeEmbedding verifies that a guard failure before
// embedding prevents the embedder from being called.
func TestGuardedEmbedder_GuardBeforeEmbedding(t *testing.T) {
	guardCalled := false
	guard := func(ctx context.Context) error {
		guardCalled = true
		return fmt.Errorf("vector target not ready")
	}
	embedder := &fakeEmbedder{}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3, guard: guard}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected guard error")
	}
	if !guardCalled {
		t.Fatal("guard should have been called")
	}
}

// TestGuardedEmbedder_GuardAfterEmbedding verifies that a guard failure after
// embedding discards vectors and returns error.
func TestGuardedEmbedder_GuardAfterEmbedding(t *testing.T) {
	callCount := 0
	guard := func(ctx context.Context) error {
		callCount++
		if callCount == 2 {
			return fmt.Errorf("vector target lost after embedding")
		}
		return nil
	}
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2, 0.3}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3, guard: guard}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected guard-after error")
	}
	if callCount != 2 {
		t.Fatalf("guard should have been called twice, got %d", callCount)
	}
}

// TestGuardedEmbedder_ResponseCountMismatch verifies that a response with wrong
// count is rejected.
func TestGuardedEmbedder_ResponseCountMismatch(t *testing.T) {
	// 2 vectors returned for 1 input
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected count mismatch error")
	}
	if !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Errorf("expected ErrEmbeddingDimensions, got: %v", err)
	}
}

// TestGuardedEmbedder_ResponseDimensionMismatch verifies that a response with
// wrong dimensions is rejected.
func TestGuardedEmbedder_ResponseDimensionMismatch(t *testing.T) {
	// 2-dim vector returned when 3 expected
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Errorf("expected ErrEmbeddingDimensions, got: %v", err)
	}
}

// TestGuardedEmbedder_NaNRejected verifies NaN values in response are rejected.
func TestGuardedEmbedder_NaNRejected(t *testing.T) {
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, math.NaN(), 0.3}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected NaN error")
	}
	if !errors.Is(err, config.ErrEmbeddingDimensions) {
		t.Errorf("expected ErrEmbeddingDimensions, got: %v", err)
	}
}

// TestGuardedEmbedder_InfRejected verifies Inf values in response are rejected.
func TestGuardedEmbedder_InfRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  float64
	}{
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			embedder := &fakeEmbedder{vectors: [][]float64{{0.1, tc.val, 0.3}}}
			gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

			_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
			if err == nil {
				t.Fatal("expected Inf error")
			}
			if !errors.Is(err, config.ErrEmbeddingDimensions) {
				t.Errorf("expected ErrEmbeddingDimensions, got: %v", err)
			}
		})
	}
}

// TestGuardedEmbedder_FiniteSuccess verifies valid finite vectors pass all checks.
func TestGuardedEmbedder_FiniteSuccess(t *testing.T) {
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2, 0.3}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	result, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 || len(result[0]) != 3 {
		t.Fatalf("unexpected result: %v", result)
	}
}

// TestGuardedEmbedder_NilGuard verifies that nil guard allows embedding without guard checks.
func TestGuardedEmbedder_NilGuard(t *testing.T) {
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2, 0.3}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3, guard: nil}

	result, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 vector, got %d", len(result))
	}
}

// TestGuardedEmbedder_EmbedderError verifies that embedder errors propagate.
func TestGuardedEmbedder_EmbedderError(t *testing.T) {
	embedder := &fakeEmbedder{err: fmt.Errorf("model unavailable")}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected embedder error")
	}
	if err.Error() != "model unavailable" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGuardedEmbedder_MultipleTexts verifies batch embedding validation.
func TestGuardedEmbedder_MultipleTexts(t *testing.T) {
	embedder := &fakeEmbedder{vectors: [][]float64{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
	}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3}

	result, err := gb.EmbedStrings(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(result))
	}
}

// TestGuardedEmbedder_GuardIdentityFlipAfterEmbedding verifies that if the guard
// flips (identity change) after embedding, vectors are discarded.
func TestGuardedEmbedder_GuardIdentityFlipAfterEmbedding(t *testing.T) {
	guardCalls := 0
	guard := func(ctx context.Context) error {
		guardCalls++
		if guardCalls >= 2 {
			return fmt.Errorf("identity changed during embedding")
		}
		return nil
	}
	embedder := &fakeEmbedder{vectors: [][]float64{{0.1, 0.2, 0.3}}}
	gb := &guardedEmbedder{Embedder: embedder, dimensions: 3, guard: guard}

	_, err := gb.EmbedStrings(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected identity flip error after embedding")
	}
	// Vectors must NOT be returned
	if guardCalls != 2 {
		t.Fatalf("expected 2 guard calls, got %d", guardCalls)
	}
}
