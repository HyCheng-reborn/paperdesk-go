package common

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino-ext/components/model/qwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/gogf/gf/v2/frame/g"
)

var (
	embeddingModel model.BaseChatModel
	rerankModel    model.BaseChatModel
	rewriteModel   model.BaseChatModel
	chatModel      model.BaseChatModel

	qaModel      model.BaseChatModel
	qaModelMu    sync.Mutex
	qaModelReady bool
)

func GetChatModel(ctx context.Context, cfg *openai.ChatModelConfig) (model.BaseChatModel, error) {
	if chatModel != nil {
		return chatModel, nil
	}
	if cfg == nil {
		cfg = &openai.ChatModelConfig{}
		err := g.Cfg().MustGet(ctx, "chat").Scan(cfg)
		if err != nil {
			return nil, err
		}
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
	if rewriteModel != nil {
		return rewriteModel, nil
	}
	if cfg == nil {
		cfg = &qwen.ChatModelConfig{}
		err := g.Cfg().MustGet(ctx, "rewrite").Scan(cfg)
		cfg.EnableThinking = Of(false)
		if err != nil {
			return nil, err
		}
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

// initQAModel 实例级初始化器，生产与测试共用
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

// GetQAModel 公开入口
func GetQAModel(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	qaModelMu.Lock()
	defer qaModelMu.Unlock()

	if qaModelReady && qaModel != nil {
		return qaModel, nil
	}

	// 如果传入了 cfg，直接使用；否则从配置读取
	var readCfg qaConfigReader
	if cfg != nil {
		readCfg = func(ctx context.Context) (*qwen.ChatModelConfig, error) {
			return cfg, nil
		}
	} else {
		readCfg = defaultQAConfigReader
	}

	cm, err := initQAModel(ctx, readCfg, defaultQAModelBuilder)
	if err != nil {
		return nil, err
	}

	qaModel = cm
	qaModelReady = true
	return cm, nil
}
