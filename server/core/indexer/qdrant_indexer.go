package indexer

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
	"github.com/qdrant/go-client/qdrant"
)

// QdrantIndexerConfig Qdrant indexer 配置
type QdrantIndexerConfig struct {
	Client     *qdrant.Client     // Required: Qdrant client
	Collection string             // Required: Collection name
	VectorDim  int                // Required: Vector dimension
	Distance   qdrant.Distance    // Required: Distance metric
	BatchSize  int                // Optional: Batch size (default: 10)
	Embedding  embedding.Embedder // Required: Embedding component
	IsAsync    bool               // Optional: 是否异步模式（包含 QA 向量）
}

// QdrantIndexer Qdrant indexer 实现（使用自定义命名向量存储）
// E2: einoIndexer 已移除 — 该字段在构造时触发 SDK ensureCollection 但从未被 Store 使用。
// 实际 Store 走 StoreWithNamedVectors 自定义路径，直接操作 client。
type QdrantIndexer struct {
	config       *QdrantIndexerConfig
	asyncIndexer indexer.Indexer // 异步模式的 indexer（用于 QA 向量）
}

// NewQdrantIndexer 创建 Qdrant indexer，使用自定义命名向量实现。
// E2: 不再构造 eino SDK indexer，避免 SDK ensureCollection 在受控创建之外隐式创建集合。
func NewQdrantIndexer(ctx context.Context, config *QdrantIndexerConfig) (indexer.Indexer, error) {
	if config.Client == nil {
		return nil, fmt.Errorf("qdrant client is required")
	}
	if config.Collection == "" {
		return nil, fmt.Errorf("collection name is required")
	}
	if config.Embedding == nil {
		return nil, fmt.Errorf("embedding component is required")
	}
	if config.BatchSize == 0 {
		config.BatchSize = 10
	}
	if config.Distance == 0 {
		config.Distance = qdrant.Distance_Cosine
	}

	idx := &QdrantIndexer{
		config: config,
	}

	// E2: 不再构造 eino SDK indexer。
	// 集合创建由受控 CreateIndex 路径负责（retriever.go defaultRagFactory）。
	// Store 操作使用 StoreWithNamedVectors 自定义实现，直接操作 client。

	return idx, nil
}

// Store 存储文档（实现 Indexer 接口）
// 使用自定义实现支持命名向量
func (idx *QdrantIndexer) Store(ctx context.Context, docs []*schema.Document, opts ...indexer.Option) ([]string, error) {
	return idx.StoreWithNamedVectors(ctx, docs, opts...)
}

// GetType 返回 indexer 类型
func (idx *QdrantIndexer) GetType() string {
	return "qdrant_indexer"
}
