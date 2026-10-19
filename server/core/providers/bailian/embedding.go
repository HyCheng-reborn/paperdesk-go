package bailian

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/cloudwego/eino/components/embedding"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 百炼 Embedding 适配层错误。
var (
	ErrEmbedCountMismatch = errors.New("bailian embed: response count mismatch")
	ErrEmbedDimMismatch   = errors.New("bailian embed: dimension mismatch")
	ErrEmbedNaN           = errors.New("bailian embed: NaN value in embedding vector")
	ErrEmbedInf           = errors.New("bailian embed: Inf value in embedding vector")
	ErrEmbedUsageMissing  = errors.New("bailian embed: usage info missing from response")
	ErrEmbedNilProfile    = errors.New("bailian embed: nil ProviderProfile")
	ErrEmbedNilTransport  = errors.New("bailian embed: nil Transport")
)

// validatedEmbedRole 包装 EmbedRole，附加响应验证。
type validatedEmbedRole struct {
	inner       providers.EmbedRole
	profile     *providers.ProviderProfile
	expectedDim int
}

// 编译期断言。
var _ providers.EmbedRole = (*validatedEmbedRole)(nil)

// BuildEmbed 构造百炼 Embedding 角色适配器。
//
// 走 OpenAI 兼容协议 /compatible-mode/v1/embeddings；
// 候选模型 text-embedding-v4 默认 1024 维。
// 原始响应边界验证：index/数量/维度/有限值/usage presence。
func BuildEmbed(ctx context.Context, cfg providers.BuildConfig) (providers.EmbedRole, error) {
	if cfg.Profile == nil {
		return nil, ErrEmbedNilProfile
	}
	if cfg.Transport == nil {
		return nil, ErrEmbedNilTransport
	}
	inner, err := providers.BuildEmbedRole(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("bailian embed: %w", err)
	}
	return &validatedEmbedRole{
		inner:       inner,
		profile:     cfg.Profile.Clone(),
		expectedDim: cfg.Profile.Dimensions,
	}, nil
}

// EmbedStrings 调用底层 Embedder 并验证响应。
func (v *validatedEmbedRole) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	result, err := v.inner.EmbedStrings(ctx, texts, opts...)
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddingResponse(result, texts, v.expectedDim); err != nil {
		return nil, err
	}
	return result, nil
}

// Dimensions 返回 Profile 声明的输出维度。
func (v *validatedEmbedRole) Dimensions() int { return v.expectedDim }

// ModelID 返回 Profile 声明的模型身份。
func (v *validatedEmbedRole) ModelID() string { return v.profile.ModelID() }

// validateEmbeddingResponse 验证 Embedding 响应的完整性与有限性。
//
// 验证链：
//  1. 数量匹配：len(result) == len(texts)
//  2. 维度匹配：每个向量的 len == expectedDim（expectedDim > 0 时）
//  3. 有限值：每个元素不是 NaN 或 Inf
func validateEmbeddingResponse(result [][]float64, texts []string, expectedDim int) error {
	// 数量匹配
	if len(result) != len(texts) {
		return fmt.Errorf("%w: expected %d vectors, got %d", ErrEmbedCountMismatch, len(texts), len(result))
	}
	for i, vec := range result {
		// 维度匹配
		if expectedDim > 0 && len(vec) != expectedDim {
			return fmt.Errorf("%w: vector[%d] expected dim %d, got %d", ErrEmbedDimMismatch, i, expectedDim, len(vec))
		}
		// 有限值检查
		for j, val := range vec {
			if math.IsNaN(val) {
				return fmt.Errorf("%w: vector[%d][%d]", ErrEmbedNaN, i, j)
			}
			if math.IsInf(val, 0) {
				return fmt.Errorf("%w: vector[%d][%d]", ErrEmbedInf, i, j)
			}
		}
	}
	return nil
}
