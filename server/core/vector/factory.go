package vector

import (
	"fmt"
	"github.com/wangle201210/go-rag/server/core/config"
)

// NewVectorStore 创建向量存储实例
func NewVectorStore(cfg *Config) (VectorStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil vector config")
	}
	d, err := config.ResolveEmbeddingDimensions(cfg.EmbeddingDimensions, nil)
	if err != nil {
		return nil, err
	}
	switch cfg.Type {
	case "es", "elasticsearch":
		if cfg.ES == nil {
			return nil, fmt.Errorf("es config is required when type is es")
		}
		return NewESVectorStore(cfg.ES, d)
	case "qdrant":
		if cfg.Qdrant == nil {
			return nil, fmt.Errorf("qdrant config is required when type is qdrant")
		}
		return NewQdrantVectorStore(cfg.Qdrant, d)
	default:
		return nil, fmt.Errorf("unsupported vector store type: %s", cfg.Type)
	}
}

func storeDimensions(values []int) (int, error) {
	if len(values) > 1 {
		return 0, config.ErrEmbeddingDimensionConflict
	}
	d := 0
	if len(values) == 1 {
		d = values[0]
	}
	return config.ResolveEmbeddingDimensions(d, nil)
}
