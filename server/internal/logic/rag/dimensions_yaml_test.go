package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gcfg"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
)

// No t.Parallel: readRagConfig uses the production default GoFrame instance.
// Restore its adapter, never replace production parsing functions or use fake getters.
func dimensionsYAMLFile(t *testing.T, legacy, provider string) {
	t.Helper()
	// Build a single embedding mapping: duplicate YAML keys must not decide precedence.
	content := "vector:\n  type: es\n  indexName: offline\nembedding:\n  model: text-embedding-3-large\n"
	if legacy != "" {
		content += "  dimensions: " + legacy + "\n"
	}
	content += "chat:\n  model: offline\n"
	if provider != "" {
		content += "provider:\n  name: openai\n  embedding_model: offline\n  embedding_dimensions: " + provider + "\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := gcfg.NewAdapterFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := g.Cfg().GetAdapter()
	g.Cfg().SetAdapter(a)
	t.Cleanup(func() { g.Cfg().SetAdapter(old) })
}
func TestDimensions_YAMLFileProductionChain(t *testing.T) {
	for _, c := range []struct {
		name, legacy, provider string
		want                   int
	}{{"missing", "", "", 1024}, {"legacy", "768", "", 768}, {"provider", "", "768", 768}, {"agree", "768", "768", 768}, {"explicitDefault", "1024", "", 1024}} {
		t.Run(c.name, func(t *testing.T) {
			dimensionsYAMLFile(t, c.legacy, c.provider)
			key := "embedding.dimensions"
			if c.provider != "" {
				key = "provider.embedding_dimensions"
			}
			if c.legacy != "" || c.provider != "" {
				v, err := g.Cfg().Get(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := v.Val().(json.Number); !ok {
					t.Fatalf("fixture did not exercise GoFrame json.Number: %T", v.Val())
				}
			}
			raw, err := readRagConfig()
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := normalizeDimensions(raw)
			if err != nil {
				t.Fatal(err)
			}
			if normalized.EmbeddingDimensions != c.want || normalized.VectorCfg.EmbeddingDimensions != c.want {
				t.Fatalf("not propagated: %+v", normalized)
			}
			if c.provider != "" {
				resetState()
				t.Cleanup(resetState)
				if normalized.Provider.EmbeddingDimensions != c.want {
					t.Fatal(normalized.Provider)
				}
				vf := &dimensionVSFactory{vs: &fakeVectorStore{}}
				rf := &dimensionRagFactory{}
				if err = InitWith(raw, vf, rf); !errors.Is(err, common.ErrEmbeddingGuardNotReady) {
					t.Fatal("provider gate bypassed", err)
				}
				if vf.got != 0 || rf.got != 0 {
					t.Fatal("gate contacted factories")
				}
			}
		})
	}
}
func TestDimensions_YAMLFileInvalidAndConflict(t *testing.T) {
	for _, key := range []string{"legacy", "provider"} {
		for _, value := range []string{"0", "-1", `"768"`, "1.5", "true", "9223372036854775808"} {
			t.Run(fmt.Sprintf("%s/%s", key, value), func(t *testing.T) {
				legacy, provider := "", ""
				if key == "legacy" {
					legacy = value
				} else {
					provider = value
				}
				dimensionsYAMLFile(t, legacy, provider)
				_, err := readRagConfig()
				if !errors.Is(err, config.ErrEmbeddingDimensions) {
					t.Fatalf("wanted dimension rejection, got %v", err)
				}
			})
		}
	}
	t.Run("conflict", func(t *testing.T) {
		dimensionsYAMLFile(t, "512", "768")
		raw, err := readRagConfig()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = normalizeDimensions(raw); !errors.Is(err, config.ErrEmbeddingDimensionConflict) {
			t.Fatal(err)
		}
	})
}
