package indexer

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino-ext/components/indexer/es8"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// newAsyncIndexer component initialization function of node 'Indexer2' in graph 'rag'
func newAsyncIndexer(ctx context.Context, conf *config.Config) (idr indexer.Indexer, err error) {
	dimensions, err := conf.ResolveDimensions()
	if err != nil {
		return nil, err
	}
	embeddingIns11, err := common.NewEmbedding(ctx, conf)
	if err != nil {
		return nil, err
	}

	// 根据向量存储类型创建不同的 indexer
	if conf.Client != nil {
		// ES indexer
		indexerConfig := &es8.IndexerConfig{
			Client:    conf.Client,
			Index:     conf.IndexName,
			BatchSize: 10,
			DocumentToFields: func(ctx context.Context, doc *schema.Document) (field2Value map[string]es8.FieldValue, err error) {
				// 生产回调仅做必要的上下文读取，随后委派给共享转换 helper（不修改输入 doc）
				var knowledgeName string
				if value, ok := ctx.Value(coretypes.KnowledgeName).(string); ok {
					knowledgeName = value
				} else {
					return nil, fmt.Errorf("必须提供知识库名称")
				}
				return documentToFields(ctx, doc, knowledgeName)
			},
		}
		indexerConfig.Embedding = embeddingIns11
		idr, err = es8.NewIndexer(ctx, indexerConfig)
		if err != nil {
			return nil, err
		}
		return idr, nil
	} else if conf.QdrantClient != nil {
		// Qdrant indexer
		idr, err = NewQdrantIndexer(ctx, &QdrantIndexerConfig{
			Client:     conf.QdrantClient,
			Collection: conf.IndexName,
			VectorDim:  dimensions,
			Distance:   0, // 使用默认 Cosine
			Embedding:  embeddingIns11,
			BatchSize:  10,
			IsAsync:    true,
		})
		if err != nil {
			return nil, err
		}
		return idr, nil
	} else {
		return nil, fmt.Errorf("no valid client configuration found")
	}
}

// documentToFields 是完整的生产字段转换 helper：将 Document 映射为 ES 字段，
// 包含 Extra 序列化。不向输入 Document 或 MetaData 写入；Extra 直接放入新返回的 fields map；
// 序列化失败返回可追踪错误且不返回成功字段。保留正常 Extra 数据语义、Content/QA 的 EmbedKey，
// 以及“非空 QA 才写 QA 字段”的规则。
func documentToFields(ctx context.Context, doc *schema.Document, knowledgeName string) (map[string]es8.FieldValue, error) {
	fields := map[string]es8.FieldValue{
		coretypes.FieldContent:  {Value: doc.Content, EmbedKey: coretypes.FieldContentVector},
		coretypes.KnowledgeName: {Value: knowledgeName},
	}

	// Extra = ExtKeys 子集的序列化，直接写入返回 map（不写回输入 doc.MetaData）。
	// 序列化失败（getExtData 实际保留的不可编码值）时返回可追踪错误，不继续返回成功字段。
	if doc.MetaData != nil {
		marshal, mErr := sonic.Marshal(getExtData(doc))
		if mErr != nil {
			return nil, fmt.Errorf("documentToFields: marshal ext for doc %q: %w", doc.ID, mErr)
		}
		fields[coretypes.FieldExtra] = es8.FieldValue{Value: string(marshal)}
	}

	// QA 字段条件写入（仅非空）
	if qaVal, ok := doc.MetaData[coretypes.FieldQAContent].(string); ok && qaVal != "" {
		fields[coretypes.FieldQAContent] = es8.FieldValue{
			Value:    qaVal,
			EmbedKey: coretypes.FieldQAContentVector,
		}
	}

	return fields, nil
}
