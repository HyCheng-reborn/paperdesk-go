package providers

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestMain 注册测试用 builder，确保所有测试使用已注册的 provider。
func TestMain(m *testing.M) {
	// 注册 "openai" 和 "bailian" 的测试 builder
	// 使用 WrapChatModel + FakeChat 作为底层，保留 Profile 信息
	_ = RegisterChatBuilder("openai", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return &profileChat{FakeChat: NewFakeChat(), profile: cfg.Profile.Clone()}, nil
	})
	_ = RegisterEmbedBuilder("openai", func(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
		return NewFakeEmbed(1024, "openai-embed"), nil
	})
	_ = RegisterRerankBuilder("openai", func(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
		return NewFakeRerank(), nil
	})
	_ = RegisterChatBuilder("bailian", func(ctx context.Context, cfg BuildConfig) (ChatRole, error) {
		return &profileChat{FakeChat: NewFakeChat(), profile: cfg.Profile.Clone()}, nil
	})
	_ = RegisterEmbedBuilder("bailian", func(ctx context.Context, cfg BuildConfig) (EmbedRole, error) {
		return NewFakeEmbed(1024, "bailian-embed"), nil
	})
	_ = RegisterRerankBuilder("bailian", func(ctx context.Context, cfg BuildConfig) (RerankRole, error) {
		return NewFakeRerank(), nil
	})
	m.Run()
}

// profileChat 包装 FakeChat，附加 Profile 信息（测试用）。
type profileChat struct {
	*FakeChat
	profile *ProviderProfile
}

func (pc *profileChat) Profile() ProviderProfile {
	if pc.profile == nil {
		return ProviderProfile{}
	}
	return pc.profile.snapshot()
}

func (pc *profileChat) Capabilities() Capabilities {
	if pc.profile == nil {
		return Capabilities{}
	}
	return pc.profile.Capabilities
}

// countingCreds 是可控的 CredentialResolver：按调用序返回预设结果，并统计调用次数。
// 用于确定性验证“失败初始化不发布缓存”“缓存命中不重复构建”。
type countingCreds struct {
	mu    sync.Mutex
	calls int
	// failUntil 表示前 N 次调用返回错误（0 表示从不失败）。
	failUntil int
}

func (c *countingCreds) resolver() CredentialResolver {
	return func(ctx context.Context, p *ProviderProfile) (string, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls++
		if c.calls <= c.failUntil {
			return "", errors.New("credential backend unavailable")
		}
		return "test-key", nil
	}
}

func (c *countingCreds) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// testChatProfile 构造一个指向不可路由端点的 Chat Profile（构造不发起网络请求）。
func testChatProfile(t *testing.T, modelName string) *ProviderProfile {
	t.Helper()
	p, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.invalid/v1",
		Model:        modelName,
		Version:      "1",
	})
	if err != nil {
		t.Fatalf("NewProviderProfile(%s): %v", modelName, err)
	}
	return p
}

// TestRegistry_ProfileIsolation：模型缓存 per Profile 隔离——
// 相同 Profile 的角色（Chat 与 Extract）复用同一实例；不同 Profile 得到独立实例；
// 缓存命中不重复构建（以凭据解析次数为构建次数代理）。
func TestRegistry_ProfileIsolation(t *testing.T) {
	ctx := context.Background()
	profileA := testChatProfile(t, "qwen-plus")
	profileB := testChatProfile(t, "qwen-max")

	creds := &countingCreds{}
	reg, err := NewRegistry(RegistryConfig{
		Chat:        profileA,
		Extract:     profileA, // 与 Chat 同 Profile → 应共享实例
		QA:          profileB, // 不同 Profile → 应独立实例
		Budget:      mustProcessBudget(t, 0, 0, 0, 0),
		Transport:   NewTransport(),
		Credentials: creds.resolver(),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	chat, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got := creds.count(); got != 1 {
		t.Fatalf("first GetChat builds once, creds calls = %d", got)
	}

	// 再次 GetChat 命中缓存：同一实例，不再构建。
	chat2, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat#2: %v", err)
	}
	if chat2 != chat {
		t.Fatal("GetChat must return cached instance for same profile")
	}
	if got := creds.count(); got != 1 {
		t.Fatalf("cache hit must not rebuild, creds calls = %d", got)
	}

	// Extract 与 Chat 共享 Profile → 同一实例，不再构建。
	extract, err := reg.GetExtract(ctx)
	if err != nil {
		t.Fatalf("GetExtract: %v", err)
	}
	if extract != chat {
		t.Fatal("Extract shares Chat profile must reuse the same instance")
	}
	if got := creds.count(); got != 1 {
		t.Fatalf("shared profile must not rebuild, creds calls = %d", got)
	}

	// QA 使用不同 Profile → 独立实例，触发一次新构建。
	qa, err := reg.GetQA(ctx)
	if err != nil {
		t.Fatalf("GetQA: %v", err)
	}
	if qa == chat {
		t.Fatal("different profile must NOT share cache instance")
	}
	if got := creds.count(); got != 2 {
		t.Fatalf("distinct profile builds once more, creds calls = %d", got)
	}

	// Rewrite 未显式绑定 → 回退到 Chat Profile（与 chat 同实例）。
	rewrite, err := reg.GetRewrite(ctx)
	if err != nil {
		t.Fatalf("GetRewrite: %v", err)
	}
	if rewrite != chat {
		t.Fatal("Rewrite falls back to Chat profile must reuse Chat instance")
	}

	// 未配置的角色返回 ErrRoleNotConfigured。
	if _, err := reg.GetEmbedder(ctx); !errors.Is(err, ErrRoleNotConfigured) {
		t.Fatalf("GetEmbedder without profile err = %v, want ErrRoleNotConfigured", err)
	}
	if _, err := reg.GetReranker(ctx); !errors.Is(err, ErrRoleNotConfigured) {
		t.Fatalf("GetReranker without profile err = %v, want ErrRoleNotConfigured", err)
	}
}

// TestRegistry_FailedInit：失败初始化不发布缓存——
// 凭据解析失败时返回错误且不写缓存，后续调用可重试并在成功后仅构建一次。
func TestRegistry_FailedInit(t *testing.T) {
	ctx := context.Background()
	profile := testChatProfile(t, "qwen-plus")

	// 前两次解析失败，第三次起成功。
	creds := &countingCreds{failUntil: 2}
	reg, err := NewRegistry(RegistryConfig{
		Chat:        profile,
		Budget:      mustProcessBudget(t, 0, 0, 0, 0),
		Transport:   NewTransport(),
		Credentials: creds.resolver(),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// 第 1 次：失败，不发布缓存。
	if _, err := reg.GetChat(ctx); err == nil {
		t.Fatal("first GetChat must fail while credentials unavailable")
	}
	// 第 2 次：仍失败（证明上次失败未污染缓存，重新尝试构建）。
	if _, err := reg.GetChat(ctx); err == nil {
		t.Fatal("second GetChat must still fail (failed init not cached)")
	}
	if got := creds.count(); got != 2 {
		t.Fatalf("failed inits must retry build each call, creds calls = %d", got)
	}

	// 第 3 次：成功构建并发布缓存。
	chat, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("third GetChat should succeed: %v", err)
	}
	if chat == nil {
		t.Fatal("successful GetChat returned nil")
	}
	if got := creds.count(); got != 3 {
		t.Fatalf("creds calls = %d, want 3", got)
	}

	// 第 4 次：命中缓存，不再构建（凭据解析次数不增）。
	chat2, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("fourth GetChat: %v", err)
	}
	if chat2 != chat {
		t.Fatal("post-success GetChat must return cached instance")
	}
	if got := creds.count(); got != 3 {
		t.Fatalf("cache hit after success must not rebuild, creds calls = %d", got)
	}

	// NewRegistry 参数校验。
	if _, err := NewRegistry(RegistryConfig{Chat: profile, Credentials: creds.resolver()}); !errors.Is(err, ErrNilBudget) {
		t.Fatalf("nil budget err = %v, want ErrNilBudget", err)
	}
	if _, err := NewRegistry(RegistryConfig{Chat: profile, Budget: mustProcessBudget(t, 0, 0, 0, 0)}); !errors.Is(err, ErrNilCredentialFn) {
		t.Fatalf("nil credentials err = %v, want ErrNilCredentialFn", err)
	}
}

// TestFake_AllRoles：fake 覆盖所有角色，经 ProviderRegistry 接口注入，装配路径与生产一致。
func TestFake_AllRoles(t *testing.T) {
	ctx := context.Background()
	reg := NewFakeRegistry()

	// 所有角色 getter 均返回非 nil、无错误。
	chat, err := reg.GetChat(ctx)
	if err != nil || chat == nil {
		t.Fatalf("GetChat: %v nil=%v", err, chat == nil)
	}
	rewrite, err := reg.GetRewrite(ctx)
	if err != nil || rewrite == nil {
		t.Fatalf("GetRewrite: %v nil=%v", err, rewrite == nil)
	}
	qa, err := reg.GetQA(ctx)
	if err != nil || qa == nil {
		t.Fatalf("GetQA: %v nil=%v", err, qa == nil)
	}
	extract, err := reg.GetExtract(ctx)
	if err != nil || extract == nil {
		t.Fatalf("GetExtract: %v nil=%v", err, extract == nil)
	}
	embed, err := reg.GetEmbedder(ctx)
	if err != nil || embed == nil {
		t.Fatalf("GetEmbedder: %v nil=%v", err, embed == nil)
	}
	reranker, err := reg.GetReranker(ctx)
	if err != nil || reranker == nil {
		t.Fatalf("GetReranker: %v nil=%v", err, reranker == nil)
	}

	// Extract 与 Chat 共享同一 fake 实例（角色别名语义）。
	if extract != chat {
		t.Fatal("fake Extract and Chat must share the same instance")
	}

	// Chat 默认行为。
	msg, err := chat.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("fake Generate: %v", err)
	}
	if msg.Content != "fake-chat-reply" {
		t.Fatalf("fake Generate content = %q", msg.Content)
	}
	sr, err := chat.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("fake Stream: %v", err)
	}
	defer sr.Close()
	var sb strings.Builder
	for {
		chunk, rerr := sr.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("fake Stream recv: %v", rerr)
		}
		sb.WriteString(chunk.Content)
	}
	if got := sb.String(); got != "fake-stream" {
		t.Fatalf("fake Stream aggregated = %q, want %q", got, "fake-stream")
	}

	// Embed 默认行为：确定维度与模型身份。
	vecs, err := embed.EmbedStrings(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("fake EmbedStrings: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 1024 {
		t.Fatalf("fake embed shape = %d x %d", len(vecs), len(vecs[0]))
	}
	if embed.Dimensions() != 1024 || embed.ModelID() != "fake-embed" {
		t.Fatalf("fake embed meta = %d %q", embed.Dimensions(), embed.ModelID())
	}

	// Rerank 默认行为：截断到 topK 并附分数。
	docs := []*schema.Document{
		{ID: "1", Content: "a"},
		{ID: "2", Content: "b"},
		{ID: "3", Content: "c"},
	}
	out, err := reranker.Rerank(ctx, "q", docs, 2)
	if err != nil {
		t.Fatalf("fake Rerank: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("fake Rerank returned %d docs, want 2", len(out))
	}
	if out[0].Score() <= out[1].Score() {
		t.Fatalf("fake Rerank scores must be descending: %v then %v", out[0].Score(), out[1].Score())
	}

	// 可定制注入：替换 Chat 角色为定制 fake（与生产装配路径一致，经接口注入）。
	custom := NewFakeChat()
	custom.SetGenerate(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		return &schema.Message{Role: schema.Assistant, Content: "custom-reply"}, nil
	})
	reg.Chat = custom
	got, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat after inject: %v", err)
	}
	cmsg, err := got.Generate(ctx, []*schema.Message{schema.UserMessage("x")})
	if err != nil {
		t.Fatalf("custom Generate: %v", err)
	}
	if cmsg.Content != "custom-reply" {
		t.Fatalf("custom reply = %q", cmsg.Content)
	}

	// 缺失角色返回 ErrRoleNotConfigured。
	reg.Rerank = nil
	if _, err := reg.GetReranker(ctx); !errors.Is(err, ErrRoleNotConfigured) {
		t.Fatalf("nil rerank err = %v, want ErrRoleNotConfigured", err)
	}
}

// TestProfile_CacheKey_NoCollision：易碰撞的 KeyOptions（未转义拼接曾编码成同一串
// "a=1,b=2"）必须产生不同缓存键，且经 Registry 验证不复用同一缓存实例（stage2 复核 B3）。
func TestProfile_CacheKey_NoCollision(t *testing.T) {
	base := ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.invalid/v1",
		Model:        "qwen-plus",
		Version:      "1",
	}
	a := base
	a.KeyOptions = map[string]string{"a": "1,b=2"}
	b := base
	b.KeyOptions = map[string]string{"a": "1", "b": "2"}
	pa, err := NewProviderProfile(a)
	if err != nil {
		t.Fatalf("NewProviderProfile(a): %v", err)
	}
	pbb, err := NewProviderProfile(b)
	if err != nil {
		t.Fatalf("NewProviderProfile(b): %v", err)
	}
	if pa.CacheKey() == pbb.CacheKey() {
		t.Fatalf("collision-prone KeyOptions must produce distinct cache keys, both=%q", pa.CacheKey())
	}
	// 经 Registry 验证：不同 KeyOptions 不得复用同一缓存实例
	reg, err := NewRegistry(RegistryConfig{
		Chat:        pa,
		QA:          pbb,
		Budget:      mustProcessBudget(t, 1, 100, 100, 1),
		Transport:   NewTransport(),
		Credentials: func(context.Context, *ProviderProfile) (string, error) { return "offline-key", nil },
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	qa, err := reg.GetQA(context.Background())
	if err != nil {
		t.Fatalf("GetQA: %v", err)
	}
	if chat == qa {
		t.Fatal("distinct KeyOptions must not reuse one cached model instance")
	}
}

// TestRegistry_SnapshotIsolated：构造 Registry 后修改调用方持有的原 Profile，
// 凭据解析器与适配器 Profile() 看到的仍是构造期快照（原值），证明 Registry/adapter
// 拥有防御性副本而非可写内部引用（stage2 复核 B3）。
func TestRegistry_SnapshotIsolated(t *testing.T) {
	ctx := context.Background()
	p := testChatProfile(t, "original-model")
	var resolvedModel string
	reg, err := NewRegistry(RegistryConfig{
		Chat:   p,
		Budget: mustProcessBudget(t, 1, 100, 100, 1),
		Credentials: func(_ context.Context, prof *ProviderProfile) (string, error) {
			resolvedModel = prof.Model
			return "offline-key", nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// 构造后修改调用方原 Profile：不得影响已发布身份
	p.Model = "mutated-after-registry-construction"
	role, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	got := role.(interface{ Profile() ProviderProfile }).Profile()
	if resolvedModel != "original-model" {
		t.Fatalf("credential resolver saw mutated model: %q", resolvedModel)
	}
	if got.Model != "original-model" {
		t.Fatalf("adapter Profile() saw mutated model: %q", got.Model)
	}
	// 进一步：修改 Profile() 返回的副本不得影响后续 GetChat 命中的缓存实例身份
	got.Model = "caller-mutation"
	role2, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat#2: %v", err)
	}
	if role2 != role {
		t.Fatal("same profile must reuse cached instance")
	}
	got2 := role2.(interface{ Profile() ProviderProfile }).Profile()
	if got2.Model != "original-model" {
		t.Fatalf("internal identity leaked through Profile() copy: %q", got2.Model)
	}
}

// ---------------------------------------------------------------------------
// R2：resolver 出口不得泄漏内部 Profile（传入深拷贝）
// ---------------------------------------------------------------------------

// TestRegistry_ResolverCannotMutateProfile：凭据解析器修改传入 Profile 的字段，
// GetChat 返回的适配器 Profile() 仍为原值，证明 resolver 得到的是深拷贝而非内部身份指针。
func TestRegistry_ResolverCannotMutateProfile(t *testing.T) {
	ctx := context.Background()
	p := testChatProfile(t, "original-model")
	reg, err := NewRegistry(RegistryConfig{
		Chat:   p,
		Budget: mustProcessBudget(t, 0, 0, 0, 0),
		Credentials: func(_ context.Context, prof *ProviderProfile) (string, error) {
			// 恶意/意外改写：试图篡改 Registry 内部已发布身份
			prof.Model = "resolver-modified-model"
			prof.Version = "hacked"
			prof.Endpoint = "https://evil.invalid/v1"
			return "offline-key", nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	role, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	got := role.(interface{ Profile() ProviderProfile }).Profile()
	if got.Model != "original-model" {
		t.Fatalf("resolver mutated internal Profile.Model: %q", got.Model)
	}
	if got.Version != "1" {
		t.Fatalf("resolver mutated internal Profile.Version: %q", got.Version)
	}
	if got.Endpoint != "https://example.invalid/v1" {
		t.Fatalf("resolver mutated internal Profile.Endpoint: %q", got.Endpoint)
	}
}

// TestRegistry_ResolverCannotMutateKeyOptions：凭据解析器修改传入 Profile 的 KeyOptions map，
// 内部缓存键不得漂移：适配器 Profile().KeyOptions 仍为原值，且第二次 GetChat 命中缓存（不重建）。
func TestRegistry_ResolverCannotMutateKeyOptions(t *testing.T) {
	ctx := context.Background()
	p, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     "https://example.invalid/v1",
		Model:        "qwen-plus",
		Version:      "1",
		KeyOptions:   map[string]string{KeyOptionStream: "true"},
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	var calls int
	reg, err := NewRegistry(RegistryConfig{
		Chat:   p,
		Budget: mustProcessBudget(t, 0, 0, 0, 0),
		Credentials: func(_ context.Context, prof *ProviderProfile) (string, error) {
			calls++
			// 试图改写 KeyOptions map：篡改现有键并注入新键
			prof.KeyOptions[KeyOptionStream] = "false"
			prof.KeyOptions["injected"] = "x"
			return "offline-key", nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	role, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	got := role.(interface{ Profile() ProviderProfile }).Profile()
	if got.KeyOptions[KeyOptionStream] != "true" {
		t.Fatalf("resolver mutated internal KeyOptions[stream]: %q", got.KeyOptions[KeyOptionStream])
	}
	if _, ok := got.KeyOptions["injected"]; ok {
		t.Fatal("resolver-injected key leaked into internal KeyOptions")
	}
	// 内部缓存键未因回调改写而漂移：第二次 GetChat 必须命中缓存（不再解析凭据）。
	role2, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat#2: %v", err)
	}
	if role2 != role {
		t.Fatal("cache key must be stable; second GetChat should hit the cached instance")
	}
	if calls != 1 {
		t.Fatalf("internal cache key drifted (rebuilt): creds calls = %d, want 1", calls)
	}
}

// TestRegistry_ResolverHoldsCopy：resolver 保留传入副本引用，在构建完成后于 Registry 之外改写，
// 不得影响 Registry 内部身份（证明传入的确实是一份独立副本）。
func TestRegistry_ResolverHoldsCopy(t *testing.T) {
	ctx := context.Background()
	p := testChatProfile(t, "original-model")
	var held *ProviderProfile
	reg, err := NewRegistry(RegistryConfig{
		Chat:   p,
		Budget: mustProcessBudget(t, 0, 0, 0, 0),
		Credentials: func(_ context.Context, prof *ProviderProfile) (string, error) {
			held = prof // 保留副本引用，稍后在 Registry 之外改写
			return "offline-key", nil
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	role, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if held == nil {
		t.Fatal("resolver did not capture a profile copy")
	}
	// 在 Registry 之外、构建完成之后改写保留的副本：不得影响 Registry 内部身份。
	held.Model = "late-mutation"
	if held.KeyOptions == nil {
		held.KeyOptions = map[string]string{}
	}
	held.KeyOptions["late"] = "mutation"

	role2, err := reg.GetChat(ctx)
	if err != nil {
		t.Fatalf("GetChat#2: %v", err)
	}
	if role2 != role {
		t.Fatal("same profile must reuse the cached instance")
	}
	got := role2.(interface{ Profile() ProviderProfile }).Profile()
	if got.Model != "original-model" {
		t.Fatalf("held-copy mutation leaked into Registry identity: %q", got.Model)
	}
	if _, ok := got.KeyOptions["late"]; ok {
		t.Fatal("held-copy KeyOptions mutation leaked into Registry identity")
	}
}
