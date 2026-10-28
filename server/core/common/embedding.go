package common

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/wangle201210/go-rag/server/core/config"
)

// 2C-3: Embedding profile 错误哨兵。
var (
	// ErrEmbeddingProfileNotReady profile 已配置但无法构造（Registry 不可用、适配器构建失败等）。
	ErrEmbeddingProfileNotReady = errors.New("embedding: profile configured but not constructible")

	// ErrEmbeddingIdentityMismatch Registry 返回的模型身份或维度与配置解析不一致。
	ErrEmbeddingIdentityMismatch = errors.New("embedding: identity or dimensions mismatch between Registry and config resolution")

	// ErrEmbeddingConfigInvalid 配置验证失败（缺少 API key、无效维度等）。
	ErrEmbeddingConfigInvalid = errors.New("embedding: invalid configuration")

	// ErrEmbeddingGuardNotReady 保留用于向后兼容的测试引用；生产代码不再使用。
	// Deprecated: 使用 ErrEmbeddingProfileNotReady 代替。
	ErrEmbeddingGuardNotReady = ErrEmbeddingProfileNotReady
)

// IsActiveProviderEmbedding reports whether the config selects the Provider
// Embedding branch. This is ONLY branch selection — it does not prove
// identity, dimensions, Registry, or physical binding are ready.
func IsActiveProviderEmbedding(conf *config.Config) bool {
	return conf != nil && conf.Provider != nil && conf.Provider.EmbeddingModel != ""
}

// HasProviderChatModel reports whether the config selects a Provider Chat model.
func HasProviderChatModel(conf *config.Config) bool {
	return conf != nil && conf.Provider != nil && conf.Provider.ChatModel != ""
}

// ResolveEmbeddingIdentity returns (providerName, modelID, dimensions) using
// the same branch condition as ComputeFingerprintMeta.
// Both legacy and Provider branches validate through ResolveEmbeddingDimensions.
func ResolveEmbeddingIdentity(conf *config.Config) (providerName, modelID string, dimensions int, err error) {
	if IsActiveProviderEmbedding(conf) {
		dims, dimErr := config.ResolveEmbeddingDimensions(conf.EmbeddingDimensions, conf.Provider)
		if dimErr != nil {
			return "", "", 0, fmt.Errorf("%w: provider dimensions: %v", ErrEmbeddingConfigInvalid, dimErr)
		}
		return conf.Provider.Name, conf.Provider.EmbeddingModel, dims, nil
	}
	// Legacy path
	dims, dimErr := conf.ResolveDimensions()
	if dimErr != nil {
		return "", "", 0, dimErr
	}
	return "legacy", conf.EmbeddingModel, dims, nil
}

func NewEmbedding(ctx context.Context, conf *config.Config) (eb embedding.Embedder, err error) {
	// 2C-3: Active Provider path — via conf.Registry.GetEmbedder
	if IsActiveProviderEmbedding(conf) {
		return newProviderEmbedding(ctx, conf)
	}

	// 旧路径：直接从 config 构建
	dimensions, err := conf.ResolveDimensions()
	if err != nil {
		return nil, err
	}
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
		econf.Dimensions = Of(dimensions)
	}
	eb, err = openai.NewEmbedder(ctx, econf)
	if err != nil {
		return nil, err
	}
	return &guardedEmbedder{Embedder: eb, dimensions: dimensions, guard: conf.VectorIOGuard}, nil
}

// newProviderEmbedding constructs an embedder via conf.Registry (2C-3 active path).
func newProviderEmbedding(ctx context.Context, conf *config.Config) (embedding.Embedder, error) {
	// 1. Registry must be injected by factory — not global
	if conf.Registry == nil {
		return nil, fmt.Errorf("%w: conf.Registry is nil for provider embedding", ErrEmbeddingProfileNotReady)
	}

	// 2. Resolve expected identity from config
	_, expectedModel, expectedDims, err := ResolveEmbeddingIdentity(conf)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve identity: %v", ErrEmbeddingProfileNotReady, err)
	}

	// 3. Get EmbedRole from Registry
	embedRole, err := conf.Registry.GetEmbedder(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: Registry.GetEmbedder: %v", ErrEmbeddingProfileNotReady, err)
	}
	if embedRole == nil {
		return nil, fmt.Errorf("%w: Registry.GetEmbedder returned nil", ErrEmbeddingProfileNotReady)
	}

	// 4. Verify identity: model ID and dimensions must match
	roleModelID := embedRole.ModelID()
	// Compare using Profile.Model + Version semantics:
	// expectedModel is the raw model name from config; roleModelID may include @version.
	// We check that roleModelID starts with expectedModel (allowing @version suffix).
	if roleModelID != expectedModel && !strings.HasPrefix(roleModelID, expectedModel+"@") {
		return nil, fmt.Errorf("%w: Registry model=%q config model=%q",
			ErrEmbeddingIdentityMismatch, roleModelID, expectedModel)
	}
	if embedRole.Dimensions() != expectedDims {
		return nil, fmt.Errorf("%w: Registry dims=%d config dims=%d",
			ErrEmbeddingIdentityMismatch, embedRole.Dimensions(), expectedDims)
	}

	// 5. Wrap as guardedEmbedder (guard before/after + dimension + finite check)
	// Provider name is bound via fingerprint (ComputeFingerprintMeta uses same ResolveEmbeddingIdentity).
	// Model ID + dimensions mismatch check above is the identity gate.
	return &guardedEmbedder{
		Embedder:   embedRole,
		dimensions: expectedDims,
		guard:      conf.VectorIOGuard,
	}, nil
}

// guardedEmbedder wraps an embedding.Embedder with guard checks and response validation.
// Serves both legacy and new Provider paths (2C-3).
type guardedEmbedder struct {
	embedding.Embedder
	dimensions int
	guard      func(context.Context) error
}

func (e *guardedEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if e.guard != nil {
		if err := e.guard(ctx); err != nil {
			return nil, fmt.Errorf("vector target before embedding: %w", err)
		}
	}
	vectors, err := e.Embedder.EmbedStrings(ctx, texts, opts...)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("%w: response count %d != input count %d", config.ErrEmbeddingDimensions, len(vectors), len(texts))
	}
	for i, v := range vectors {
		if len(v) != e.dimensions {
			return nil, fmt.Errorf("%w: response[%d] dims=%d configured=%d", config.ErrEmbeddingDimensions, i, len(v), e.dimensions)
		}
		for j, f := range v {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fmt.Errorf("%w: response[%d][%d] non-finite value", config.ErrEmbeddingDimensions, i, j)
			}
		}
	}
	// E3: do not hand vectors to Store/query if identity changed during embedding.
	if e.guard != nil {
		if err := e.guard(ctx); err != nil {
			return nil, fmt.Errorf("vector target after embedding: %w", err)
		}
	}
	return vectors, nil
}
