package providers

import (
	"context"
	"errors"
	"fmt"

	embopenai "github.com/cloudwego/eino-ext/components/embedding/openai"
	chatopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/rerank"
)

// 适配层错误。
var (
	ErrNilProfile   = errors.New("providers: nil ProviderProfile")
	ErrNilTransport = errors.New("providers: nil Transport")
	ErrNilInner     = errors.New("providers: inner model must not be nil")
)

// BuildConfig 汇集构造角色适配器所需的不可变 Profile、临时凭据与共享传输。
//
// 设计要点（stage2-plan §3.3.1）：APIKey 明文不进入 Profile，仅在构造期经此临时传递；
// Transport 在同一 Provider 的各角色间共享（生产共享链），复用连接池。
// 构造过程不发起网络请求（Eino 的 NewChatModel/NewEmbedder 仅装配 client）。
type BuildConfig struct {
	Profile   *ProviderProfile
	APIKey    string
	Transport *Transport
}

func (c BuildConfig) validate() error {
	if c.Profile == nil {
		return ErrNilProfile
	}
	if c.Transport == nil {
		return ErrNilTransport
	}
	return nil
}

// ---------------------------------------------------------------------------
// ChatRole 适配：现有 openai.NewChatModel → ChatRole
// ---------------------------------------------------------------------------

// chatAdapter 将 Eino model.BaseChatModel 适配为 ChatRole。
// 采用嵌入 Eino 接口方式（stage2-plan §3.4.1）：Generate/Stream 由内层实例直接提升，
// 签名与 ChatRole 完全一致，零转换开销，不改变 Stage 1 的 Eino 语义。
type chatAdapter struct {
	model.BaseChatModel
	profile *ProviderProfile
}

// 编译期断言：chatAdapter 同时满足 ChatRole 与 Eino model.BaseChatModel。
var (
	_ ChatRole            = (*chatAdapter)(nil)
	_ model.BaseChatModel = (*chatAdapter)(nil)
)

// Profile 返回构造该角色的不可变 Profile 的防御性副本（值类型，KeyOptions 深拷贝），
// 绝不暴露可写内部引用（stage2 复核 B3）。
func (a *chatAdapter) Profile() ProviderProfile { return a.profile.snapshot() }

// Capabilities 返回 Profile 声明的能力集。
func (a *chatAdapter) Capabilities() Capabilities { return a.profile.Capabilities }

// BuildChatRole 是“现有模型 → ChatRole”的生产共享构造入口：
// 内部使用现有 openai.NewChatModel，并注入共享 Transport 的 *http.Client。
func BuildChatRole(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	p := cfg.Profile
	cm, err := chatopenai.NewChatModel(ctx, &chatopenai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		BaseURL:    p.Endpoint,
		Model:      p.Model,
		HTTPClient: cfg.Transport.Client(), // 复用统一 transport
	})
	if err != nil {
		return nil, fmt.Errorf("providers: build chat model: %w", err)
	}
	if cm == nil {
		return nil, fmt.Errorf("providers: build chat model: %w", ErrNilInner)
	}
	return &chatAdapter{BaseChatModel: cm, profile: p.Clone()}, nil
}

// WrapChatModel 将任意已构造的 Eino model.BaseChatModel 包装为 ChatRole（生产共享）。
// 用于复用现有构造器（如 qwen.NewChatModel）产出的实例，统一为角色接口。
func WrapChatModel(inner model.BaseChatModel, p *ProviderProfile) (ChatRole, error) {
	if inner == nil {
		return nil, ErrNilInner
	}
	if p == nil {
		return nil, ErrNilProfile
	}
	return &chatAdapter{BaseChatModel: inner, profile: p.Clone()}, nil
}

// ---------------------------------------------------------------------------
// EmbedRole 适配：现有 openai.NewEmbedder → EmbedRole
// ---------------------------------------------------------------------------

// embedAdapter 将 Eino embedding.Embedder 适配为 EmbedRole，
// 额外提供 Dimensions/ModelID（由 Profile 声明，供 2C 索引 fingerprint 使用）。
type embedAdapter struct {
	inner   embedding.Embedder
	profile *ProviderProfile
}

var _ EmbedRole = (*embedAdapter)(nil)

// EmbedStrings 委托内层 Eino Embedder（签名一致）。
func (a *embedAdapter) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	return a.inner.EmbedStrings(ctx, texts, opts...)
}

// Dimensions 返回 Profile 声明的输出维度。
func (a *embedAdapter) Dimensions() int { return a.profile.Dimensions }

// ModelID 返回 Profile 声明的模型身份（model + version）。
func (a *embedAdapter) ModelID() string { return a.profile.ModelID() }

// Profile 返回构造该角色的不可变 Profile 的防御性副本（值类型，KeyOptions 深拷贝）。
func (a *embedAdapter) Profile() ProviderProfile { return a.profile.snapshot() }

// BuildEmbedRole 是“现有模型 → EmbedRole”的生产共享构造入口：
// 内部使用现有 openai.NewEmbedder，并注入共享 Transport 的 *http.Client。
func BuildEmbedRole(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	p := cfg.Profile
	ecfg := &embopenai.EmbeddingConfig{
		APIKey:     cfg.APIKey,
		Model:      p.Model,
		BaseURL:    p.Endpoint,
		HTTPClient: cfg.Transport.Client(), // 复用统一 transport
	}
	if p.Dimensions > 0 {
		d := p.Dimensions
		ecfg.Dimensions = &d
	}
	eb, err := embopenai.NewEmbedder(ctx, ecfg)
	if err != nil {
		return nil, fmt.Errorf("providers: build embedder: %w", err)
	}
	if eb == nil {
		return nil, fmt.Errorf("providers: build embedder: %w", ErrNilInner)
	}
	return &embedAdapter{inner: eb, profile: p.Clone()}, nil
}

// WrapEmbedder 将任意已构造的 Eino embedding.Embedder 包装为 EmbedRole（生产共享）。
func WrapEmbedder(inner embedding.Embedder, p *ProviderProfile) (EmbedRole, error) {
	if inner == nil {
		return nil, ErrNilInner
	}
	if p == nil {
		return nil, ErrNilProfile
	}
	return &embedAdapter{inner: inner, profile: p.Clone()}, nil
}

// ---------------------------------------------------------------------------
// RerankRole 适配：现有 rerank.Rerank → RerankRole
// ---------------------------------------------------------------------------

// rerankAdapter 复用 Stage 1B-1 已验收的 core/rerank.Rerank 校验链
// （输入隔离、结果校验、响应体限制、有界超时、错误链），仅附加 Profile。
type rerankAdapter struct {
	inner   *rerank.Rerank
	profile *ProviderProfile
}

var _ RerankRole = (*rerankAdapter)(nil)

// Rerank 委托内层已验收实现（签名与 RerankRole 一致）。
func (a *rerankAdapter) Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error) {
	return a.inner.Rerank(ctx, query, docs, topK)
}

// Profile 返回构造该角色的不可变 Profile 的防御性副本（值类型，KeyOptions 深拷贝）。
func (a *rerankAdapter) Profile() ProviderProfile { return a.profile.snapshot() }

// BuildRerankRole 是“现有 rerank.Rerank → RerankRole”的生产共享构造入口：
// 复用 core/rerank 的全部已验收校验链，并注入共享 Transport 的 *http.Client。
func BuildRerankRole(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	_ = ctx // rerank.NewRerank 不需要 ctx；构造不发起网络请求
	p := cfg.Profile
	rr := rerank.NewRerank(&rerank.RerankConfig{
		URL:    p.Endpoint,
		APIKey: cfg.APIKey,
		Model:  p.Model,
	}, cfg.Transport.Client()) // HTTPDoer 由 *http.Client 满足，复用统一 transport
	if rr == nil {
		return nil, fmt.Errorf("providers: build rerank: %w", ErrNilInner)
	}
	return &rerankAdapter{inner: rr, profile: p.Clone()}, nil
}

// WrapRerank 将已构造的 *rerank.Rerank 包装为 RerankRole（生产共享）。
func WrapRerank(inner *rerank.Rerank, p *ProviderProfile) (RerankRole, error) {
	if inner == nil {
		return nil, ErrNilInner
	}
	if p == nil {
		return nil, ErrNilProfile
	}
	return &rerankAdapter{inner: inner, profile: p.Clone()}, nil
}
