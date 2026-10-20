package common

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/wangle201210/go-rag/server/core/providers"
)

var (
	embeddingModel model.BaseChatModel
	rerankModel    model.BaseChatModel
	rewriteModel   model.BaseChatModel
	chatModel      model.BaseChatModel
)

// ---------------------------------------------------------------------------
// Stage 2B: Registry 委派支持
// ---------------------------------------------------------------------------

var (
	registryMu sync.RWMutex
	registry   providers.ProviderRegistry
)

// SetRegistry 设置全局 ProviderRegistry（2B 接线）。
// 业务层在初始化时调用，将 Registry 注入到兼容层。
// 设置后，GetChatModel/GetRewriteModel/GetQAModel 在 cfg==nil 时委派给 Registry。
func SetRegistry(r providers.ProviderRegistry) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = r
}

// GetRegistry 返回当前注册的全局 ProviderRegistry（nil 表示未设置）。
func GetRegistry() providers.ProviderRegistry {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry
}

func GetChatModel(ctx context.Context, cfg *openai.ChatModelConfig) (model.BaseChatModel, error) {
	return GetChatModelWithRegistry(ctx, cfg, GetRegistry())
}

// GetChatModelWithRegistry 使用显式传入的 Registry 构造 Chat 模型。
// Fix 4: 不依赖全局 Registry，避免初始化期间临时发布。
func GetChatModelWithRegistry(ctx context.Context, cfg *openai.ChatModelConfig, reg providers.ProviderRegistry) (model.BaseChatModel, error) {
	// Fix 5: cfg != nil 时直接构造，不查 Registry（显式配置不被静默忽略）
	if cfg != nil {
		cm, err := openai.NewChatModel(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return cm, nil
	}
	// cfg == nil: 优先使用显式传入的 Registry
	if reg != nil {
		chatRole, err := reg.GetChat(ctx)
		if err != nil {
			return nil, fmt.Errorf("registry GetChat: %w", err)
		}
		return chatRole, nil
	}
	// 旧路径
	if chatModel != nil {
		return chatModel, nil
	}
	cfg = &openai.ChatModelConfig{}
	err := g.Cfg().MustGet(ctx, "chat").Scan(cfg)
	if err != nil {
		return nil, err
	}
	cm, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	chatModel = cm
	return cm, nil
}

func GetEmbeddingModel(ctx context.Context, cfg *openai.ChatModelConfig) (model.BaseChatModel, error) {
	if embeddingModel != nil {
		return embeddingModel, nil
	}
	if cfg == nil {
		cfg = &openai.ChatModelConfig{}
		err := g.Cfg().MustGet(ctx, "embedding").Scan(cfg)
		if err != nil {
			return nil, err
		}
	}
	cm, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	embeddingModel = cm
	return cm, nil
}

func GetRewriteModel(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	return GetRewriteModelWithRegistry(ctx, cfg, GetRegistry())
}

// GetRewriteModelWithRegistry 使用显式传入的 Registry 构造 Rewrite 模型。
// Fix 4: 不依赖全局 Registry。
func GetRewriteModelWithRegistry(ctx context.Context, cfg *qwen.ChatModelConfig, reg providers.ProviderRegistry) (model.BaseChatModel, error) {
	// Fix 5: cfg != nil 时直接构造，不查 Registry
	if cfg != nil {
		cm, err := qwen.NewChatModel(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return cm, nil
	}
	// cfg == nil: 优先使用显式传入的 Registry
	if reg != nil {
		chatRole, err := reg.GetRewrite(ctx)
		if err != nil {
			return nil, fmt.Errorf("registry GetRewrite: %w", err)
		}
		return chatRole, nil
	}
	// 旧路径
	if rewriteModel != nil {
		return rewriteModel, nil
	}
	cfg = &qwen.ChatModelConfig{}
	err := g.Cfg().MustGet(ctx, "rewrite").Scan(cfg)
	cfg.EnableThinking = Of(false)
	if err != nil {
		return nil, err
	}
	cm, err := qwen.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rewriteModel = cm
	return cm, nil
}

func GetRerankModel(ctx context.Context, cfg *openai.ChatModelConfig) (model.BaseChatModel, error) {
	if rerankModel != nil {
		return rerankModel, nil
	}
	if cfg == nil {
		cfg = &openai.ChatModelConfig{}
		err := g.Cfg().MustGet(ctx, "rerank").Scan(cfg)
		if err != nil {
			return nil, err
		}
	}
	cm, err := openai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, err
	}
	rerankModel = cm
	return cm, nil
}

// qaModelBuilder 定义模型构建依赖
type qaModelBuilder func(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error)

// qaConfigReader 定义配置读取依赖
type qaConfigReader func(ctx context.Context) (*qwen.ChatModelConfig, error)

// initQAModel 无状态初始化流程：读配置→构建模型→校验非 nil，生产与测试共用。
func initQAModel(ctx context.Context, readCfg qaConfigReader, build qaModelBuilder) (model.BaseChatModel, error) {
	cfg, err := readCfg(ctx)
	if err != nil {
		return nil, fmt.Errorf("read qa config: %w", err)
	}
	cm, err := build(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build qa model: %w", err)
	}
	if cm == nil {
		return nil, fmt.Errorf("qa model builder returned nil")
	}
	return cm, nil
}

// defaultQAConfigReader 生产默认配置读取
func defaultQAConfigReader(ctx context.Context) (*qwen.ChatModelConfig, error) {
	cfg := &qwen.ChatModelConfig{}
	v, err := g.Cfg().Get(ctx, "qa")
	if err != nil {
		return nil, fmt.Errorf("get qa config: %w", err)
	}
	if err := v.Scan(cfg); err != nil {
		return nil, fmt.Errorf("scan qa config: %w", err)
	}
	cfg.EnableThinking = Of(false)
	return cfg, nil
}

// defaultQAModelBuilder 生产默认模型构建
func defaultQAModelBuilder(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	return qwen.NewChatModel(ctx, cfg)
}

// qaModelCache 封装 QA 模型专用的锁、缓存状态与初始化流程（实例级）。
// 锁与缓存仅保护本实例；测试可创建独立实例并注入固定 reader/builder，
// 确定性验证失败不发布缓存、成功缓存、并发只构建一次；不新增可变包级测试替换函数。
type qaModelCache struct {
	mu      sync.Mutex
	model   model.BaseChatModel
	ready   bool
	readCfg qaConfigReader
	build   qaModelBuilder
}

// get 返回缓存的 QA 模型；未就绪时经 initQAModel 构建，成功后才发布缓存。
// 初始化失败不发布缓存（允许重试）。cfg != nil 时直接使用传入配置（保留原有行为）。
func (c *qaModelCache) get(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ready && c.model != nil {
		return c.model, nil
	}

	read := c.readCfg
	if cfg != nil {
		read = func(context.Context) (*qwen.ChatModelConfig, error) { return cfg, nil }
	}

	cm, err := initQAModel(ctx, read, c.build)
	if err != nil {
		return nil, err // 失败不发布缓存，允许重试
	}
	c.model = cm
	c.ready = true
	return cm, nil
}

// qaDefaultCache 生产默认实例；GetQAModel 委派给它。
var qaDefaultCache = &qaModelCache{
	readCfg: defaultQAConfigReader,
	build:   defaultQAModelBuilder,
}

// GetQAModel 公开入口，委派给生产默认实例；保留签名与成功缓存行为。
// Fix 5: cfg != nil 时直接构造，不查 Registry。
func GetQAModel(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	return GetQAModelWithRegistry(ctx, cfg, GetRegistry())
}

// GetQAModelWithRegistry 使用显式传入的 Registry 构造 QA 模型。
// Fix 4: 不依赖全局 Registry，避免初始化期间临时发布。
func GetQAModelWithRegistry(ctx context.Context, cfg *qwen.ChatModelConfig, reg providers.ProviderRegistry) (model.BaseChatModel, error) {
	// Fix 5: cfg != nil 时直接构造，不查 Registry
	if cfg != nil {
		return qaDefaultCache.get(ctx, cfg)
	}
	// cfg == nil: 优先使用显式传入的 Registry
	if reg != nil {
		chatRole, err := reg.GetQA(ctx)
		if err != nil {
			return nil, fmt.Errorf("registry GetQA: %w", err)
		}
		return chatRole, nil
	}
	return qaDefaultCache.get(ctx, nil)
}
