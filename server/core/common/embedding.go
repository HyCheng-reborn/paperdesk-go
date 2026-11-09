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
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/vector"
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

	// ErrEmbeddingModelOverride 调用方试图用 WithModel 改变已绑定模型身份。
	// P2-2：不得静默丢弃，必须拒绝。
	ErrEmbeddingModelOverride = errors.New("embedding: runtime model override rejected")
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
//
// Provider 分支的 modelID 同时就是实际随请求发送的模型值（shared
// providers.RequestModelID）：协议不能在请求中表达 "@version" 时 fail closed，
// 不再返回一个实际请求永远不会携带的版本身份。
func ResolveEmbeddingIdentity(conf *config.Config) (providerName, modelID string, dimensions int, err error) {
	if IsActiveProviderEmbedding(conf) {
		dims, dimErr := config.ResolveEmbeddingDimensions(conf.EmbeddingDimensions, conf.Provider)
		if dimErr != nil {
			return "", "", 0, fmt.Errorf("%w: provider dimensions: %w", ErrEmbeddingConfigInvalid, dimErr)
		}
		requestModel, idErr := providers.RequestModelID(conf.Provider.EmbeddingModel)
		if idErr != nil {
			return "", "", 0, fmt.Errorf("%w: provider model: %w", ErrEmbeddingConfigInvalid, idErr)
		}
		// 与 fingerprint / Registry Profile 同一规范化语义。
		return providers.NormalizeProviderName(conf.Provider.Name), requestModel, dims, nil
	}
	// Legacy path: 模型名按配置原样发送，身份与请求值相同。
	dims, dimErr := conf.ResolveDimensions()
	if dimErr != nil {
		return "", "", 0, dimErr
	}
	canonical, idErr := providers.CanonicalModelID(conf.EmbeddingModel)
	if idErr != nil {
		return "", "", 0, fmt.Errorf("%w: legacy model: %w", ErrEmbeddingConfigInvalid, idErr)
	}
	return "legacy", canonical, dims, nil
}

// ExpectedEmbeddingFingerprint 返回配置声明的 embedding 身份的唯一期望指纹。
//
// 它把“解析身份 → 构造完整指纹（version/model/dims/provider/similarity）”
// 收敛到单一实现：Rag 指纹门禁（core.ComputeFingerprintMeta）与直接入口
// （NewEmbedding）共用，避免不同入口各自维护字段子集（P2：曾遗漏 similarity）。
func ExpectedEmbeddingFingerprint(conf *config.Config) (*vector.Fingerprint, error) {
	providerName, modelID, dims, err := ResolveEmbeddingIdentity(conf)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, fmt.Errorf("embedding model not configured")
	}
	if dims <= 0 {
		return nil, fmt.Errorf("invalid embedding dimensions: %d", dims)
	}
	return vector.NewExpectedEmbeddingFingerprint(modelID, dims, providerName)
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
	guard := conf.VectorIOGuard
	if binding, bindErr := targetBinding(conf); bindErr == nil && binding != nil {
		// 受控路径：运行时校验必须来自可信绑定，而不是任意回调。
		guard = binding.Guard()
	}
	return &guardedEmbedder{
		Embedder:   eb,
		dimensions: dimensions,
		guard:      guard,
		modelID:    strings.TrimSpace(conf.EmbeddingModel),
	}, nil
}

// targetBinding 提取可信物理目标绑定；非 *vector.TargetBinding 一律视为未绑定。
func targetBinding(conf *config.Config) (*vector.TargetBinding, error) {
	if conf == nil || conf.PhysicalBinding == nil {
		return nil, fmt.Errorf("%w: physical target binding is required", ErrEmbeddingProfileNotReady)
	}
	binding, ok := conf.PhysicalBinding.(*vector.TargetBinding)
	if !ok || binding == nil {
		return nil, fmt.Errorf("%w: physical target binding has unexpected type %T", ErrEmbeddingProfileNotReady, conf.PhysicalBinding)
	}
	return binding, nil
}

// verifiedTargetBinding 返回构建期已校验的绑定：名称、资源与 fingerprint 自洽，
// 且重新读取具体目标确认未被替换。guard / 生命周期布尔 / 非空名称不再作为绑定证据。
func verifiedTargetBinding(ctx context.Context, conf *config.Config) (*vector.TargetBinding, error) {
	binding, err := targetBinding(conf)
	if err != nil {
		return nil, err
	}
	if err := binding.Check(conf.IndexName, conf.Client); err != nil {
		return nil, fmt.Errorf("%w: target binding: %w", ErrEmbeddingProfileNotReady, err)
	}
	if err := binding.Verify(ctx); err != nil {
		return nil, fmt.Errorf("%w: verify target binding: %w", ErrEmbeddingProfileNotReady, err)
	}
	return binding, nil
}

// checkBindingIdentity 用共享的完整指纹比较校验绑定身份。
//
// P2: 不再由本入口逐项比对 Provider/模型/维度（曾遗漏 similarity），
// 而是构造与 Rag 门禁完全一致的期望指纹，再交给 vector.VerifyFingerprint
// 比较全部字段（version/embedding_model_id/dimensions/provider_name/similarity）。
func checkBindingIdentity(binding *vector.TargetBinding, expected *vector.Fingerprint) error {
	stored := binding.Fingerprint()
	if err := vector.VerifyFingerprint(&stored, expected); err != nil {
		return fmt.Errorf("%w: %w", ErrEmbeddingIdentityMismatch, err)
	}
	return nil
}

// newProviderEmbedding constructs an embedder via conf.Registry (2C-3 active path).
func newProviderEmbedding(ctx context.Context, conf *config.Config) (embedding.Embedder, error) {
	// 1. Registry must be injected by factory — not global
	if conf.Registry == nil {
		return nil, fmt.Errorf("%w: conf.Registry is nil for provider embedding", ErrEmbeddingProfileNotReady)
	}

	// C3-R1: Readiness checks — controlled lifecycle required BEFORE any model HTTP call.
	if !conf.ManagedResourceLifecycle {
		return nil, fmt.Errorf("%w: ManagedResourceLifecycle is false (no controlled lifecycle)", ErrEmbeddingProfileNotReady)
	}
	if conf.IndexName == "" {
		return nil, fmt.Errorf("%w: IndexName is empty (no physical target)", ErrEmbeddingProfileNotReady)
	}

	// P1-3: 可信绑定（名称/资源/fingerprint 已由具体 ES 目标读取校验）替代
	// “非 nil guard + 生命周期布尔 + 非空名称”的自证。构建前完成一次重新读取。
	binding, err := verifiedTargetBinding(ctx, conf)
	if err != nil {
		return nil, err
	}

	// 2. Resolve expected identity and its complete fingerprint (shared with the
	// Rag gate), then verify the binding against every fingerprint field.
	expectedFP, err := ExpectedEmbeddingFingerprint(conf)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve identity: %w", ErrEmbeddingProfileNotReady, err)
	}
	expectedProvider := providers.NormalizeProviderName(expectedFP.ProviderName)
	expectedModel := expectedFP.EmbeddingModelID
	expectedDims := expectedFP.Dimensions
	if err := checkBindingIdentity(binding, expectedFP); err != nil {
		return nil, err
	}

	// 3. Get EmbedRole from Registry
	embedRole, err := conf.Registry.GetEmbedder(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: Registry.GetEmbedder: %w", ErrEmbeddingProfileNotReady, err)
	}
	if embedRole == nil {
		return nil, fmt.Errorf("%w: Registry.GetEmbedder returned nil", ErrEmbeddingProfileNotReady)
	}

	// C3-R2/P1-2: 身份元数据缺失即失败关闭（不得因为可选接口缺失而跳过校验）。
	profiled, ok := embedRole.(providers.ProfiledEmbedRole)
	if !ok {
		return nil, fmt.Errorf("%w: embedding role %T does not expose verifiable provider identity", ErrEmbeddingIdentityMismatch, embedRole)
	}
	profile := profiled.Profile()
	profileProvider := providers.NormalizeProviderName(profile.ProviderName)
	if profileProvider == "" {
		return nil, fmt.Errorf("%w: embedding role reports empty provider name", ErrEmbeddingIdentityMismatch)
	}
	if profileProvider != expectedProvider {
		return nil, fmt.Errorf("%w: provider name: role=%q expected=%q",
			ErrEmbeddingIdentityMismatch, profileProvider, expectedProvider)
	}

	// 模型/版本使用无歧义规范化后严格相等（拒绝 "m@" 与 "m" 互为别名）。
	roleModelID, err := providers.CanonicalModelID(embedRole.ModelID())
	if err != nil {
		return nil, fmt.Errorf("%w: role model identity: %w", ErrEmbeddingIdentityMismatch, err)
	}
	profileModelID, err := providers.CanonicalModelID(profile.ModelID())
	if err != nil {
		return nil, fmt.Errorf("%w: profile model identity: %w", ErrEmbeddingIdentityMismatch, err)
	}
	if roleModelID != profileModelID {
		return nil, fmt.Errorf("%w: role model=%q profile model=%q",
			ErrEmbeddingIdentityMismatch, roleModelID, profileModelID)
	}
	if roleModelID != expectedModel {
		return nil, fmt.Errorf("%w: model: role=%q expected=%q",
			ErrEmbeddingIdentityMismatch, roleModelID, expectedModel)
	}
	if profile.Dimensions != 0 && profile.Dimensions != expectedDims {
		return nil, fmt.Errorf("%w: profile dims=%d config dims=%d",
			ErrEmbeddingIdentityMismatch, profile.Dimensions, expectedDims)
	}
	if embedRole.Dimensions() != expectedDims {
		return nil, fmt.Errorf("%w: Registry dims=%d config dims=%d",
			ErrEmbeddingIdentityMismatch, embedRole.Dimensions(), expectedDims)
	}

	// 5. Wrap as guardedEmbedder (binding guard + dimension + finite check)
	return &guardedEmbedder{
		Embedder:   embedRole,
		dimensions: expectedDims,
		guard:      binding.Guard(),
		modelID:    expectedModel,
	}, nil
}

// guardedEmbedder wraps an embedding.Embedder with guard checks and response validation.
// Serves both legacy and new Provider paths (2C-3).
type guardedEmbedder struct {
	embedding.Embedder
	dimensions int
	guard      func(context.Context) error
	// modelID 是构建时绑定的模型身份（Provider 路径为 canonical 形式）。
	modelID string
}

func (e *guardedEmbedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	if e.guard != nil {
		if err := e.guard(ctx); err != nil {
			return nil, fmt.Errorf("vector target before embedding: %w", err)
		}
	}
	// C3-R2/P2-2: 拒绝会改变已绑定模型身份的选项（不再静默丢弃），
	// 无关选项按原顺序原样传递。
	filteredOpts, optErr := filterEmbeddingOptions(opts, e.modelID)
	if optErr != nil {
		return nil, optErr
	}
	vectors, err := e.Embedder.EmbedStrings(ctx, texts, filteredOpts...)
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

// filterEmbeddingOptions rejects options that would change the bound model identity
// and preserves all other options in their original order.
// C3-R2/P2-2: 已绑定模型不得被运行期改写；静默丢弃会隐藏错误。
func filterEmbeddingOptions(opts []embedding.Option, boundModelID string) ([]embedding.Option, error) {
	if len(opts) == 0 {
		return nil, nil
	}
	filtered := make([]embedding.Option, 0, len(opts))
	for _, opt := range opts {
		// 每个 option 单独探测，避免复用被前一个 option 修改过的 base
		// （GetCommonOptions 会就地修改 base，曾导致后续无关选项一同丢失）。
		var probe embedding.Options
		embedding.GetCommonOptions(&probe, opt)
		if probe.Model != nil {
			requested := strings.TrimSpace(*probe.Model)
			if requested != boundModelID {
				return nil, fmt.Errorf("%w: requested model %q, bound model %q", ErrEmbeddingModelOverride, requested, boundModelID)
			}
		}
		filtered = append(filtered, opt)
	}
	return filtered, nil
}
