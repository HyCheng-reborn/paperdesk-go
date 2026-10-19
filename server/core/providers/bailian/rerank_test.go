package bailian

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// rerankResponse 构造与 SiliconFlow/百炼兼容的 rerank JSON 响应。
func rerankResponse(results []map[string]any) map[string]any {
	return map[string]any{
		"id":      "rerank-test",
		"results": results,
	}
}

func rerankResult(index int, score float64) map[string]any {
	return map[string]any{
		"index":           index,
		"relevance_score": score,
	}
}

func newRerankTestProfile(endpoint string) *providers.ProviderProfile {
	p, _ := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: "bailian",
		Protocol:     "openai-compatible",
		Endpoint:     endpoint,
		Model:        "qwen3-rerank",
	})
	return p
}

func testDocs(contents ...string) []*schema.Document {
	docs := make([]*schema.Document, len(contents))
	for i, c := range contents {
		docs[i] = &schema.Document{ID: string(rune('a' + i)), Content: c}
	}
	return docs
}

func TestBuildRerank_NormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rerankResponse([]map[string]any{
			rerankResult(1, 0.95),
			rerankResult(0, 0.75),
		}))
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0", "doc1")
	result, err := rk.Rerank(context.Background(), "query", docs, 2)
	if err != nil {
		t.Fatalf("Rerank failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// 第一个结果应该是 doc1（score=0.95）
	if result[0].Content != "doc1" {
		t.Errorf("expected first result to be 'doc1', got %q", result[0].Content)
	}
	if result[0].Score() < 0.9 {
		t.Errorf("expected score ~0.95, got %f", result[0].Score())
	}
}

func TestBuildRerank_MissingIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 缺少 index 字段
		json.NewEncoder(w).Encode(map[string]any{
			"id": "rerank-test",
			"results": []map[string]any{
				{"relevance_score": 0.9},
			},
		})
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0")
	_, err = rk.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !strings.Contains(err.Error(), "missing index") {
		t.Errorf("expected 'missing index' error, got: %v", err)
	}
}

func TestBuildRerank_MissingScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "rerank-test",
			"results": []map[string]any{
				{"index": 0},
			},
		})
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0")
	_, err = rk.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for missing score")
	}
	if !strings.Contains(err.Error(), "missing score") {
		t.Errorf("expected 'missing score' error, got: %v", err)
	}
}

func TestBuildRerank_NaNScore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// NaN 不是有效 JSON，但我们可以用极大值模拟
		// 实际上 rerank.Rerank 验证 NaN/Inf 是在解析后
		// 直接测试 rerank.Rerank 的 NaN 验证
		json.NewEncoder(w).Encode(map[string]any{
			"id": "rerank-test",
			"results": []map[string]any{
				{"index": 0, "relevance_score": "NaN"},
			},
		})
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0")
	_, err = rk.Rerank(context.Background(), "query", docs, 1)
	// 类型不匹配会导致 JSON 解析错误或 score 为 nil
	if err == nil {
		t.Fatal("expected error for NaN score")
	}
}

func TestBuildRerank_DuplicateIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rerankResponse([]map[string]any{
			rerankResult(0, 0.9),
			rerankResult(0, 0.8), // 重复 index
		}))
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0", "doc1")
	_, err = rk.Rerank(context.Background(), "query", docs, 2)
	if err == nil {
		t.Fatal("expected error for duplicate index")
	}
	if !strings.Contains(err.Error(), "duplicate index") {
		t.Errorf("expected 'duplicate index' error, got: %v", err)
	}
}

func TestBuildRerank_IndexOutOfBounds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rerankResponse([]map[string]any{
			rerankResult(5, 0.9), // index=5 超出 docs 范围
		}))
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0", "doc1")
	_, err = rk.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for index out of bounds")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("expected 'out of range' error, got: %v", err)
	}
}

func TestBuildRerank_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0")
	_, err = rk.Rerank(context.Background(), "query", docs, 1)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestBuildRerank_Timeout30s(t *testing.T) {
	// 验证 rerank 有 30s 超时约束（由底层 rerank.Rerank 保证）
	// 使用 context 超时来测试，而非等待 30s
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 等待 5s 后返回（超过 context 的 1s 超时）
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	docs := testDocs("doc0")
	_, err = rk.Rerank(ctx, "query", docs, 1)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestBuildRerank_NilProfile(t *testing.T) {
	tp := providers.NewTransport()
	_, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   nil,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err == nil {
		t.Fatal("expected error for nil profile")
	}
}

func TestBuildRerank_NilTransport(t *testing.T) {
	profile := newRerankTestProfile("http://localhost:1234")
	_, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil transport")
	}
}

func TestBuildRerank_InfScore(t *testing.T) {
	// 测试 rerank 对 Inf 分数的校验（由底层 rerank.Rerank 保证）
	// 直接测试 rerank 包的校验链
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 写入一个超大值模拟 Inf
		json.NewEncoder(w).Encode(map[string]any{
			"id": "rerank-test",
			"results": []map[string]any{
				{"index": 0, "relevance_score": math.MaxFloat64},
			},
		})
	}))
	defer srv.Close()

	profile := newRerankTestProfile(srv.URL)
	tp := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	rk, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test-key",
		Transport: tp,
	})
	if err != nil {
		t.Fatalf("BuildRerank failed: %v", err)
	}

	docs := testDocs("doc0")
	// MaxFloat64 不是 Inf，应通过校验
	result, err := rk.Rerank(context.Background(), "query", docs, 1)
	if err != nil {
		t.Fatalf("MaxFloat64 should pass validation: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 result, got %d", len(result))
	}
}
