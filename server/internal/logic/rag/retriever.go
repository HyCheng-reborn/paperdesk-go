package rag

import (
	"context"
	"errors"
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

// RagConfig 完整 RAG 初始化配置
type RagConfig struct {
	VectorCfg      vector.Config
	EmbeddingAPIKey string
	BaseURL        string
	EmbeddingModel string
	ChatModel      string
}

// RagFactory 创建 *core.Rag 的工厂接口
// 生产环境使用 defaultRagFactory，测试使用 fake
type RagFactory interface {
	Create(cfg RagConfig, vs vector.VectorStore) (*core.Rag, error)
}

// defaultRagFactory 生产环境的 RagFactory 实现
type defaultRagFactory struct{}

func (f *defaultRagFactory) Create(cfg RagConfig, vs vector.VectorStore) (*core.Rag, error) {
	ctx := context.Background()

	// 根据 VectorStore 类型提取客户端
	var client *elasticsearch.Client
	var qdrantClient *qdrant.Client

	if esStore, ok := vs.(*vector.ESVectorStore); ok {
		client = esStore.GetClient()
	} else if qdrantStore, ok := vs.(*vector.QdrantVectorStore); ok {
		qdrantClient = qdrantStore.GetClient()
	}

	return core.New(ctx, &config.Config{
		Client:         client,
		QdrantClient:   qdrantClient,
		IndexName:      cfg.VectorCfg.IndexName,
		APIKey:         cfg.EmbeddingAPIKey,
		BaseURL:        cfg.BaseURL,
		EmbeddingModel: cfg.EmbeddingModel,
		ChatModel:      cfg.ChatModel,
	})
}

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

// readRagConfig 从 GoFrame 配置读取完整 RAG 配置（生产入口）
func readRagConfig() (RagConfig, error) {
	ctx := gctx.New()

	vectorCfg, err := buildVectorConfigFromCfg(ctx)
	if err != nil {
		return RagConfig{}, err
	}

	apiKey, err := cfgGet(ctx, "embedding.apiKey")
	if err != nil {
		return RagConfig{}, err
	}
	baseURL, err := cfgGet(ctx, "embedding.baseURL")
	if err != nil {
		return RagConfig{}, err
	}
	embeddingModel, err := cfgGet(ctx, "embedding.model")
	if err != nil {
		return RagConfig{}, err
	}
	chatModel, err := cfgGet(ctx, "chat.model")
	if err != nil {
		return RagConfig{}, err
	}

	return RagConfig{
		VectorCfg:      *vectorCfg,
		EmbeddingAPIKey: apiKey,
		BaseURL:        baseURL,
		EmbeddingModel: embeddingModel,
		ChatModel:      chatModel,
	}, nil
}

// InitWithRealDeps 使用真实依赖初始化 RAG 服务（生产入口）
func InitWithRealDeps() error {
	cfg, err := readRagConfig()
	if err != nil {
		return fmt.Errorf("rag: %w", err)
	}
	return InitWith(cfg, &defaultVectorStoreFactory{}, &defaultRagFactory{})
}

// Init 初始化 RAG 服务（生产入口，等价于 InitWithRealDeps）。
func Init() error {
	return InitWithRealDeps()
}

// InitWith 初始化 RAG 服务：使用注入的完整配置和工厂。
// cfg: 注入的 RAG 配置（必填）；
// vsFactory: 注入的 VectorStore 工厂（必填）；
// ragFactory: 注入的 RagFactory（必填）。
// 所有配置验证在工厂调用之前完成。
// 重复调用返回 error（幂等拒绝）。
func InitWith(cfg RagConfig, vsFactory VectorStoreFactory, ragFactory RagFactory) error {
	ragMu.Lock()
	defer ragMu.Unlock()

	if ragInit {
		return fmt.Errorf("rag: Init already called")
	}

	// 验证配置（在工厂调用之前）
	if cfg.VectorCfg.Type == "" {
		return fmt.Errorf("rag: vector.type not configured")
	}
	if cfg.VectorCfg.IndexName == "" {
		return fmt.Errorf("rag: vector.indexName not configured")
	}
	if cfg.EmbeddingModel == "" {
		return fmt.Errorf("rag: embedding.model not configured")
	}
	if cfg.ChatModel == "" {
		return fmt.Errorf("rag: chat.model not configured")
	}

	// 验证工厂
	if vsFactory == nil {
		return fmt.Errorf("rag: VectorStoreFactory must not be nil")
	}
	if ragFactory == nil {
		return fmt.Errorf("rag: RagFactory must not be nil")
	}

	// 创建向量存储实例
	vs, err := vsFactory.Create(&cfg.VectorCfg)
	if err != nil {
		return fmt.Errorf("rag: NewVectorStore failed: %w", err)
	}

	// 使用注入的 RagFactory 创建完整 RAG 服务
	ragsvr, err := ragFactory.Create(cfg, vs)
	if err != nil {
		closeErr := vs.Close()
		return errors.Join(
			fmt.Errorf("rag: New of rag failed: %w", err),
			closeErr,
		)
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
		esAddr, err := cfgGet(ctx, "vector.es.address")
		if err != nil {
			return nil, err
		}
		esUser, err := cfgGet(ctx, "vector.es.username")
		if err != nil {
			return nil, err
		}
		esPass, err := cfgGet(ctx, "vector.es.password")
		if err != nil {
			return nil, err
		}
		vectorCfg.ES = &vector.ESConfig{
			Address:  esAddr,
			Username: esUser,
			Password: esPass,
		}
	} else if vectorType == "qdrant" {
		qdAddr, err := cfgGet(ctx, "vector.qdrant.address")
		if err != nil {
			return nil, err
		}
		qdPort, err := cfgGetInt(ctx, "vector.qdrant.port")
		if err != nil {
			return nil, err
		}
		qdKey, err := cfgGet(ctx, "vector.qdrant.apiKey")
		if err != nil {
			return nil, err
		}
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
