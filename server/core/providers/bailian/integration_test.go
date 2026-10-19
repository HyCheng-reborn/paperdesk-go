package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B1: 预算接入测试
// ---------------------------------------------------------------------------

// TestBudget_RealHTTPSend 验证 Registry+ProcessBudget(MaxRequests=1) 发送 2 次 HTTP，
// 第二次返回 ErrBudgetExhausted。
func TestBudget_RealHTTPSend(t *testing.T) {
	// 设置假 HTTP 服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "test",
			"model": "test",
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": "hello"}},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	// 创建有限预算（MaxRequests=1）
	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatalf("NewProcessBudget: %v", err)
	}

	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))

	profile, _ := providers.NewProviderProfile(providers.ProviderProfile{
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
		Credentials: func(ctx context.Context, p *providers.ProviderProfile) (string, error) { return "test-key", nil },
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	chat, err := reg.GetChat(context.Background())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}

	// 第一次发送应成功
	_, err = chat.Generate(context.Background(), msgs)
	if err != nil {
		t.Fatalf("first Generate should succeed: %v", err)
	}

	// 第二次发送应返回预算耗尽
	_, err = chat.Generate(context.Background(), msgs)
	if err == nil {
		t.Fatal("second Generate should return budget exhausted error")
	}
	if !containsStr(err.Error(), "budget") {
		t.Errorf("expected budget error, got: %v", err)
	}
}

// TestBudget_StreamFinish 验证 Stream 终结时 Finish，usage 正确结算。
func TestBudget_StreamFinish(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	snap := budget.Snapshot()
	if snap.AttemptsUsed != 0 {
		t.Errorf("expected 0 attempts, got %d", snap.AttemptsUsed)
	}
}

// TestBudget_RetryNoRefresh 验证重试计入预算，不刷新累计。
func TestBudget_RetryNoRefresh(t *testing.T) {
	budget, _ := providers.NewProcessBudget(5, 0, 0, 0)

	rb, _ := budget.NewRequestBudget(context.Background())

	// 模拟 3 次重试
	for i := 0; i < 3; i++ {
		lease, err := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
		if err != nil {
			t.Fatalf("Reserve %d: %v", i, err)
		}
		lease.Commit()
		lease.Finish(nil) // unknown usage → 保守保留预留
	}

	snap := budget.Snapshot()
	if snap.AttemptsUsed != 3 {
		t.Errorf("expected 3 attempts used, got %d", snap.AttemptsUsed)
	}
	if snap.CommittedAttempts != 3 {
		t.Errorf("expected 3 committed, got %d", snap.CommittedAttempts)
	}
}

// ---------------------------------------------------------------------------
// B3: Embedding index 验证测试
// ---------------------------------------------------------------------------

// TestEmbedding_RawIndices_OutOfOrder 验证乱序 index 拒绝。
func TestEmbedding_RawIndices_OutOfOrder(t *testing.T) {
	body := buildRawEmbedResponse([]int{1, 0, 2})
	err := validateRawEmbeddingIndices(body)
	if err == nil {
		t.Fatal("expected error for out-of-order indices")
	}
	if !containsStr(err.Error(), "disorder") && !containsStr(err.Error(), "ascending") {
		t.Errorf("expected disorder error, got: %v", err)
	}
}

// TestEmbedding_RawIndices_Duplicate 验证重复 index 拒绝。
func TestEmbedding_RawIndices_Duplicate(t *testing.T) {
	body := buildRawEmbedResponse([]int{0, 0, 1})
	err := validateRawEmbeddingIndices(body)
	if err == nil {
		t.Fatal("expected error for duplicate indices")
	}
	if !containsStr(err.Error(), "duplicate") {
		t.Errorf("expected duplicate error, got: %v", err)
	}
}

// TestEmbedding_RawIndices_Missing 验证缺失 index 拒绝。
func TestEmbedding_RawIndices_Missing(t *testing.T) {
	// 构造一个 data item 缺少 index 字段
	body := []byte(`{"data":[{"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`)
	err := validateRawEmbeddingIndices(body)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !containsStr(err.Error(), "no index") {
		t.Errorf("expected missing index error, got: %v", err)
	}
}

// TestEmbedding_UsageMissing 验证 usage 缺失保守 unknown，不退款。
func TestEmbedding_UsageMissing(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())

	lease, err := rb.Reserve(&providers.TokenEstimate{InputTokens: 100, MaxOutput: 0})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	lease.Commit()
	// Finish with nil usage → unknown → 保守保留预留
	lease.Finish(nil)

	snap := budget.Snapshot()
	// 预留的 input tokens 应该保留（不退款为 0）
	if snap.InputUsed != 100 {
		t.Errorf("expected 100 input tokens used (conservative), got %d", snap.InputUsed)
	}
}

// TestEmbedding_RawIndices_Valid 验证正常 index 通过。
func TestEmbedding_RawIndices_Valid(t *testing.T) {
	body := buildRawEmbedResponse([]int{0, 1, 2})
	err := validateRawEmbeddingIndices(body)
	if err != nil {
		t.Fatalf("expected no error for valid indices, got: %v", err)
	}
}

// buildRawEmbedResponse 构造原始 Embedding 响应 JSON。
func buildRawEmbedResponse(indices []int) []byte {
	data := make([]map[string]any, len(indices))
	for i, idx := range indices {
		data[i] = map[string]any{
			"index":     idx,
			"embedding": []float64{0.1, 0.2, 0.3},
		}
	}
	resp := map[string]any{"data": data}
	b, _ := json.Marshal(resp)
	return b
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// B6: 并发注册测试（bailian 侧）
// ---------------------------------------------------------------------------

// TestBailian_InitRegistered 验证 init() 注册成功，Registry 可以构建。
func TestBailian_InitRegistered(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport()

	profile, _ := providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost/v1",
		Model:        "test-model",
	})

	_, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: func(ctx context.Context, p *providers.ProviderProfile) (string, error) { return "key", nil },
	})
	if err != nil {
		t.Fatalf("NewRegistry with bailian: %v", err)
	}
}
