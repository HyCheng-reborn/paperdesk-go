package rag

import (
	"context"
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
	ragMu    sync.RWMutex
	ragInit  bool
)

// cfgGet 安全读取配置，不 panic。
func cfgGet(ctx context.Context, key string) string {
	v, _ := g.Cfg().Get(ctx, key)
	return v.String()
}

// cfgGetInt 安全读取配置整数，不 panic。
func cfgGetInt(ctx context.Context, key string) int {
	v, _ := g.Cfg().Get(ctx, key)
	return v.Int()
}

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
	vectorType := cfgGet(ctx, "vector.type")
	indexName := cfgGet(ctx, "vector.indexName")

	if vectorType == "" {
		return fmt.Errorf("rag: vector.type not configured")
	}
	if indexName == "" {
		return fmt.Errorf("rag: vector.indexName not configured")
	}

	// 创建向量存储配置
	vectorCfg := &vector.Config{
		Type:      vectorType,
		IndexName: indexName,
	}

	// 根据类型配置
	if vectorType == "es" || vectorType == "elasticsearch" {
		vectorCfg.ES = &vector.ESConfig{
			Address:  cfgGet(ctx, "vector.es.address"),
			Username: cfgGet(ctx, "vector.es.username"),
			Password: cfgGet(ctx, "vector.es.password"),
		}
	} else if vectorType == "qdrant" {
		vectorCfg.Qdrant = &vector.QdrantConfig{
			Address: cfgGet(ctx, "vector.qdrant.address"),
			Port:    cfgGetInt(ctx, "vector.qdrant.port"),
			APIKey:  cfgGet(ctx, "vector.qdrant.apiKey"),
		}
	}

	// 创建向量存储实例
	vectorStore, err := vector.NewVectorStore(vectorCfg)
	if err != nil {
		return fmt.Errorf("rag: NewVectorStore failed: %w", err)
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
		APIKey:         cfgGet(ctx, "embedding.apiKey"),
		BaseURL:        cfgGet(ctx, "embedding.baseURL"),
		EmbeddingModel: cfgGet(ctx, "embedding.model"),
		ChatModel:      cfgGet(ctx, "chat.model"),
	})
	if err != nil {
		return fmt.Errorf("rag: New of rag failed: %w", err)
	}

	// 成功后才赋值全局变量（原子发布）
	ragSvr = ragsvr
	ragInit = true
	return nil
}

// GetRagSvr 返回 RAG 服务实例。未初始化时返回 nil。
func GetRagSvr() *core.Rag {
	ragMu.RLock()
	defer ragMu.RUnlock()
	if !ragInit {
		return nil
	}
	return ragSvr
}

// IsInitialized 返回 RAG 服务是否已初始化。
func IsInitialized() bool {
	ragMu.RLock()
	defer ragMu.RUnlock()
	return ragInit
}

// Reset 重置 RAG 服务状态，清理全局变量。用于初始化失败时的级联清理。
func Reset() {
	ragMu.Lock()
	defer ragMu.Unlock()
	ragSvr = nil
	ragInit = false
}
