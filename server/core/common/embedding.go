package common

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/wangle201210/go-rag/server/core/config"
)

// B2: Embedding 生产门控错误。
var (
	ErrEmbeddingGuardNotReady = errors.New("embedding: new profile requires Stage 2C guard (dimension fingerprint + mismatch blocking) before production use")
)

func NewEmbedding(ctx context.Context, conf *config.Config) (eb embedding.Embedder, err error) {
	// B2: Embedding guard — 新 Embedding profile 在 2C guard 不存在时 fail closed。
	// 区分两种情况：
	//   1. 未配置 Provider 或 Provider.EmbeddingModel 为空 → 继续旧路径（不拒绝）
	//   2. 明确配置了新 Embedding profile → 拒绝（2C guard 不存在）
	if conf != nil && conf.Provider != nil && conf.Provider.EmbeddingModel != "" {
		// 明确配置了新 Embedding profile → fail closed
		return nil, fmt.Errorf("%w: provider=%q model=%q",
			ErrEmbeddingGuardNotReady, conf.Provider.Name, conf.Provider.EmbeddingModel)
	}

	// B4: Registry 存在但 Embedding 未配置 → 继续旧路径（不委派 Registry）
	// 仅当 Provider 配置存在且 EmbeddingModel 非空时才尝试 Registry（上方已拒绝）

	// 旧路径：直接从 config 构建
	econf := &openai.EmbeddingConfig{
		APIKey:  conf.APIKey,
		Model:   conf.EmbeddingModel,
		Timeout: 0,
		BaseURL: conf.BaseURL,
	}
	if econf.APIKey == "" {
		econf.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if econf.BaseURL == "" {
		econf.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if econf.Model == "" {
		econf.Model = "text-embedding-3-large"
	}
	// Only request dimensions for models that support it. BGE-M3 already
	// returns 1024 dimensions and SiliconFlow rejects this optional parameter.
	if strings.HasPrefix(econf.Model, "text-embedding-3-") || strings.HasPrefix(econf.Model, "Qwen/Qwen3-Embedding-") {
		econf.Dimensions = Of(1024)
	}
	eb, err = openai.NewEmbedder(ctx, econf)
	if err != nil {
		return nil, err
	}
	return eb, nil
}
