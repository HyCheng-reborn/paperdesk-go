package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Role 标识模型角色（stage2-plan §2）。Extract 作为 ChatRole 的角色别名（§3.4.4）。
type Role int

const (
	RoleChat Role = iota
	RoleRewrite
	RoleQA
	RoleExtract
	RoleEmbed
	RoleRerank
)

func (r Role) String() string {
	switch r {
	case RoleChat:
		return "chat"
	case RoleRewrite:
		return "rewrite"
	case RoleQA:
		return "qa"
	case RoleExtract:
		return "extract"
	case RoleEmbed:
		return "embed"
	case RoleRerank:
		return "rerank"
	default:
		return "unknown"
	}
}

// ProviderRegistry 是业务层访问模型的统一入口（stage2-plan §3.1）。
// 业务代码通过角色接口访问模型，Eino 类型不直接暴露给 rag.go 以上的业务层。
type ProviderRegistry interface {
	GetChat(ctx context.Context) (ChatRole, error)
	GetRewrite(ctx context.Context) (ChatRole, error)
	GetQA(ctx context.Context) (ChatRole, error)
	GetExtract(ctx context.Context) (ChatRole, error)
	GetEmbedder(ctx context.Context) (EmbedRole, error)
	GetReranker(ctx context.Context) (RerankRole, error)
	// NewRequestBudget 从绑定的 ProcessBudget 创建请求级预算（B1 接入）。
	NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error)
}

// 编译期断言：默认实现满足接口。
var _ ProviderRegistry = (*registry)(nil)

// Registry 相关错误。
var (
	ErrRoleNotConfigured = errors.New("providers: role not configured")
	ErrNilBudget         = errors.New("providers: ProcessBudget must not be nil")
	ErrNilCredentialFn   = errors.New("providers: CredentialResolver must not be nil")
)

// CredentialResolver 按 Profile 解析 API Key 明文（凭据不进入 Profile）。
// 生产实现从配置/环境变量读取（2B 接线）；解析失败必须向上传播，不静默回退（§4.10）。
type CredentialResolver func(ctx context.Context, p *ProviderProfile) (string, error)

// RegistryConfig 是构造 ProviderRegistry 的不可变输入。
// 每个角色可绑定独立 Profile；未显式绑定的 Chat 家族角色回退到 Chat Profile
// （Extract 与 Chat 共享配置，见 §3.4.4）。
type RegistryConfig struct {
	Chat        *ProviderProfile
	Rewrite     *ProviderProfile
	QA          *ProviderProfile
	Extract     *ProviderProfile
	Embed       *ProviderProfile
	Rerank      *ProviderProfile
	Budget      *ProcessBudget     // 进程预算，Registry 构造时绑定（§3.3.1）
	Transport   *Transport         // 共享传输；nil 时使用 NewTransport() 默认实例
	Credentials CredentialResolver // 凭据解析（必填）
}

// registry 是 ProviderRegistry 的默认实现。
//
// 模型缓存 per Profile 隔离：chat/embed/rerank 各持一张以 Profile.CacheKey() 为键的缓存表，
// 因此相同 Profile 的角色（如 Chat 与 Extract 共享 Profile）复用同一实例，
// 不同 Profile 得到相互独立的实例，绝不共享。
//
// default 构造只发布完整初始化对象：构建成功（非 nil）才写入缓存；
// 凭据解析失败或构建失败均返回错误且不写缓存（允许后续重试），
// 与既有 qaModelCache 的“失败不发布缓存”契约一致。
type registry struct {
	mu          sync.Mutex // 保护三张缓存表；构建在持锁期间完成 → 并发只构建一次
	chatCache   map[string]ChatRole
	embedCache  map[string]EmbedRole
	rerankCache map[string]RerankRole

	chatProfile    *ProviderProfile
	rewriteProfile *ProviderProfile
	qaProfile      *ProviderProfile
	extractProfile *ProviderProfile
	embedProfile   *ProviderProfile
	rerankProfile  *ProviderProfile

	budget    *ProcessBudget
	transport *Transport
	creds     CredentialResolver
}

// NewRegistry 构造默认 ProviderRegistry。构造过程不发起网络请求。
// 校验：Budget 与 Credentials 必填（§3.5 缺失即报错，不延迟到首次调用）。
// B6: 首次成功构建后冻结 builder 注册表。
func NewRegistry(cfg RegistryConfig) (ProviderRegistry, error) {
	if cfg.Budget == nil {
		return nil, ErrNilBudget
	}
	if cfg.Credentials == nil {
		return nil, ErrNilCredentialFn
	}
	tp := cfg.Transport
	if tp == nil {
		tp = NewTransport()
	}
	// 防御性快照（stage2 复核 B3）：Registry 拥有经校验 Profile 的独立副本，
	// 构造后调用方修改原对象不影响已发布身份。未显式绑定的 Chat 家族角色
	// 回退到 Chat 的同一快照（共享指针 → 复用同一缓存实例）。
	chatProfile := cfg.Chat.Clone()
	rewriteProfile := chatProfile
	if cfg.Rewrite != nil {
		rewriteProfile = cfg.Rewrite.Clone()
	}
	qaProfile := chatProfile
	if cfg.QA != nil {
		qaProfile = cfg.QA.Clone()
	}
	extractProfile := chatProfile
	if cfg.Extract != nil {
		extractProfile = cfg.Extract.Clone()
	}
	// B6: 冻结 builder 注册表（首次构建后拒绝新注册）
	freezeBuilders()
	return &registry{
		chatCache:      map[string]ChatRole{},
		embedCache:     map[string]EmbedRole{},
		rerankCache:    map[string]RerankRole{},
		chatProfile:    chatProfile,
		rewriteProfile: rewriteProfile,
		qaProfile:      qaProfile,
		extractProfile: extractProfile,
		embedProfile:   cfg.Embed.Clone(),
		rerankProfile:  cfg.Rerank.Clone(),
		budget:         cfg.Budget,
		transport:      tp,
		creds:          cfg.Credentials,
	}, nil
}

// Budget 返回绑定的进程预算（供调用方派生 RequestBudget/TaskBudget）。
func (r *registry) Budget() *ProcessBudget { return r.budget }

// NewRequestBudget 从绑定的 ProcessBudget 创建请求级预算（B1 接入）。
func (r *registry) NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error) {
	return r.budget.NewRequestBudget(ctx, opts...)
}

// Transport 返回共享传输层。
func (r *registry) Transport() *Transport { return r.transport }

func (r *registry) chatProfileFor(role Role) *ProviderProfile {
	switch role {
	case RoleRewrite:
		return r.rewriteProfile
	case RoleQA:
		return r.qaProfile
	case RoleExtract:
		return r.extractProfile
	default: // RoleChat
		return r.chatProfile
	}
}

// GetChat 返回 Chat 角色。
func (r *registry) GetChat(ctx context.Context) (ChatRole, error) {
	return r.getChatRole(ctx, RoleChat)
}

// GetRewrite 返回 Rewrite 角色（ChatRole）。
func (r *registry) GetRewrite(ctx context.Context) (ChatRole, error) {
	return r.getChatRole(ctx, RoleRewrite)
}

// GetQA 返回 QA 角色（ChatRole）。
func (r *registry) GetQA(ctx context.Context) (ChatRole, error) {
	return r.getChatRole(ctx, RoleQA)
}

// GetExtract 返回 Extract 角色（ChatRole）；与 Chat 共享 Profile 时返回同一实例。
func (r *registry) GetExtract(ctx context.Context) (ChatRole, error) {
	return r.getChatRole(ctx, RoleExtract)
}

// getChatRole 是 Chat 家族角色的统一 get-or-build 路径。
// 持 r.mu 完成“查缓存→解析凭据→构建→发布”，保证并发只构建一次、失败不发布。
func (r *registry) getChatRole(ctx context.Context, role Role) (ChatRole, error) {
	p := r.chatProfileFor(role)
	if p == nil {
		return nil, fmt.Errorf("%w: %s", ErrRoleNotConfigured, role)
	}
	key := p.CacheKey()

	r.mu.Lock()
	defer r.mu.Unlock()

	if c, ok := r.chatCache[key]; ok {
		return c, nil // 命中缓存（per Profile 隔离）
	}
	role1, err := r.buildChat(ctx, p)
	if err != nil {
		return nil, err // 失败不发布缓存，允许重试
	}
	r.chatCache[key] = role1
	return role1, nil
}

func (r *registry) buildChat(ctx context.Context, p *ProviderProfile) (ChatRole, error) {
	apiKey, err := r.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	// Provider-specific builder hook (2B: bailian dispatch)
	if b, ok := lookupChatBuilder(p.ProviderName); ok {
		role, err := b(ctx, BuildConfig{Profile: p, APIKey: apiKey, Transport: r.transport, Budget: r.budget})
		if err != nil {
			return nil, err
		}
		if role == nil {
			return nil, fmt.Errorf("providers: %s chat builder returned nil for %s", p.ProviderName, p.ModelID())
		}
		return role, nil
	}
	// B6: 未知 Provider 返回明确错误
	return nil, fmt.Errorf("%w: no chat builder for provider %q (known: %v)", ErrUnknownProvider, p.ProviderName, knownProviderNames())
}

// GetEmbedder 返回 Embedding 角色。
func (r *registry) GetEmbedder(ctx context.Context) (EmbedRole, error) {
	p := r.embedProfile
	if p == nil {
		return nil, fmt.Errorf("%w: embed", ErrRoleNotConfigured)
	}
	key := p.CacheKey()

	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.embedCache[key]; ok {
		return e, nil
	}
	apiKey, err := r.resolve(ctx, p)
	if err != nil {
		return nil, err // 失败不发布缓存
	}
	var emb EmbedRole
	// Provider-specific builder hook (2B: bailian dispatch)
	if b, ok := lookupEmbedBuilder(p.ProviderName); ok {
		emb, err = b(ctx, BuildConfig{Profile: p, APIKey: apiKey, Transport: r.transport, Budget: r.budget})
	} else {
		// B6: 未知 Provider 返回明确错误
		return nil, fmt.Errorf("%w: no embed builder for provider %q (known: %v)", ErrUnknownProvider, p.ProviderName, knownProviderNames())
	}
	if err != nil {
		return nil, err // 失败不发布缓存
	}
	if emb == nil {
		return nil, fmt.Errorf("providers: embed builder returned nil for %s", p.ModelID())
	}
	r.embedCache[key] = emb
	return emb, nil
}

// GetReranker 返回 Rerank 角色。
func (r *registry) GetReranker(ctx context.Context) (RerankRole, error) {
	p := r.rerankProfile
	if p == nil {
		return nil, fmt.Errorf("%w: rerank", ErrRoleNotConfigured)
	}
	key := p.CacheKey()

	r.mu.Lock()
	defer r.mu.Unlock()

	if rr, ok := r.rerankCache[key]; ok {
		return rr, nil
	}
	apiKey, err := r.resolve(ctx, p)
	if err != nil {
		return nil, err // 失败不发布缓存
	}
	var rk RerankRole
	// Provider-specific builder hook (2B: bailian dispatch)
	if b, ok := lookupRerankBuilder(p.ProviderName); ok {
		rk, err = b(ctx, BuildConfig{Profile: p, APIKey: apiKey, Transport: r.transport, Budget: r.budget})
	} else {
		// B6: 未知 Provider 返回明确错误
		return nil, fmt.Errorf("%w: no rerank builder for provider %q (known: %v)", ErrUnknownProvider, p.ProviderName, knownProviderNames())
	}
	if err != nil {
		return nil, err // 失败不发布缓存
	}
	if rk == nil {
		return nil, fmt.Errorf("providers: rerank builder returned nil for %s", p.ModelID())
	}
	r.rerankCache[key] = rk
	return rk, nil
}

// resolve 经凭据解析器获取 API Key；解析失败向上传播，不静默回退（§4.10）。
//
// 安全边界（stage2 复核 R2）：向回调传入 Profile 的深拷贝（Clone），而非 Registry
// 内部身份指针。回调对 Profile 字段或 KeyOptions map 的任何改写只作用于其私有副本，
// 绝不影响 Registry 内部已发布身份，也不会造成"key 先算、模型后建"下错误身份被
// 发布在旧 key 下的问题。单点修复 resolve 即覆盖 chat/embed/rerank 三条构建路径。
func (r *registry) resolve(ctx context.Context, p *ProviderProfile) (string, error) {
	apiKey, err := r.creds(ctx, p.Clone())
	if err != nil {
		return "", fmt.Errorf("providers: resolve credential for %s: %w", p.ModelID(), err)
	}
	return apiKey, nil
}

// ---------------------------------------------------------------------------
// Provider-specific builder hooks (2B: 百炼适配层注册)
// ---------------------------------------------------------------------------

// ChatBuilder 是 Provider 特有的 Chat 角色构造函数。
// 注册后，Registry 在构建 Chat 角色时，若 Profile.ProviderName 匹配，则调用该函数。
type ChatBuilder func(ctx context.Context, cfg BuildConfig) (ChatRole, error)

// EmbedBuilder 是 Provider 特有的 Embed 角色构造函数。
type EmbedBuilder func(ctx context.Context, cfg BuildConfig) (EmbedRole, error)

// RerankBuilder 是 Provider 特有的 Rerank 角色构造函数。
type RerankBuilder func(ctx context.Context, cfg BuildConfig) (RerankRole, error)

// builderMu 保护三张 builder map 的并发读写（B6 修复）。
// init() 注册与首次构建时读取可能并发，必须加锁。
var builderMu sync.RWMutex

var (
	chatBuilders   = map[string]ChatBuilder{}
	embedBuilders  = map[string]EmbedBuilder{}
	rerankBuilders = map[string]RerankBuilder{}
	buildersFrozen bool // 首次 Registry 构建后冻结，拒绝新注册
)

// builder 注册/读取错误。
var (
	ErrBuilderFrozen    = errors.New("providers: builder registry frozen after first Registry build")
	ErrNilBuilder       = errors.New("providers: builder must not be nil")
	ErrDuplicateBuilder = errors.New("providers: builder already registered")
	ErrUnknownProvider  = errors.New("providers: unknown provider")
)

// RegisterChatBuilder 注册 Provider 特有的 Chat 构造函数。
// 通常在 Provider 适配包的 init() 中调用。
// B6: 冻结后拒绝注册；nil builder 拒绝；重复注册拒绝。
func RegisterChatBuilder(providerName string, b ChatBuilder) error {
	if b == nil {
		return ErrNilBuilder
	}
	builderMu.Lock()
	defer builderMu.Unlock()
	if buildersFrozen {
		return ErrBuilderFrozen
	}
	if _, exists := chatBuilders[providerName]; exists {
		return fmt.Errorf("%w: chat builder for %q", ErrDuplicateBuilder, providerName)
	}
	chatBuilders[providerName] = b
	return nil
}

// RegisterEmbedBuilder 注册 Provider 特有的 Embed 构造函数。
func RegisterEmbedBuilder(providerName string, b EmbedBuilder) error {
	if b == nil {
		return ErrNilBuilder
	}
	builderMu.Lock()
	defer builderMu.Unlock()
	if buildersFrozen {
		return ErrBuilderFrozen
	}
	if _, exists := embedBuilders[providerName]; exists {
		return fmt.Errorf("%w: embed builder for %q", ErrDuplicateBuilder, providerName)
	}
	embedBuilders[providerName] = b
	return nil
}

// RegisterRerankBuilder 注册 Provider 特有的 Rerank 构造函数。
func RegisterRerankBuilder(providerName string, b RerankBuilder) error {
	if b == nil {
		return ErrNilBuilder
	}
	builderMu.Lock()
	defer builderMu.Unlock()
	if buildersFrozen {
		return ErrBuilderFrozen
	}
	if _, exists := rerankBuilders[providerName]; exists {
		return fmt.Errorf("%w: rerank builder for %q", ErrDuplicateBuilder, providerName)
	}
	rerankBuilders[providerName] = b
	return nil
}

// freezeBuilders 冻结 builder 注册表，阻止后续注册（首次 Registry 构建后调用）。
func freezeBuilders() {
	builderMu.Lock()
	defer builderMu.Unlock()
	buildersFrozen = true
}

// lookupChatBuilder 查找 Chat builder（只读，加读锁）。
func lookupChatBuilder(providerName string) (ChatBuilder, bool) {
	builderMu.RLock()
	defer builderMu.RUnlock()
	b, ok := chatBuilders[providerName]
	return b, ok
}

// lookupEmbedBuilder 查找 Embed builder（只读，加读锁）。
func lookupEmbedBuilder(providerName string) (EmbedBuilder, bool) {
	builderMu.RLock()
	defer builderMu.RUnlock()
	b, ok := embedBuilders[providerName]
	return b, ok
}

// lookupRerankBuilder 查找 Rerank builder（只读，加读锁）。
func lookupRerankBuilder(providerName string) (RerankBuilder, bool) {
	builderMu.RLock()
	defer builderMu.RUnlock()
	b, ok := rerankBuilders[providerName]
	return b, ok
}

// knownProviderNames 返回已注册的 Provider 名称列表（用于错误信息）。
func knownProviderNames() []string {
	builderMu.RLock()
	defer builderMu.RUnlock()
	seen := map[string]bool{}
	for k := range chatBuilders {
		seen[k] = true
	}
	for k := range embedBuilders {
		seen[k] = true
	}
	for k := range rerankBuilders {
		seen[k] = true
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	return names
}
