package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B4: OpenAI 路径预算门禁回归测试
//
// 修复前 openai/register.go 直接调用基础适配器，绕过 Reserve/Commit/Finish。
// 以下测试用真实 Registry → 真实 builder（openai.init 注册）→ httptest，
// 断言 OpenAI 路径与 bailian 走同一预算门禁。
// ---------------------------------------------------------------------------

func openaiTestCredResolver(ctx context.Context, p *providers.ProviderProfile) (string, error) {
	return "test-key", nil
}

func mustOpenAIProfile(t *testing.T, p providers.ProviderProfile) *providers.ProviderProfile {
	t.Helper()
	pp, err := providers.NewProviderProfile(p)
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	return pp
}

// newOpenAIChatServer 返回固定 Chat 补全响应，并计数真实发送次数。
func newOpenAIChatServer(t *testing.T, reqCount *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(reqCount, 1)
		raw, _ := io.ReadAll(r.Body)
		var probe struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &probe)

		if probe.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			chunks := []string{
				`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
				`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
				`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":6,"total_tokens":12}}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
				if flusher != nil {
					flusher.Flush()
				}
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-openai",
			"object": "chat.completion",
			"model":  "gpt-test",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10},
		})
	}))
}

// TestOpenAI_BudgetCoversHTTP 验证 MaxRequests=1 时 OpenAI 路径第二次发送被拒，
// 且拒绝发生在真实 HTTP 发送之前。
func TestOpenAI_BudgetCoversHTTP(t *testing.T) {
	var reqCount int32
	srv := newOpenAIChatServer(t, &reqCount)
	defer srv.Close()

	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatalf("NewProcessBudget: %v", err)
	}
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustOpenAIProfile(t, providers.ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     srv.URL + "/v1",
		Model:        "gpt-test",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})

	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: openaiTestCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}

	// 第一次发送成功。
	if _, err := chat.Generate(context.Background(), msgs); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	// 第二次应被预算门禁拒绝（MaxRequests=1）。
	_, err = chat.Generate(context.Background(), msgs)
	if err == nil {
		t.Fatal("second Generate should be rejected by budget")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("expected budget error, got: %v", err)
	}
	// 真实发送只有一次（第二次在发送前拒绝）。
	if n := atomic.LoadInt32(&reqCount); n != 1 {
		t.Errorf("expected exactly 1 HTTP send, got %d", n)
	}
}

// TestOpenAI_StreamBudget 验证 OpenAI Stream 终结时 Finish：slot 释放、attempt 结算。
func TestOpenAI_StreamBudget(t *testing.T) {
	var reqCount int32
	srv := newOpenAIChatServer(t, &reqCount)
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustOpenAIProfile(t, providers.ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     srv.URL + "/v1",
		Model:        "gpt-test",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})
	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: openaiTestCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	sr, err := chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sb strings.Builder
	for {
		msg, rerr := sr.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("Recv: %v", rerr)
			}
			break
		}
		if msg != nil {
			sb.WriteString(msg.Content)
		}
	}
	sr.Close()

	if got := sb.String(); got != "Hello world" {
		t.Errorf("aggregated stream = %q, want %q", got, "Hello world")
	}

	// Stream 终结后：attempt 已结算，in-flight slot 已释放。
	deadline := time.Now().Add(3 * time.Second)
	var snap providers.BudgetSnapshot
	for time.Now().Before(deadline) {
		snap = budget.Snapshot()
		if snap.CommittedAttempts >= 1 && snap.InFlight == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snap.CommittedAttempts < 1 {
		t.Errorf("expected >=1 committed attempt after stream finish, got %d", snap.CommittedAttempts)
	}
	if snap.InFlight != 0 {
		t.Errorf("expected in-flight slot released after stream finish, got %d", snap.InFlight)
	}
}

// ---------------------------------------------------------------------------
// Fix 7: 独立流关闭测试（真实 httptest 阻塞服务器）
// ---------------------------------------------------------------------------

// newBlockingOpenAIStreamServer 创建发送首条后阻塞 3s 再关闭连接的 SSE 服务器。
func newBlockingOpenAIStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected http.Flusher")
			return
		}
		chunk := `{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"first"},"finish_reason":null}]}`
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()

		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
}

// TestIndependentStreamEarlyClose 验证 OpenAI 路径真实 HTTP 流式关闭：
// 服务器发送首条后阻塞，消费者读首条后提前 Close，
// lease 应在合理时间内终结。
func TestIndependentStreamEarlyClose(t *testing.T) {
	srv := newBlockingOpenAIStreamServer(t)
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustOpenAIProfile(t, providers.ProviderProfile{
		ProviderName: ProviderOpenAI,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     srv.URL + "/v1",
		Model:        "gpt-test",
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})

	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: openaiTestCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	sr, err := chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	_, err = sr.Recv()
	if err != nil {
		sr.Close()
		t.Fatalf("Recv: %v", err)
	}

	sr.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := budget.Snapshot()
		if snap.CommittedAttempts >= 1 && snap.InFlight == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	snap := budget.Snapshot()
	t.Fatalf("expected lease finalized after early close: committed=%d inFlight=%d", snap.CommittedAttempts, snap.InFlight)
}
