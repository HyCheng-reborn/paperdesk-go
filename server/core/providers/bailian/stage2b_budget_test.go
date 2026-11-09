package bailian

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B3: 输出预留与 opts/default max_tokens 同源
// ---------------------------------------------------------------------------

// chatCompletionWithUsage 返回一个标准非流式 Chat 补全响应。
func chatCompletionWithUsage() map[string]any {
	return map[string]any{
		"id":     "chatcmpl-b3",
		"object": "chat.completion",
		"model":  "test-model",
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     4,
			"completion_tokens": 4,
			"total_tokens":      8,
		},
	}
}

// TestBudget_OutputOptsMatchReservation 用真实 HTTP 断言：请求体 max_tokens
// 与预算预留的 MaxOutput 同源一致（opts 透传，不再固定 8192）。
func TestBudget_OutputOptsMatchReservation(t *testing.T) {
	bodyCh := make(chan int, 1)
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		raw, _ := io.ReadAll(r.Body)
		var probe struct {
			MaxTokens *int `json:"max_tokens"`
		}
		_ = json.Unmarshal(raw, &probe)
		mt := -1
		if probe.MaxTokens != nil {
			mt = *probe.MaxTokens
		}
		select {
		case bodyCh <- mt:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionWithUsage())
	}))
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     srv.URL + "/v1",
		Model:        "test-model",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})

	role, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: transport,
		Budget:    budget,
	})
	if err != nil {
		t.Fatalf("BuildChat: %v", err)
	}
	adapter, ok := role.(*chatAdapter)
	if !ok {
		t.Fatalf("expected *chatAdapter, got %T", role)
	}

	const wantMax = 1234
	opts := []model.Option{model.WithMaxTokens(wantMax)}
	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}

	// 预留侧：resolveMaxOutput 与 estimateTokensWithOutput 必须反映 opts 的实际值。
	if got := adapter.resolveMaxOutput(opts); got != wantMax {
		t.Fatalf("resolveMaxOutput = %d, want %d (opts not parsed)", got, wantMax)
	}
	if est := adapter.estimateTokensWithOutput(msgs, adapter.resolveMaxOutput(opts)); est.MaxOutput != wantMax {
		t.Fatalf("reservation MaxOutput = %d, want %d", est.MaxOutput, wantMax)
	}

	// 真实发送：请求体 max_tokens 必须与预留同源一致。
	if _, err := role.Generate(context.Background(), msgs, opts...); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	select {
	case mt := <-bodyCh:
		if mt != wantMax {
			t.Fatalf("request body max_tokens = %d, want %d (reservation/send not same-source)", mt, wantMax)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for request body")
	}
}

// TestBudget_OutputOptsRejected 验证超限的输出预留将在真实发送之前被拒绝。
func TestBudget_OutputOptsRejected(t *testing.T) {
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionWithUsage())
	}))
	defer srv.Close()

	// 进程输出上限 100，远小于请求的 WithMaxTokens(50000)。
	budget, _ := providers.NewProcessBudget(0, 0, 100, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     srv.URL + "/v1",
		Model:        "test-model",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})

	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: fakeCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}
	_, err = chat.Generate(context.Background(), msgs, model.WithMaxTokens(50000))
	if err == nil {
		t.Fatal("expected budget exhausted error for over-limit max_tokens")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("expected budget error, got: %v", err)
	}
	// 超限时必须在发送前拒绝：服务器不应收到任何请求。
	if n := atomic.LoadInt32(&reqCount); n != 0 {
		t.Errorf("expected 0 HTTP sends (reject before send), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// B5: 累计 owner 共享（ChatStream 子步骤共享同一 RequestBudget）
// ---------------------------------------------------------------------------

// TestOwner_ChatStreamShared 验证入口注入的 RequestBudget 被子步骤共享：
// 两次 Generate 累计到同一 owner（child scope），第三次因 child 上限被拒，
// 即使进程级仍有余量。
func TestOwner_ChatStreamShared(t *testing.T) {
	srv := newFakeChatServer(t)
	defer srv.Close()

	// 进程级允许 10 次；请求级 owner 上限 2 次。
	budget, _ := providers.NewProcessBudget(10, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     srv.URL + "/v1",
		Model:        "test-model",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})
	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: fakeCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	// 入口构造一次 RequestBudget（child 上限 2），通过 ctx 传递给所有子步骤。
	rb, err := budget.NewRequestBudget(context.Background(), providers.WithMaxRequests(2))
	if err != nil {
		t.Fatalf("NewRequestBudget: %v", err)
	}
	ctx := providers.ContextWithRequestBudget(context.Background(), rb)
	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}

	if _, err := chat.Generate(ctx, msgs); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if _, err := chat.Generate(ctx, msgs); err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	// 第三次：child owner 上限 2 已耗尽，即使进程级允许 10。
	if _, err := chat.Generate(ctx, msgs); err == nil {
		t.Fatal("third Generate should be rejected by shared owner limit")
	}
	// 共享累计：同一 owner 记录了两次 committed。
	if snap := rb.Snapshot(); snap.CommittedAttempts != 2 {
		t.Errorf("shared owner CommittedAttempts = %d, want 2", snap.CommittedAttempts)
	}
}
