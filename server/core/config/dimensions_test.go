package config

import (
	"errors"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"testing"
)

func TestDimensions_Resolution(t *testing.T) {
	cases := []struct {
		name   string
		legacy int
		p      *ProviderConfig
		want   int
		err    error
	}{
		{"default", 0, nil, 1024, nil}, {"legacy", 768, nil, 768, nil}, {"provider", 0, &ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}, 768, nil}, {"fallback", 768, &ProviderConfig{EmbeddingModel: "m"}, 768, nil}, {"agree", 768, &ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}, 768, nil}, {"conflict", 512, &ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}, 0, ErrEmbeddingDimensionConflict}, {"negative", -1, nil, 0, ErrEmbeddingDimensions}, {"negativeProvider", 0, &ProviderConfig{EmbeddingDimensions: -1}, 0, ErrEmbeddingDimensions}, {"missingProvider", 0, &ProviderConfig{EmbeddingModel: "m"}, 0, ErrEmbeddingDimensions}, {"orphanProviderDimensions", 0, &ProviderConfig{EmbeddingDimensions: 768}, 0, ErrEmbeddingDimensions}, {"chatOnly", 0, &ProviderConfig{ChatModel: "chat"}, 1024, nil}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, e := ResolveEmbeddingDimensions(c.legacy, c.p)
			if d != c.want || !errors.Is(e, c.err) {
				t.Fatalf("got %d %v", d, e)
			}
		})
	}
}
func TestDimensions_StrictParsing(t *testing.T) {
	for _, v := range []any{0, -1, 1.5, "768", true, uint64(^uint64(0))} {
		if _, e := ParseEmbeddingDimensions(v); !errors.Is(e, ErrEmbeddingDimensions) {
			t.Fatalf("accepted %v", v)
		}
	}
	for _, v := range []any{768, int64(768), uint32(768)} {
		if d, e := ParseEmbeddingDimensions(v); e != nil || d != 768 {
			t.Fatal(d, e)
		}
	}
	if d, e := ParseEmbeddingDimensions(nil); e != nil || d != 0 {
		t.Fatal(d, e)
	}
}
func TestDimensions_SchemaAndCopy(t *testing.T) {
	c := &Config{EmbeddingDimensions: 768, Provider: &ProviderConfig{EmbeddingModel: "m", EmbeddingDimensions: 768}}
	copy := c.Copy()
	copy.Provider.EmbeddingDimensions = 512
	if c.Provider.EmbeddingDimensions != 768 || copy.EmbeddingDimensions != 768 {
		t.Fatal("copy lost dimensions or aliased provider")
	}
	m, e := ESMapping(768)
	if e != nil {
		t.Fatal(e)
	}
	q, e := QdrantCollection("test", 768)
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{coretypes.FieldContentVector, coretypes.FieldQAContentVector} {
		if *m.Properties[key].(*types.DenseVectorProperty).Dims != 768 {
			t.Fatal("ES size")
		}
		if q.VectorsConfig.GetParamsMap().Map[key].Size != 768 {
			t.Fatal("Qdrant size")
		}
	}
	if _, e = ESMapping(0); !errors.Is(e, ErrEmbeddingDimensions) {
		t.Fatal(e)
	}
	if _, e = QdrantCollection("test", -1); !errors.Is(e, ErrEmbeddingDimensions) {
		t.Fatal(e)
	}
}
