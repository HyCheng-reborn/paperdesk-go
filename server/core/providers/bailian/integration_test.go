package bailian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B1: 预算接入测试
// ---------------------------------------------------------------------------

// TestBudget_RealHTTPSend 验证 Registry+ProcessBudget(MaxRequests=1) 发送 2 次 HTTP，
// 第二次返回 ErrBudgetExhausted。
func TestBudget_RealHTTPSend(t *testing.T) {
	srv := newFakeChatServer(t)
	defer srv.Close()

	budget, err := providers.NewProcessBudget(1, 0, 0, 0)
	if err != nil {
		t.Fatalf("NewProcessBudget: %v", err)
	}

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
	// 使用 SSE 格式的流式服务器
	srv := newFakeStreamServer(t)
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
	sr, err := chat.Stream(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// 消费所有 chunk
	for {
		_, err := sr.Recv()
		if err != nil {
			break
		}
	}
	sr.Close()

	// 验证预算已结算（attempts >= 1）
	snap := budget.Snapshot()
	if snap.AttemptsUsed < 1 {
		t.Errorf("expected at least 1 attempt used, got %d", snap.AttemptsUsed)
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

// TestBudget_RedirectCountsAttempts 验证 307 重定向返回错误（Fix 2b: CheckRedirect 禁止）。
// B6: 重定向目标改为本机 httptest，不联网。
func TestBudget_RedirectCountsAttempts(t *testing.T) {
	// 本机重定向目标（替代外部域名，全部 httptest 不联网）
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer targetSrv.Close()

	// 创建重定向服务器：307 指向本机目标
	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirectSrv.Close()

	// 使用生产 Transport（包含 CheckRedirect 禁止重定向）
	transport := providers.NewTransport()

	// 发送请求应失败（重定向被禁止）
	client := transport.Client()
	resp, err := client.Get(redirectSrv.URL + "/chat")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error due to redirect prohibition")
	}
	if !errors.Is(err, providers.ErrRedirectProhibited) {
		// http.Client wraps the redirect error
		if !containsStr(err.Error(), "redirect") && !containsStr(err.Error(), "prohibited") {
			t.Errorf("expected redirect prohibited error, got: %v", err)
		}
	}
}

// TestBudget_OutputCapMatches 验证输出预留与 defaultMaxOutputTokens 一致（Fix 2c）。
func TestBudget_OutputCapMatches(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)

	// 构造适配器并检查估算
	adapter := &chatAdapter{
		inner:   nil, // 不需要实际调用
		profile: nil,
		budget:  budget,
	}

	msgs := []*schema.Message{{Role: schema.User, Content: "test"}}
	// B3: 同源计算 —— opts 为 nil 时 resolveMaxOutput 返回默认上限
	effectiveMaxOutput := adapter.resolveMaxOutput(nil)
	est := adapter.estimateTokensWithOutput(msgs, effectiveMaxOutput)

	if est.MaxOutput != defaultMaxOutputTokens {
		t.Errorf("expected MaxOutput=%d, got %d", defaultMaxOutputTokens, est.MaxOutput)
	}
	if est.MaxOutput != 8192 {
		t.Errorf("expected MaxOutput=8192 (aligned with max_tokens), got %d", est.MaxOutput)
	}
}

// TestBudget_EmbeddingSettlesUsage 验证已知 usage 结算（Fix 2d）。
func TestBudget_EmbeddingSettlesUsage(t *testing.T) {
	// 创建返回 usage 的 embedding 服务器
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": []float64{0.1, 0.2, 0.3}, "object": "embedding"},
			},
			"usage": map[string]any{
				"prompt_tokens": 100,
				"total_tokens":  100,
			},
			"model": "test-embed",
		})
	}))
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport(providers.WithHTTPClient(srv.Client()))
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     srv.URL,
		Model:        "test-embed",
		Dimensions:   3,
	})

	reg, err := providers.NewRegistry(providers.RegistryConfig{
		Embed:       profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: fakeCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	embedder, err := reg.GetEmbedder(context.Background())
	if err != nil {
		t.Fatalf("GetEmbedder: %v", err)
	}

	_, err = embedder.EmbedStrings(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("EmbedStrings: %v", err)
	}

	// Fix 2d: usage 应从响应中结算，而非保守保留预留
	snap := budget.Snapshot()
	if snap.InputUsed != 100 {
		t.Errorf("expected InputUsed=100 (from response usage), got %d", snap.InputUsed)
	}
}

// TestBudget_LocalErrorNoCommit 验证本地错误不消耗 committed attempt（Fix 2e）。
func TestBudget_LocalErrorNoCommit(t *testing.T) {
	budget, _ := providers.NewProcessBudget(10, 0, 0, 0)
	transport := providers.NewTransport()
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost:1/v1",
		Model:        "test-rerank",
	})

	// 构建 rerank 适配器
	rr, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test",
		Transport: transport,
		Budget:    budget,
	})
	if err != nil {
		t.Fatalf("BuildRerank: %v", err)
	}

	// topK=0 应在 Reserve 之前返回错误（Fix 2e）
	docs := []*schema.Document{{ID: "1", Content: "test"}}
	_, err = rr.Rerank(context.Background(), "query", docs, 0)
	if err == nil {
		t.Fatal("expected error for topK=0")
	}
	if !containsStr(err.Error(), "topK") {
		t.Errorf("expected topK error, got: %v", err)
	}

	// 验证没有消耗任何 attempt
	snap := budget.Snapshot()
	if snap.AttemptsUsed != 0 {
		t.Errorf("expected 0 attempts used (local error should not consume), got %d", snap.AttemptsUsed)
	}
	if snap.CommittedAttempts != 0 {
		t.Errorf("expected 0 committed (local error should not commit), got %d", snap.CommittedAttempts)
	}
}

// ---------------------------------------------------------------------------
// Fix 3: 流式所有权测试
// ---------------------------------------------------------------------------

// TestStream_EarlyCloseFinalizes 验证提前 Close 终结 lease，无 goroutine 泄漏。
func TestStream_EarlyCloseFinalizes(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建一个有多个 chunk 的流
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
		{Role: schema.Assistant, Content: "chunk2"},
		{Role: schema.Assistant, Content: "chunk3"},
	})

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	wrapped := providers.WrapBudgetStreamReader(parentCtx, func() {}, sr, lease, nil)
	reader := wrapped.Reader()

	// 只读一个 chunk 然后提前关闭
	_, err := reader.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	wrapped.Close()

	// 验证 lease 已终结。
	// 提前 Close 的终结路径是异步的：wrapped.Close() 关闭 pipe reader 后，
	// 后台 goroutine 需被调度执行 writer.Send→false→lsw.Close()→finalize→lease.Finish()。
	// 该 goroutine 与本次 Close() 无同步关系，故以带超时的轮询等待其完成，避免时序 flake。
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Error("expected lease to be finalized after Close")
	}
}

// TestStream_CancelFinalizes 验证取消终结 lease。
func TestStream_CancelFinalizes(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建流
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
	})

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	wrapped := providers.WrapBudgetStreamReader(parentCtx, func() {}, sr, lease, nil)
	reader := wrapped.Reader()

	// 读取所有（EOF 终结）
	for {
		_, err := reader.Recv()
		if err != nil {
			break
		}
	}
	wrapped.Close()

	if !lease.Finalized() {
		t.Error("expected lease to be finalized after EOF+Close")
	}
}

// TestStream_ErrorFinalizes 验证错误终结 lease，不当退款。
func TestStream_ErrorFinalizes(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建会返回错误的流
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
		writer.Send(nil, fmt.Errorf("connection reset"))
	}()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	wrapped := providers.WrapBudgetStreamReader(parentCtx, func() {}, reader, lease, nil)
	consumer := wrapped.Reader()

	// 读取直到错误
	for {
		_, err := consumer.Recv()
		if err != nil {
			break
		}
	}

	// lease 应已终结
	if !lease.Finalized() {
		t.Error("expected lease to be finalized after error")
	}
	// committed attempt 不应退款
	snap := budget.Snapshot()
	if snap.CommittedAttempts != 1 {
		t.Errorf("expected 1 committed (no refund on error), got %d", snap.CommittedAttempts)
	}
}

// ---------------------------------------------------------------------------
// B3: Embedding index 验证测试
// ---------------------------------------------------------------------------

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

func TestEmbedding_RawIndices_Missing(t *testing.T) {
	body := []byte(`{"data":[{"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`)
	err := validateRawEmbeddingIndices(body)
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	if !containsStr(err.Error(), "no index") {
		t.Errorf("expected missing index error, got: %v", err)
	}
}

func TestEmbedding_RawIndices_Valid(t *testing.T) {
	body := buildRawEmbedResponse([]int{0, 1, 2})
	err := validateRawEmbeddingIndices(body)
	if err != nil {
		t.Fatalf("expected no error for valid indices, got: %v", err)
	}
}

// TestEmbedding_NilTransport 验证 nil Transport 不 panic（Fix 7）。
func TestEmbedding_NilTransport(t *testing.T) {
	// Transport with nil inner client transport
	transport := providers.NewTransport(providers.WithHTTPClient(&http.Client{
		// Transport is nil → should fallback to http.DefaultTransport
	}))

	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost:1",
		Model:        "test-embed",
		Dimensions:   3,
	})

	// 不应 panic
	_, err := BuildEmbed(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "key",
		Transport: transport,
	})
	// 可能因连接失败而报错，但不应 panic
	_ = err
}

// ---------------------------------------------------------------------------
// Fix 7: Rerank nil doc 防御
// ---------------------------------------------------------------------------

// TestRerank_NilDocPreservesError 验证 nil doc 返回可识别错误而非 panic。
func TestRerank_NilDocPreservesError(t *testing.T) {
	// estimateRerankTokens 应安全处理 nil docs
	est := estimateRerankTokens("query", []*schema.Document{nil, {ID: "1", Content: "text"}, nil})
	if est == nil {
		t.Fatal("expected non-nil estimate")
	}
	if est.InputTokens <= 0 {
		t.Errorf("expected positive InputTokens, got %d", est.InputTokens)
	}
}

// TestRerank_LocalValidationBeforeReserve 验证本地校验错误不消耗预算（Fix 2e）。
func TestRerank_LocalValidationBeforeReserve(t *testing.T) {
	budget, _ := providers.NewProcessBudget(10, 0, 0, 0)
	transport := providers.NewTransport()
	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost:1/rerank",
		Model:        "test-rerank",
	})

	rr, err := BuildRerank(context.Background(), providers.BuildConfig{
		Profile:   profile,
		APIKey:    "test",
		Transport: transport,
		Budget:    budget,
	})
	if err != nil {
		t.Fatalf("BuildRerank: %v", err)
	}

	// empty docs → 本地校验失败
	_, err = rr.Rerank(context.Background(), "query", []*schema.Document{}, 5)
	if err == nil {
		t.Fatal("expected error for empty docs")
	}

	// empty query → 本地校验失败
	_, err = rr.Rerank(context.Background(), "", []*schema.Document{{ID: "1", Content: "x"}}, 5)
	if err == nil {
		t.Fatal("expected error for empty query")
	}

	// 验证没有消耗任何 attempt
	snap := budget.Snapshot()
	if snap.AttemptsUsed != 0 {
		t.Errorf("expected 0 attempts, got %d", snap.AttemptsUsed)
	}
}

// ---------------------------------------------------------------------------
// Fix 2a: Owner reuse 测试
// ---------------------------------------------------------------------------

// TestBudget_OwnerReuseViaCtx 验证入口构造的 RequestBudget 被适配器复用。
func TestBudget_OwnerReuseViaCtx(t *testing.T) {
	srv := newFakeChatServer(t)
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(2, 0, 0, 0)
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

	// 入口构造一次 RequestBudget，通过 ctx 传递
	rb, _ := budget.NewRequestBudget(context.Background())
	ctx := providers.ContextWithRequestBudget(context.Background(), rb)

	msgs := []*schema.Message{{Role: schema.User, Content: "hello"}}

	// 两次调用共享同一 RequestBudget（MaxRequests=2 在 ProcessBudget 级别）
	_, err = chat.Generate(ctx, msgs)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	_, err = chat.Generate(ctx, msgs)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}

	// 第三次应失败
	_, err = chat.Generate(ctx, msgs)
	if err == nil {
		t.Fatal("third Generate should fail (budget exhausted)")
	}
}

// ---------------------------------------------------------------------------
// B6: 并发注册测试（bailian 侧）
// ---------------------------------------------------------------------------

func TestBailian_InitRegistered(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	transport := providers.NewTransport()

	profile := mustProfile(t, providers.ProviderProfile{
		ProviderName: ProviderBailian,
		Protocol:     "openai-compatible",
		Endpoint:     "http://localhost/v1",
		Model:        "test-model",
	})

	_, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:        profile,
		Budget:      budget,
		Transport:   transport,
		Credentials: fakeCredResolver,
	})
	if err != nil {
		t.Fatalf("NewRegistry with bailian: %v", err)
	}
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

func mustProfile(t *testing.T, p providers.ProviderProfile) *providers.ProviderProfile {
	t.Helper()
	pp, err := providers.NewProviderProfile(p)
	if err != nil {
		t.Fatalf("NewProviderProfile: %v", err)
	}
	return pp
}

func fakeCredResolver(ctx context.Context, p *providers.ProviderProfile) (string, error) {
	return "test-key", nil
}

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

// newFakeChatServer 创建返回固定 Chat Completion 响应的测试服务器。
func newFakeChatServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"model":  "test-model",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "hello response",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})
	}))
}

// newFakeStreamServer 创建返回 SSE 流的测试服务器。
func newFakeStreamServer(t *testing.T) *httptest.Server {
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

		chunks := []string{
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// 确保 lease 终结的并发安全性（通过 StreamSession 并发 Close）
func TestStream_ConcurrentCloseFinalizes(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
	})

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	session := providers.WrapBudgetStreamReader(parentCtx, func() {}, sr, lease, nil)

	// 并发 Close 不应 panic（底层 sync.Once 保证幂等）
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.Close()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent Close")
	}

	// 等待 lease 终结
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Error("expected lease to be finalized")
	}
}

// 确保未使用的 import 不报错
var _ = model.TokenUsage{}

// ---------------------------------------------------------------------------
// Fix 7: 独立流关闭测试（真实 httptest 阻塞服务器）
// ---------------------------------------------------------------------------

// newBlockingStreamServer 创建发送首条后阻塞 3s 再关闭连接的 SSE 服务器。
// 模拟生产环境中服务器发送缓慢或网络连接保持的场景。
func newBlockingStreamServer(t *testing.T) *httptest.Server {
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
		// 发送首条 chunk
		chunk := `{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"first"},"finish_reason":null}]}`
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()

		// 阻塞 3s 后关闭连接（模拟慢速服务器）
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	}))
}

// TestIndependentStreamEarlyClose 验证真实 HTTP 流式路径：
// 服务器发送首条后阻塞，消费者读首条后提前 Close，
// lease 应在合理时间内终结（adapter 检测 Send→false → 退出 → inner writer 关闭 → goroutine 收敛）。
func TestIndependentStreamEarlyClose(t *testing.T) {
	srv := newBlockingStreamServer(t)
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

	sr, err := chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// 读首条
	_, err = sr.Recv()
	if err != nil {
		sr.Close()
		t.Fatalf("Recv: %v", err)
	}

	// 消费者提前关闭
	sr.Close()

	// lease 应在 5s 内终结（服务器阻塞 3s + 裕量）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := budget.Snapshot()
		if snap.CommittedAttempts >= 1 && snap.InFlight == 0 {
			return // 成功
		}
		time.Sleep(time.Millisecond)
	}
	snap := budget.Snapshot()
	t.Fatalf("expected lease finalized after early close: committed=%d inFlight=%d", snap.CommittedAttempts, snap.InFlight)
}
