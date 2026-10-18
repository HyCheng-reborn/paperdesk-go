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
		Budget:      NewProcessBudget(0, 0, 0, 0),
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
		Budget:      NewProcessBudget(0, 0, 0, 0),
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
	if _, err := NewRegistry(RegistryConfig{Chat: profile, Budget: NewProcessBudget(0, 0, 0, 0)}); !errors.Is(err, ErrNilCredentialFn) {
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
