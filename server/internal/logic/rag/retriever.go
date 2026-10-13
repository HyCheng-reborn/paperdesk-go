package rag

import (
	"fmt"
	"sync"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

var (
	ragSvr   *core.Rag
	ragMu    sync.Mutex
	ragInit  bool
)

// Init 初始化 RAG 服务：读取向量存储配置、创建向量存储连接、创建 embedding 客户端。
// 必须在程序启动时显式调用，不在 init() 中自动执行，以支持离线测试隔离。
// 重复调用返回 error（幂等拒绝）。
func Init() error {
	ragMu.Lock()
	defer ragMu.Unlock()

	if ragInit {
		return fmt.Errorf("rag: Init already called")
	}

	ctx := gctx.New()

	// 读取向量存储配置
	vectorType := g.Cfg().MustGet(ctx, "vector.type").String()
	indexName := g.Cfg().MustGet(ctx, "vector.indexName").String()

	// 创建向量存储配置
	vectorCfg := &vector.Config{
		Type:      vectorType,
		IndexName: indexName,
	}

	// 根据类型配置
	if vectorType == "es" || vectorType == "elasticsearch" {
		vectorCfg.ES = &vector.ESConfig{
			Address:  g.Cfg().MustGet(ctx, "vector.es.address").String(),
			Username: g.Cfg().MustGet(ctx, "vector.es.username").String(),
			Password: g.Cfg().MustGet(ctx, "vector.es.password").String(),
		}
	} else if vectorType == "qdrant" {
		vectorCfg.Qdrant = &vector.QdrantConfig{
			Address: g.Cfg().MustGet(ctx, "vector.qdrant.address").String(),
			Port:    g.Cfg().MustGet(ctx, "vector.qdrant.port").Int(),
			APIKey:  g.Cfg().MustGet(ctx, "vector.qdrant.apiKey").String(),
		}
	}

	// 创建向量存储实例
	vectorStore, err := vector.NewVectorStore(vectorCfg)
	if err != nil {
		return fmt.Errorf("NewVectorStore failed: %w", err)
	}

	// 根据类型获取对应的客户端
	var client *elasticsearch.Client
	var qdrantClient *qdrant.Client

	if esStore, ok := vectorStore.(*vector.ESVectorStore); ok {
		client = esStore.GetClient()
	} else if qdrantStore, ok := vectorStore.(*vector.QdrantVectorStore); ok {
		qdrantClient = qdrantStore.GetClient()
	}

	// 使用局部变量构建完整实例
	ragsvr, err := core.New(ctx, &config.Config{
		Client:         client,
		QdrantClient:   qdrantClient,
		IndexName:      indexName,
		APIKey:         g.Cfg().MustGet(ctx, "embedding.apiKey").String(),
		BaseURL:        g.Cfg().MustGet(ctx, "embedding.baseURL").String(),
		EmbeddingModel: g.Cfg().MustGet(ctx, "embedding.model").String(),
		ChatModel:      g.Cfg().MustGet(ctx, "chat.model").String(),
	})
	if err != nil {
		return fmt.Errorf("New of rag failed: %w", err)
	}

	// 成功后才赋值全局变量（原子发布）
	ragSvr = ragsvr
	ragInit = true
	return nil
}

// GetRagSvr 返回 RAG 服务实例。未初始化时返回 nil。
func GetRagSvr() *core.Rag {
	ragMu.Lock()
	defer ragMu.Unlock()
	if !ragInit {
		return nil
	}
	return ragSvr
}

// IsInitialized 返回 RAG 服务是否已初始化。
func IsInitialized() bool {
	ragMu.Lock()
	defer ragMu.Unlock()
	return ragInit
}
