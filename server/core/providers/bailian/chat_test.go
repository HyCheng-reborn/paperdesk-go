package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// newTestProfile 创建用于测试的 ProviderProfile，endpoint 指向 httptest server。
func newTestProfile(endpoint, model string) *providers.ProviderProfile {
	p, _ := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: "bailian",
		Protocol:     "openai-compatible",
		Endpoint:     endpoint,
		Model:        model,
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})
	return p
}

// openAIChatResponse 构造 OpenAI 兼容的 chat completion JSON 响应。
func openAIChatResponse(content string, promptTokens, completionTokens int) map[string]any {
	resp := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	}
	if promptTokens > 0 || completionTokens > 0 {
		resp["usage"] = map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		}
	}
	return resp
}

// openAISSEChunks 构造 SSE 流式响应体。
func openAISSEChunks(contents []string) string {
	var b strings.Builder
	for _, c := range contents {
		b.WriteString("data: ")
		chunk := map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion.chunk",
			"created": 1700000000,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"content": c,
					},
					"finish_reason": nil,
				},
			},
		}
		data, _ := json.Marshal(chunk)
		b.Write(data)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestBuildChat_NormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIChatResponse("hello world", 10, 5))
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	msg, err := chat.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", msg.Content)
	}
}

func TestBuildChat_ErrorCode401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"Unauthorized","code":401}}`))
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "bad-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	_, err = chat.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "authentication") {
		t.Errorf("expected 401/auth error, got: %v", err)
	}
}

func TestBuildChat_ErrorCode500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error","code":500}}`))
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	_, err = chat.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") && !strings.Contains(err.Error(), "server error") {
		t.Errorf("expected 500/server error, got: %v", err)
	}
}

func TestBuildChat_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	_, err = chat.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestBuildChat_UsageMissing(t *testing.T) {
	// usage 缺失时保守处理：不报错，但结果仍可用
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 不含 usage 字段
		json.NewEncoder(w).Encode(openAIChatResponse("response without usage", 0, 0))
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	msg, err := chat.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Generate should not fail on missing usage: %v", err)
	}
	if msg == nil || msg.Content == "" {
		t.Error("expected non-empty response even without usage")
	}
}

func TestBuildChat_StreamNormal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Write([]byte(openAISSEChunks([]string{"hello ", "world"})))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sr, err := chat.Stream(ctx, []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	defer sr.Close()

	var content strings.Builder
	for {
		msg, recvErr := sr.Recv()
		if recvErr != nil {
			break
		}
		content.WriteString(msg.Content)
	}
	if content.Len() == 0 {
		t.Error("expected non-empty stream content")
	}
}

func TestBuildChat_StreamInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		// 发送一个 chunk 后关闭连接，模拟中断
		w.Write([]byte(openAISSEChunks([]string{"partial"})))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 不发送 [DONE]，直接返回（连接关闭）
	}))
	defer srv.Close()

	profile := newTestProfile(srv.URL, "qwen-plus")
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	chat, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildChat failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sr, err := chat.Stream(ctx, []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream failed: %v", err)
	}
	defer sr.Close()

	// 读取直到 EOF 或错误（中断场景）
	for {
		_, recvErr := sr.Recv()
		if recvErr != nil {
			break // EOF 或错误，符合预期
		}
	}
	// 流中断不应 panic，测试通过
}

func TestBuildChat_NilProfile(t *testing.T) {
	tp := providers.NewTransport()
	_, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   nil,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err == nil {
		t.Fatal("expected error for nil profile")
	}
	if !strings.Contains(err.Error(), "nil Provider") && !strings.Contains(err.Error(), "nil Profile") {
		t.Errorf("expected 'nil Profile' error, got: %v", err)
	}
}

func TestBuildChat_NilTransport(t *testing.T) {
	profile := newTestProfile("http://localhost:1234", "qwen-plus")
	_, err := BuildChat(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil transport")
	}
}
