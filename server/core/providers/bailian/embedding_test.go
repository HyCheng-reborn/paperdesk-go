package bailian

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// openAIEmbedResponse 构造 OpenAI 兼容的 embedding JSON 响应。
func openAIEmbedResponse(dims int, count int, withUsage bool) map[string]any {
	data := make([]map[string]any, count)
	for i := 0; i < count; i++ {
		vec := make([]float64, dims)
		for j := range vec {
			vec[j] = float64(i+j+1) * 0.01
		}
		data[i] = map[string]any{
			"object":    "embedding",
			"embedding": vec,
			"index":     i,
		}
	}
	resp := map[string]any{
		"object": "list",
		"data":   data,
		"model":  "text-embedding-v4",
	}
	if withUsage {
		resp["usage"] = map[string]any{
			"prompt_tokens": 10,
			"total_tokens":  10 + count*dims,
		}
	}
	return resp
}

func newEmbedTestProfile(endpoint string, dims int) *providers.ProviderProfile {
	p, _ := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: "bailian",
		Protocol:     "openai-compatible",
		Endpoint:     endpoint,
		Model:        "text-embedding-v4",
		Dimensions:   dims,
	})
	return p
}

func TestBuildEmbed_NormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse(4, 2, true))
	}))
	defer srv.Close()

	profile := newEmbedTestProfile(srv.URL, 4)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	emb, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildEmbed failed: %v", err)
	}

	result, err := emb.EmbedStrings(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedStrings failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(result))
	}
	if len(result[0]) != 4 {
		t.Errorf("expected dim 4, got %d", len(result[0]))
	}
}

func TestBuildEmbed_CountMismatch(t *testing.T) {
	// 服务器返回 3 个向量，但请求只有 2 条文本
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse(4, 3, true))
	}))
	defer srv.Close()

	profile := newEmbedTestProfile(srv.URL, 4)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	emb, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildEmbed failed: %v", err)
	}

	_, err = emb.EmbedStrings(context.Background(), []string{"hello", "world"})
	if err == nil {
		t.Fatal("expected count mismatch error")
	}
	if !strings.Contains(err.Error(), "count mismatch") {
		t.Errorf("expected 'count mismatch' error, got: %v", err)
	}
}

func TestBuildEmbed_DimensionMismatch(t *testing.T) {
	// 服务器返回维度为 3 的向量，但 Profile 声明维度为 4
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse(3, 2, true))
	}))
	defer srv.Close()

	profile := newEmbedTestProfile(srv.URL, 4)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	emb, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildEmbed failed: %v", err)
	}

	_, err = emb.EmbedStrings(context.Background(), []string{"hello", "world"})
	if err == nil {
		t.Fatal("expected dimension mismatch error")
	}
	if !strings.Contains(err.Error(), "dimension mismatch") {
		t.Errorf("expected 'dimension mismatch' error, got: %v", err)
	}
}

func TestBuildEmbed_NaN(t *testing.T) {
	// 服务器返回含 NaN 的向量
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 手动构造含 NaN 的 JSON（标准库 json 不直接支持 NaN，需手动写入）
		w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,` + fmt.Sprintf("%q", "NaN") + `,0.3,0.4],"index":0}],"model":"text-embedding-v4","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()

	// 由于标准 JSON 不支持 NaN，我们用自定义 handler 直接注入 NaN 值
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 用 math.NaN 无法直接 json.Marshal，手动写非法 JSON 模拟
		// 实际上 eino 的 openai embedder 不会返回 NaN，但我们要测试验证层
		// 因此直接测试 validateEmbeddingResponse 函数
	}))
	defer srv2.Close()

	// 直接测试验证函数
	result := [][]float64{{0.1, math.NaN(), 0.3, 0.4}}
	err := validateEmbeddingResponse(result, []string{"hello"}, 4)
	if err == nil {
		t.Fatal("expected NaN error")
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Errorf("expected NaN error, got: %v", err)
	}
}

func TestBuildEmbed_Inf(t *testing.T) {
	result := [][]float64{{0.1, math.Inf(1), 0.3, 0.4}}
	err := validateEmbeddingResponse(result, []string{"hello"}, 4)
	if err == nil {
		t.Fatal("expected Inf error")
	}
	if !strings.Contains(err.Error(), "Inf") {
		t.Errorf("expected Inf error, got: %v", err)
	}
}

func TestBuildEmbed_UsageMissing(t *testing.T) {
	// usage 缺失不报错（保守处理，不静默当 0）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse(4, 1, false)) // withUsage=false
	}))
	defer srv.Close()

	profile := newEmbedTestProfile(srv.URL, 4)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	emb, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildEmbed failed: %v", err)
	}

	result, err := emb.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedStrings should not fail on missing usage: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 vector, got %d", len(result))
	}
}

func TestValidateEmbeddingResponse_OK(t *testing.T) {
	result := [][]float64{{0.1, 0.2, 0.3, 0.4}, {0.5, 0.6, 0.7, 0.8}}
	err := validateEmbeddingResponse(result, []string{"a", "b"}, 4)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateEmbeddingResponse_NegativeInf(t *testing.T) {
	result := [][]float64{{0.1, math.Inf(-1), 0.3}}
	err := validateEmbeddingResponse(result, []string{"a"}, 3)
	if err == nil {
		t.Fatal("expected -Inf error")
	}
}

func TestBuildEmbed_NilProfile(t *testing.T) {
	tp := providers.NewTransport()
	_, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   nil,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err == nil {
		t.Fatal("expected error for nil profile")
	}
}

func TestBuildEmbed_NilTransport(t *testing.T) {
	profile := newEmbedTestProfile("http://localhost:1234", 4)
	_, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil transport")
	}
}

func TestEmbedRole_Dimensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(openAIEmbedResponse(4, 1, true))
	}))
	defer srv.Close()

	profile := newEmbedTestProfile(srv.URL, 4)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	emb, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildEmbed failed: %v", err)
	}
	if emb.Dimensions() != 4 {
		t.Errorf("expected Dimensions()=4, got %d", emb.Dimensions())
	}
	if emb.ModelID() != "text-embedding-v4" {
		t.Errorf("expected ModelID()='text-embedding-v4', got %q", emb.ModelID())
	}
}
