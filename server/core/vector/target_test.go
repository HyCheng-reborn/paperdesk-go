package vector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPhysicalTargetRejectsExpressions(t *testing.T) {
	for _, name := range []string{"", "a,b", "a*", "a?", "_all", "cluster:idx", "<idx-{now/d}>", "a/b", "a b", ".", "..", "-x"} {
		if !errors.Is(ValidatePhysicalName(name), ErrPhysicalTargetRequired) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"idx", "idx-20261025-abc123", "a.b_c"} {
		if err := ValidatePhysicalName(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPhysicalTargetESRejectsAliasAndMultipleResults(t *testing.T) {
	for _, names := range [][]string{{"physical-a"}, {"idx", "physical-b"}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			body := map[string]any{}
			for _, name := range names {
				body[name] = map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(body)
		}))
		store, err := NewESVectorStore(&ESConfig{Address: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.ReadFingerprint(context.Background(), "idx")
		srv.Close()
		if !errors.Is(err, ErrPhysicalTargetRequired) {
			t.Fatal(err)
		}
	}
}

func TestPhysicalTargetBindingCapturesIdentityAndPropagatesCancellation(t *testing.T) {
	fake := newFakeEServer()
	store, server := newTestESVectorStore(t, fake)
	defer server.Close()
	fp := testFingerprint()
	if err := store.CreateIndex(context.Background(), "idx", fp); err != nil {
		t.Fatal(err)
	}
	guard, err := BindPhysicalTarget(context.Background(), store, "idx", fp)
	if err != nil {
		t.Fatal(err)
	}
	fp.EmbeddingModelID = "caller-mutated-config"
	if err := guard(context.Background()); err != nil {
		t.Fatalf("guard captured mutable fingerprint: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.indices["idx"].uuid = "replacement"
	fake.mu.Unlock()
	if err := guard(context.Background()); !errors.Is(err, ErrResourceReplaced) {
		t.Fatal(err)
	}
}
