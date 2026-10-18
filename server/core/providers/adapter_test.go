package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// roundTripperFunc 让 *http.Client 使用可计数的 RoundTripper，
// 用于证明适配器确实走了注入的统一 Transport（而非默认 client）。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// chatCompletionBody 是非流式 Chat 补全响应（OpenAI 兼容 wire format）。
const chatCompletionBody = `{
  "id": "chatcmpl-adapter",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "qwen-plus",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "hello-from-adapter"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}
}`

// embeddingBody 是 Embedding 响应（OpenAI 兼容 wire format，3 维）。
const embeddingBody = `{
  "object": "list",
  "data": [
    {"object": "embedding", "embedding": [0.1, 0.2, 0.3], "index": 0},
    {"object": "embedding", "embedding": [0.4, 0.5, 0.6], "index": 1}
  ],
  "model": "text-embedding-v4",
  "usage": {"prompt_tokens": 2, "completion_tokens": 0, "total_tokens": 2}
}`

// streamChunks 是流式 Chat 补全的 SSE 分片（delta 拼接为 "stream-hello"）。
var streamChunks = []string{
	`{"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"qwen-plus","choices":[{"index":0,"delta":{"role":"assistant","content":"stream-"}}]}`,
	`{"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
	`{"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"qwen-plus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
}

// newOpenAITestServer 启动一个 OpenAI 兼容的离线测试服务，按路径/请求体分派。
func newOpenAITestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var probe struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &probe)

		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(embeddingBody))
		case probe.Stream:
			w.Header().Set("Content-Type", "text/event-stream")
			if f, ok := w.(http.Flusher); ok {
				for _, c := range streamChunks {
					_, _ = w.Write([]byte("data: " + c + "\n\n"))
					f.Flush()
				}
			} else {
				for _, c := range streamChunks {
					_, _ = w.Write([]byte("data: " + c + "\n\n"))
				}
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(chatCompletionBody))
		}
	}))
}

// TestAdapter_ChatRole：现有 openai.NewChatModel → ChatRole 适配。
// 经 httptest 端到端验证 Generate/Stream，并证明注入的统一 Transport 被使用。
func TestAdapter_ChatRole(t *testing.T) {
	ctx := context.Background()
	srv := newOpenAITestServer(t)
	defer srv.Close()

	var rtCount int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&rtCount, 1)
		return http.DefaultTransport.RoundTrip(req)
	})}
	tr := NewTransport(WithHTTPClient(client))

	profile, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     srv.URL,
		Model:        "qwen-plus",
		Version:      "1",
		Capabilities: Capabilities{Stream: true, Usage: true},
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}

	role, err := BuildChatRole(ctx, BuildConfig{Profile: profile, APIKey: "test-key", Transport: tr})
	if err != nil {
		t.Fatalf("BuildChatRole: %v", err)
	}
	if role == nil {
		t.Fatal("BuildChatRole returned nil role")
	}

	// 端到端 Generate（非流式）。
	msg, err := role.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if msg == nil || msg.Content != "hello-from-adapter" {
		t.Fatalf("Generate content = %q, want %q", contentOf(msg), "hello-from-adapter")
	}

	// 端到端 Stream（流式），聚合 delta。
	sr, err := role.Stream(ctx, []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()
	var sb strings.Builder
	for {
		chunk, rerr := sr.Recv()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			t.Fatalf("Stream receive: %v", rerr)
		}
		sb.WriteString(chunk.Content)
	}
	if got := sb.String(); got != "stream-hello" {
		t.Fatalf("Stream aggregated = %q, want %q", got, "stream-hello")
	}

	// 注入的统一 Transport 确实被使用（两次请求：Generate + Stream）。
	if n := atomic.LoadInt32(&rtCount); n < 2 {
		t.Fatalf("injected transport RoundTrip count = %d, want >= 2", n)
	}

	// 适配器暴露 Profile/Capabilities。
	type profiled interface {
		Profile() *ProviderProfile
		Capabilities() Capabilities
	}
	pf, ok := role.(profiled)
	if !ok {
		t.Fatal("chatAdapter must expose Profile()/Capabilities()")
	}
	if pf.Profile() != profile {
		t.Fatal("Profile() must return the construction profile")
	}
	if !pf.Capabilities().Stream || !pf.Capabilities().Usage {
		t.Fatalf("Capabilities() = %+v", pf.Capabilities())
	}

	// WrapChatModel：委托已构造实例（用 FakeChat 作为 inner，离线验证委托语义）。
	fake := NewFakeChat()
	wrapped, err := WrapChatModel(fake, profile)
	if err != nil {
		t.Fatalf("WrapChatModel: %v", err)
	}
	wmsg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("x")})
	if err != nil {
		t.Fatalf("wrapped.Generate: %v", err)
	}
	if wmsg.Content != "fake-chat-reply" || fake.GenerateCalls() != 1 {
		t.Fatalf("WrapChatModel must delegate: content=%q calls=%d", wmsg.Content, fake.GenerateCalls())
	}

	// 错误路径。
	if _, err := BuildChatRole(ctx, BuildConfig{Profile: nil, Transport: tr}); !errors.Is(err, ErrNilProfile) {
		t.Fatalf("nil profile err = %v, want ErrNilProfile", err)
	}
	if _, err := BuildChatRole(ctx, BuildConfig{Profile: profile, Transport: nil}); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("nil transport err = %v, want ErrNilTransport", err)
	}
	if _, err := WrapChatModel(nil, profile); !errors.Is(err, ErrNilInner) {
		t.Fatalf("wrap nil inner err = %v, want ErrNilInner", err)
	}
	if _, err := WrapChatModel(fake, nil); !errors.Is(err, ErrNilProfile) {
		t.Fatalf("wrap nil profile err = %v, want ErrNilProfile", err)
	}
}

// TestAdapter_EmbedRole：现有 openai.NewEmbedder → EmbedRole 适配。
// 经 httptest 端到端验证 EmbedStrings，Dimensions/ModelID 来自 Profile 声明。
func TestAdapter_EmbedRole(t *testing.T) {
	ctx := context.Background()
	srv := newOpenAITestServer(t)
	defer srv.Close()

	var rtCount int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&rtCount, 1)
		return http.DefaultTransport.RoundTrip(req)
	})}
	tr := NewTransport(WithHTTPClient(client))

	profile, err := NewProviderProfile(ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     ProtocolOpenAICompat,
		Endpoint:     srv.URL,
		Model:        "text-embedding-v4",
		Version:      "1",
		Dimensions:   3,
	})
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}

	role, err := BuildEmbedRole(ctx, BuildConfig{Profile: profile, APIKey: "test-key", Transport: tr})
	if err != nil {
		t.Fatalf("BuildEmbedRole: %v", err)
	}
	if role == nil {
		t.Fatal("BuildEmbedRole returned nil role")
	}

	vecs, err := role.EmbedStrings(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedStrings: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("EmbedStrings returned %d vectors, want 2", len(vecs))
	}
	wantVecs := [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}
	for i := range vecs {
		if len(vecs[i]) != 3 {
			t.Fatalf("vec[%d] dim = %d, want 3", i, len(vecs[i]))
		}
		for j := range vecs[i] {
			if diff := vecs[i][j] - wantVecs[i][j]; diff > 1e-6 || diff < -1e-6 {
				t.Fatalf("vec[%d][%d] = %v, want %v", i, j, vecs[i][j], wantVecs[i][j])
			}
		}
	}

	// Dimensions/ModelID 来自 Profile（非运行时探测）。
	if role.Dimensions() != 3 {
		t.Fatalf("Dimensions() = %d, want 3", role.Dimensions())
	}
	if role.ModelID() != "text-embedding-v4@1" {
		t.Fatalf("ModelID() = %q, want %q", role.ModelID(), "text-embedding-v4@1")
	}
	if n := atomic.LoadInt32(&rtCount); n < 1 {
		t.Fatalf("injected transport must be used, count = %d", n)
	}

	// WrapEmbedder：委托已构造实例（用 FakeEmbed 作为 inner）。
	// 注意 Dimensions/ModelID 仍取自 Profile，而非 inner。
	fake := NewFakeEmbed(8, "inner-model")
	wrapped, err := WrapEmbedder(fake, profile)
	if err != nil {
		t.Fatalf("WrapEmbedder: %v", err)
	}
	wv, err := wrapped.EmbedStrings(ctx, []string{"x"})
	if err != nil {
		t.Fatalf("wrapped.EmbedStrings: %v", err)
	}
	if len(wv) != 1 || len(wv[0]) != 8 || fake.Calls() != 1 {
		t.Fatalf("WrapEmbedder must delegate EmbedStrings: %v calls=%d", wv, fake.Calls())
	}
	if wrapped.Dimensions() != 3 || wrapped.ModelID() != "text-embedding-v4@1" {
		t.Fatalf("wrapped Dimensions/ModelID must come from profile: %d %q", wrapped.Dimensions(), wrapped.ModelID())
	}

	// 错误路径。
	if _, err := BuildEmbedRole(ctx, BuildConfig{Profile: nil, Transport: tr}); !errors.Is(err, ErrNilProfile) {
		t.Fatalf("nil profile err = %v, want ErrNilProfile", err)
	}
	if _, err := BuildEmbedRole(ctx, BuildConfig{Profile: profile, Transport: nil}); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("nil transport err = %v, want ErrNilTransport", err)
	}
	if _, err := WrapEmbedder(nil, profile); !errors.Is(err, ErrNilInner) {
		t.Fatalf("wrap nil inner err = %v, want ErrNilInner", err)
	}
	if _, err := WrapEmbedder(fake, nil); !errors.Is(err, ErrNilProfile) {
		t.Fatalf("wrap nil profile err = %v, want ErrNilProfile", err)
	}
}

func contentOf(m *schema.Message) string {
	if m == nil {
		return ""
	}
	return m.Content
}
