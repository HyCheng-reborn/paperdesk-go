package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/container/gvar"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"

	// 导入百炼适配包，触发 init() 注册 builder hooks
	_ "github.com/wangle201210/go-rag/server/core/providers/bailian"
	// Fix 6: 导入 OpenAI 适配包，触发 init() 注册 builder hooks
	_ "github.com/wangle201210/go-rag/server/core/providers/openai"
)

var (
	ragSvr      *core.Rag
	vectorStore vector.VectorStore
	ragMu       sync.RWMutex
	ragInit     bool

	// B5: 进程级 ProcessBudget 持久化，跨 Registry 重建复用同一实例。
	// 由进程 owner 持有，不随 Registry 重建刷新。
	processBudget   *providers.ProcessBudget
	processBudgetMu sync.Mutex
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

	// Stage 2B: Provider 配置（可选，nil 时走旧路径）
	Provider *config.ProviderConfig
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

	// B5: Registry 先作为构建上下文，core.New 成功后才发布。
	// 失败回滚，不影响旧实例。
	var reg providers.ProviderRegistry
	if cfg.Provider != nil {
		var err error
		reg, err = buildRegistry(ctx, cfg.Provider)
		if err != nil {
			return nil, fmt.Errorf("build provider registry: %w", err)
		}
		// 不在此处 SetRegistry —— 等 core.New 成功后才发布
	}

	rag, err := core.New(ctx, &config.Config{
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
		Provider:              cfg.Provider,
		Registry:              reg, // B5: 显式传入本次 Registry
	})
	if err != nil {
		// B5: core.New 失败 → 不发布 Registry，不影响旧实例
		return nil, err
	}

	// B5: core.New 成功后才发布 Registry
	if reg != nil {
		common.SetRegistry(reg)
	}

	return rag, nil
}

// buildRegistry 从 ProviderConfig 构建 ProviderRegistry。
// 构造过程不发起网络请求；凭据解析失败或 Profile 校验失败返回错误。
// B5: ProcessBudget 跨 Registry 重建复用同一实例（进程 owner 持有）。
func buildRegistry(ctx context.Context, pc *config.ProviderConfig) (providers.ProviderRegistry, error) {
	if pc == nil {
		return nil, fmt.Errorf("nil ProviderConfig")
	}

	// 解析 API Key：优先配置值，其次环境变量
	apiKey := pc.APIKey
	if apiKey == "" {
		apiKey = resolveAPIKeyFromEnv(pc.Name)
	}

	// 凭据解析器：返回固定 apiKey
	credFn := func(ctx context.Context, p *providers.ProviderProfile) (string, error) {
		if apiKey == "" {
			return "", fmt.Errorf("API key not configured for provider %q", pc.Name)
		}
		return apiKey, nil
	}

	// B5: ProcessBudget 持久化：首次创建，后续复用同一实例（不随 Registry 重建刷新）
	processBudgetMu.Lock()
	if processBudget == nil {
		var err error
		processBudget, err = providers.NewProcessBudget(pc.MaxRequests, pc.MaxInputTokens, pc.MaxOutputTokens, pc.MaxConcurrency)
		if err != nil {
			processBudgetMu.Unlock()
			return nil, fmt.Errorf("create process budget: %w", err)
		}
	}
	budget := processBudget
	processBudgetMu.Unlock()

	// 构造共享 Transport
	transport := providers.NewTransport()

	// 构建各角色的 ProviderProfile
	protocol := "openai-compatible"
	providerName := pc.Name
	baseURL := pc.BaseURL

	var err error

	// Chat Profile
	var chatProfile *providers.ProviderProfile
	if pc.ChatModel != "" {
		chatProfile, err = providers.NewProviderProfile(providers.ProviderProfile{
			ProviderName: providerName,
			Protocol:     protocol,
			Endpoint:     baseURL,
			Model:        pc.ChatModel,
			Capabilities: providers.Capabilities{Stream: true, Usage: true},
		})
		if err != nil {
			return nil, fmt.Errorf("chat profile: %w", err)
		}
	}

	// Rewrite Profile
	var rewriteProfile *providers.ProviderProfile
	if pc.RewriteModel != "" {
		rewriteProfile, err = providers.NewProviderProfile(providers.ProviderProfile{
			ProviderName: providerName,
			Protocol:     protocol,
			Endpoint:     baseURL,
			Model:        pc.RewriteModel,
			Capabilities: providers.Capabilities{Stream: true, Usage: true},
		})
		if err != nil {
			return nil, fmt.Errorf("rewrite profile: %w", err)
		}
	}

	// QA Profile
	var qaProfile *providers.ProviderProfile
	if pc.QAModel != "" {
		qaProfile, err = providers.NewProviderProfile(providers.ProviderProfile{
			ProviderName: providerName,
			Protocol:     protocol,
			Endpoint:     baseURL,
			Model:        pc.QAModel,
			Capabilities: providers.Capabilities{Stream: true, Usage: true},
		})
		if err != nil {
			return nil, fmt.Errorf("qa profile: %w", err)
		}
	}

	// Embed Profile
	var embedProfile *providers.ProviderProfile
	if pc.EmbeddingModel != "" {
		dims := pc.EmbeddingDimensions
		if dims <= 0 {
			dims = 1024
		}
		embedProfile, err = providers.NewProviderProfile(providers.ProviderProfile{
			ProviderName: providerName,
			Protocol:     protocol,
			Endpoint:     baseURL,
			Model:        pc.EmbeddingModel,
			Dimensions:   dims,
		})
		if err != nil {
			return nil, fmt.Errorf("embed profile: %w", err)
		}
	}

	// Rerank Profile
	var rerankProfile *providers.ProviderProfile
	if pc.RerankModel != "" {
		// B4: Rerank endpoint 必须显式配置，不能靠拼接
		rerankURL := pc.RerankURL
		if rerankURL == "" {
			return nil, fmt.Errorf("rerank_url must be explicitly configured when rerank_model is set")
		}
		rerankProfile, err = providers.NewProviderProfile(providers.ProviderProfile{
			ProviderName: providerName,
			Protocol:     protocol,
			Endpoint:     rerankURL,
			Model:        pc.RerankModel,
		})
		if err != nil {
			return nil, fmt.Errorf("rerank profile: %w", err)
		}
	}

	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        chatProfile,
		Rewrite:     rewriteProfile,
		QA:          qaProfile,
		Embed:       embedProfile,
		Rerank:      rerankProfile,
		Budget:      budget,
		Transport:   transport,
		Credentials: credFn,
	})
	if err != nil {
		return nil, fmt.Errorf("create registry: %w", err)
	}
	return reg, nil
}

// resolveAPIKeyFromEnv 根据 Provider 名称从环境变量解析 API Key。
func resolveAPIKeyFromEnv(providerName string) string {
	switch providerName {
	case "bailian":
		for _, k := range []string{"DASHSCOPE_API_KEY", "OPENAI_API_KEY"} {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
	case "openai":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v
		}
	}
	return ""
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

	// Stage 2B: Provider 配置块（可选，缺失时 nil）
	var providerCfg *config.ProviderConfig
	v, err := g.Cfg().Get(ctx, "provider")
	if err == nil && !v.IsNil() {
		providerCfg = &config.ProviderConfig{}
		if scanErr := v.Scan(providerCfg); scanErr != nil {
			return RagConfig{}, fmt.Errorf("scan provider config: %w", scanErr)
		}
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
		Provider:              providerCfg,
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
// Fix 4: Shutdown 失败时保留所有依赖（Registry、vectorStore、ragSvr、ragInit），
// SetRegistry(nil) 移到成功+身份核实后。
func Reset() error {
	ragMu.Lock()

	// 锁内捕获实例及代次，解锁后不读全局
	target := ragSvr
	targetVS := vectorStore
	targetGen := ragInit

	if target == nil {
		// 无实例，直接清理 vectorStore 和 Registry
		common.SetRegistry(nil)
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
		// Fix 4: Shutdown 失败：保留所有依赖和状态（Registry、vectorStore、ragSvr、ragInit）
		return fmt.Errorf("rag shutdown failed (dependencies preserved): %w", err)
	}

	// 成功：重新加锁核实身份
	ragMu.Lock()

	// 核实仍为同一代
	if ragSvr == target && ragInit == targetGen {
		// Fix 4: 同一代，安全关闭 —— SetRegistry(nil) 在成功+身份核实后
		common.SetRegistry(nil)
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
