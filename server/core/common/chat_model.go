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

func GetQAModel(ctx context.Context, cfg *qwen.ChatModelConfig) (model.BaseChatModel, error) {
	qaModelMu.Lock()
	if qaModelReady && qaModel != nil {
		m := qaModel
		qaModelMu.Unlock()
		return m, nil
	}

	// 初始化
	if cfg == nil {
		cfg = &qwen.ChatModelConfig{}
		v := g.Cfg().MustGet(ctx, "qa")
		if err := v.Scan(cfg); err != nil {
			qaModelMu.Unlock()
			return nil, fmt.Errorf("scan qa config: %w", err)
		}
		cfg.EnableThinking = Of(false)
	}

	cm, err := qwen.NewChatModel(ctx, cfg)
	if err != nil {
		qaModelMu.Unlock()
		return nil, fmt.Errorf("create qa model: %w", err)
	}

	qaModel = cm
	qaModelReady = true
	qaModelMu.Unlock()
	return cm, nil
}
