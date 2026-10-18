package rag

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/container/gvar"
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
	VectorCfg       vector.Config
	EmbeddingAPIKey string
	BaseURL         string
	EmbeddingModel  string
	ChatModel       string
	QAEnabled       *bool // nil = 默认启用
	QAMaxConcurrent int   // 0 或负数 = 默认 4

	// 后台任务生命周期配置
	MaxBackgroundTasks    int           // 默认 8，<=0 默认，>256 钳位到 256
	BackgroundTaskTimeout time.Duration // 默认 5min，>60min 钳位到 60min
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
		Client:                client,
		QdrantClient:          qdrantClient,
		IndexName:             cfg.VectorCfg.IndexName,
		APIKey:                cfg.EmbeddingAPIKey,
		BaseURL:               cfg.BaseURL,
		EmbeddingModel:        cfg.EmbeddingModel,
		ChatModel:             cfg.ChatModel,
		QAEnabled:             cfg.QAEnabled,
		QAMaxConcurrent:       cfg.QAMaxConcurrent,
		MaxBackgroundTasks:    cfg.MaxBackgroundTasks,
		BackgroundTaskTimeout: cfg.BackgroundTaskTimeout,
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

// configGetter 配置读取接口
type configGetter interface {
	Get(ctx context.Context, key string) (*gvar.Var, error)
}

// defaultConfigGetter 生产默认配置读取器
type defaultConfigGetter struct{}

func (d *defaultConfigGetter) Get(ctx context.Context, key string) (*gvar.Var, error) {
	return g.Cfg().Get(ctx, key)
}

// readQAConfig 从 reader 读取 QA 配置。缺失字段使用默认值；读取失败返回 error。
func readQAConfig(ctx context.Context, getter configGetter) (qaEnabled *bool, qaMaxConcurrent int, err error) {
	// qa.enabled
	v, err := getter.Get(ctx, "qa.enabled")
	if err != nil {
		return nil, 0, fmt.Errorf("read qa.enabled: %w", err)
	}
	if !v.IsNil() {
		b := v.Bool()
		qaEnabled = &b
	}

	// qa.maxConcurrent
	v, err = getter.Get(ctx, "qa.maxConcurrent")
	if err != nil {
		return nil, 0, fmt.Errorf("read qa.maxConcurrent: %w", err)
	}
	if !v.IsNil() {
		qaMaxConcurrent = v.Int()
	}

	return qaEnabled, qaMaxConcurrent, nil
}

// readLifecycleConfig 从 reader 读取后台任务生命周期配置。缺失字段使用默认值；读取失败返回 error。
// 类型校验：必须为整数，非整数类型返回错误。
func readLifecycleConfig(ctx context.Context, getter configGetter) (maxBG int, timeout time.Duration, err error) {
	// rag.maxBackgroundTasks: 缺失默认 0（由 New() 钳位到 8）
	v, err := getter.Get(ctx, "rag.maxBackgroundTasks")
	if err != nil {
		return 0, 0, fmt.Errorf("read rag.maxBackgroundTasks: %w", err)
	}
	if !v.IsNil() {
		// 类型校验：必须是整数
		if !v.IsInt() {
			return 0, 0, fmt.Errorf("rag.maxBackgroundTasks must be int, got %T", v.Val())
		}
		maxBG = v.Int()
	}

	// rag.backgroundTaskTimeoutMinutes: 缺失默认 0（由 New() 钳位到 5min）
	v, err = getter.Get(ctx, "rag.backgroundTaskTimeoutMinutes")
	if err != nil {
		return 0, 0, fmt.Errorf("read rag.backgroundTaskTimeoutMinutes: %w", err)
	}
	if !v.IsNil() {
		// 类型校验：必须是整数
		if !v.IsInt() {
			return 0, 0, fmt.Errorf("rag.backgroundTaskTimeoutMinutes must be int, got %T", v.Val())
		}
		minutes := v.Int()
		// 先校验/限制分钟数，再转换为 time.Duration，避免乘法溢出
		if minutes > 60 {
			minutes = 60
		}
		if minutes <= 0 {
			minutes = 5 // 非正默认 5
		}
		timeout = time.Duration(minutes) * time.Minute
	}

	return maxBG, timeout, nil
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

	// QA 配置
	qaEnabled, qaMaxConcurrent, err := readQAConfig(ctx, &defaultConfigGetter{})
	if err != nil {
		return RagConfig{}, fmt.Errorf("read qa config: %w", err)
	}

	// 后台任务生命周期配置
	maxBG, timeout, err := readLifecycleConfig(ctx, &defaultConfigGetter{})
	if err != nil {
		return RagConfig{}, fmt.Errorf("read lifecycle config: %w", err)
	}

	return RagConfig{
		VectorCfg:             *vectorCfg,
		EmbeddingAPIKey:       apiKey,
		BaseURL:               baseURL,
		EmbeddingModel:        embeddingModel,
		ChatModel:             chatModel,
		QAEnabled:             qaEnabled,
		QAMaxConcurrent:       qaMaxConcurrent,
		MaxBackgroundTasks:    maxBG,
		BackgroundTaskTimeout: timeout,
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
// 如果 Rag 实例存在，先调用 Shutdown 等待后台任务收敛，防止绕过收敛。
// Shutdown 失败时保留依赖和引用，返回可识别错误，不继续 Close/置空。
func Reset() error {
	ragMu.Lock()

	// 锁内捕获实例及代次，解锁后不读全局
	target := ragSvr
	targetVS := vectorStore
	targetGen := ragInit

	if target == nil {
		// 无实例，直接清理 vectorStore
		var err error
		if targetVS != nil {
			err = targetVS.Close()
			vectorStore = nil
		}
		ragSvr = nil
		ragInit = false
		ragMu.Unlock()
		return err
	}

	// 解锁等待捕获实例的 Shutdown
	ragMu.Unlock()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := target.Shutdown(shutdownCtx)
	shutdownCancel()

	if err != nil {
		// Shutdown 失败：保留依赖和状态，返回可识别错误
		return fmt.Errorf("rag shutdown failed (dependencies preserved): %w", err)
	}

	// 成功：重新加锁核实身份
	ragMu.Lock()

	// 核实仍为同一代
	if ragSvr == target && ragInit == targetGen {
		// 同一代，安全关闭
		var closeErr error
		if targetVS != nil {
			closeErr = targetVS.Close()
		}
		ragSvr = nil
		vectorStore = nil
		ragInit = false
		ragMu.Unlock()
		return closeErr
	}

	// 换代：另一调用已完成 Reset+Init，不触碰新对象
	ragMu.Unlock()
	return nil
}
