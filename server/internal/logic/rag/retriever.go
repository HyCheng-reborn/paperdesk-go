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
	ragSvr      *core.Rag
	vectorStore vector.VectorStore
	ragMu       sync.RWMutex
	ragInit     bool
)

// VectorStoreFactory 创建 VectorStore 的工厂接口
// 生产环境使用 defaultVectorStoreFactory，测试使用 fake
type VectorStoreFactory interface {
	Create(cfg *vector.Config) (vector.VectorStore, error)
}

// defaultVectorStoreFactory 生产环境的 VectorStoreFactory 实现
type defaultVectorStoreFactory struct{}

func (f *defaultVectorStoreFactory) Create(cfg *vector.Config) (vector.VectorStore, error) {
	return vector.NewVectorStore(cfg)
}

// cfgGet 安全读取配置，传播错误而非忽略。
func cfgGet(ctx context.Context, key string) (string, error) {
	v, err := g.Cfg().Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("failed to read config %q: %w", key, err)
	}
	return v.String(), nil
}

// cfgGetInt 安全读取配置整数，传播错误而非忽略。
func cfgGetInt(ctx context.Context, key string) (int, error) {
	v, err := g.Cfg().Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("failed to read config %q: %w", key, err)
	}
	return v.Int(), nil
}

// Init 初始化 RAG 服务（生产入口，等价于 InitWith(nil, nil)）。
func Init() error {
	return InitWith(nil, nil)
}

// InitWith 初始化 RAG 服务：使用注入的配置和工厂。
// vectorCfg: 注入的向量配置（nil 时从 g.Cfg() 读取）；
// vsFactory: 注入的 VectorStore 工厂（nil 时使用默认工厂）。
// 重复调用返回 error（幂等拒绝）。
func InitWith(vectorCfg *vector.Config, vsFactory VectorStoreFactory) error {
	ragMu.Lock()
	defer ragMu.Unlock()

	if ragInit {
		return fmt.Errorf("rag: Init already called")
	}

	ctx := gctx.New()

	// 如果未注入配置，从 g.Cfg() 读取
	if vectorCfg == nil {
		var err error
		vectorCfg, err = buildVectorConfigFromCfg(ctx)
		if err != nil {
			return fmt.Errorf("rag: %w", err)
		}
	}

	// 如果未注入工厂，使用默认工厂
	if vsFactory == nil {
		vsFactory = &defaultVectorStoreFactory{}
	}

	// 创建向量存储实例
	vs, err := vsFactory.Create(vectorCfg)
	if err != nil {
		return fmt.Errorf("rag: NewVectorStore failed: %w", err)
	}

	// 根据类型获取对应的客户端
	var client *elasticsearch.Client
	var qdrantClient *qdrant.Client

	if esStore, ok := vs.(*vector.ESVectorStore); ok {
		client = esStore.GetClient()
	} else if qdrantStore, ok := vs.(*vector.QdrantVectorStore); ok {
		qdrantClient = qdrantStore.GetClient()
	}

	// 读取 embedding 配置
	apiKey, _ := cfgGet(ctx, "embedding.apiKey")
	baseURL, _ := cfgGet(ctx, "embedding.baseURL")
	embeddingModel, _ := cfgGet(ctx, "embedding.model")
	chatModel, _ := cfgGet(ctx, "chat.model")

	// 使用局部变量构建完整实例
	ragsvr, err := core.New(ctx, &config.Config{
		Client:         client,
		QdrantClient:   qdrantClient,
		IndexName:      vectorCfg.IndexName,
		APIKey:         apiKey,
		BaseURL:        baseURL,
		EmbeddingModel: embeddingModel,
		ChatModel:      chatModel,
	})
	if err != nil {
		// 清理已创建的 vectorStore
		_ = vs.Close()
		return fmt.Errorf("rag: New of rag failed: %w", err)
	}

	// 成功后才赋值全局变量（原子发布）
	ragSvr = ragsvr
	vectorStore = vs
	ragInit = true
	return nil
}

// buildVectorConfigFromCfg 从 GoFrame 配置构建向量存储配置
func buildVectorConfigFromCfg(ctx context.Context) (*vector.Config, error) {
	vectorType, err := cfgGet(ctx, "vector.type")
	if err != nil {
		return nil, err
	}
	if vectorType == "" {
		return nil, fmt.Errorf("vector.type not configured")
	}
	indexName, err := cfgGet(ctx, "vector.indexName")
	if err != nil {
		return nil, err
	}
	if indexName == "" {
		return nil, fmt.Errorf("vector.indexName not configured")
	}

	vectorCfg := &vector.Config{
		Type:      vectorType,
		IndexName: indexName,
	}

	if vectorType == "es" || vectorType == "elasticsearch" {
		esAddr, _ := cfgGet(ctx, "vector.es.address")
		esUser, _ := cfgGet(ctx, "vector.es.username")
		esPass, _ := cfgGet(ctx, "vector.es.password")
		vectorCfg.ES = &vector.ESConfig{
			Address:  esAddr,
			Username: esUser,
			Password: esPass,
		}
	} else if vectorType == "qdrant" {
		qdAddr, _ := cfgGet(ctx, "vector.qdrant.address")
		qdPort, _ := cfgGetInt(ctx, "vector.qdrant.port")
		qdKey, _ := cfgGet(ctx, "vector.qdrant.apiKey")
		vectorCfg.Qdrant = &vector.QdrantConfig{
			Address: qdAddr,
			Port:    qdPort,
			APIKey:  qdKey,
		}
	}

	return vectorCfg, nil
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

// Reset 重置 RAG 服务状态，释放资源。用于初始化失败时的级联清理。
// 实际关闭 VectorStore 连接（如果实现了 Close 接口）。
func Reset() error {
	ragMu.Lock()
	defer ragMu.Unlock()

	var err error
	if vectorStore != nil {
		err = vectorStore.Close()
		vectorStore = nil
	}
	ragSvr = nil
	ragInit = false
	return err
}
