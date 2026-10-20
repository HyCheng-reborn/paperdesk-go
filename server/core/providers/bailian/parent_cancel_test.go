package bailian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// 第十一轮：百炼父取消背压修复验证
//
// 修复前 wrapLeaseStreamReader 丢弃 parentCtx，没有 parentCtx watcher，
// 转发 goroutine 是纯 for 循环无 select。当输出满且消费者不消费时，
// 仅取消父 ctx 无法使流收敛。
//
// 修复后 bailian.chatAdapter.StreamSession 改用 providers.WrapBudgetStreamReader，
// 传入父 ctx，使父 ctx 单独取消也能收敛（P1-2）。
// ---------------------------------------------------------------------------

// newSlowSSEServer 返回一个发送指定数量 chunk 后阻塞的 SSE 服务器。
// blocked 信号在发送完所有 chunk 后关闭，用于同步"已发送但未结束"状态。
func newSlowSSEServer(t *testing.T, chunks []string, blocked chan struct{}) *httptest.Server {
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
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		// 通知测试：chunk 已全部发送
		if blocked != nil {
			close(blocked)
		}
		// 阻塞直到请求上下文取消或超时
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
}

// TestBailianStreamSession_ParentCancelUnderBackpressure 验证核心修复：
// 真实 Registry → bailian builder → 锁定 SDK → httptest SSE。
// 输出满且不消费，只 cancel 父 ctx，等待 Done 收敛，确认 slot=0、所有任务退出。
func TestBailianStreamSession_ParentCancelUnderBackpressure(t *testing.T) {
	blocked := make(chan struct{})
	chunks := []string{
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"A"},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"B"},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"C"},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`,
	}
	srv := newSlowSSEServer(t, chunks, blocked)
	defer srv.Close()

	budget, _ := providers.NewProcessBudget(1, 0, 0, 0) // MaxRequests=1
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

	// 创建父 ctx 并取消（不消费、不 Close）
	parentCtx, parentCancel := context.WithCancel(context.Background())
	sessioner := chat.(providers.StreamSessioner)
	session, err := sessioner.StreamSession(parentCtx, []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		parentCancel()
		t.Fatalf("StreamSession: %v", err)
	}
	sr := session.Reader()

	// 等待服务器发送完所有 chunk（确保 pipe 内有背压）
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		parentCancel()
		sr.Close()
		t.Fatal("timeout waiting for server to send chunks")
	}

	// 关键：只取消父 ctx，不调用 Close/Drain
	parentCancel()

	// 等待 Done 收敛（所有任务退出）
	done := session.Done()
	select {
	case <-done:
		// 成功收敛
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancel under backpressure: Done() did not converge within timeout")
	}

	// 验证 slot 已释放（InFlight=0）
	snap := budget.Snapshot()
	if snap.InFlight != 0 {
		t.Errorf("expected InFlight=0 after parent cancel, got %d", snap.InFlight)
	}
	// 验证 lease 已终结
	if snap.CommittedAttempts < 1 {
		t.Errorf("expected at least 1 committed attempt, got %d", snap.CommittedAttempts)
	}
}

// TestBailianStreamSession_ExplicitClose 验证显式 Close 后 Done 收敛。
func TestBailianStreamSession_ExplicitClose(t *testing.T) {
	chunks := []string{
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
		`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	}
	srv := newSlowSSEServer(t, chunks, nil)
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

	sessioner := chat.(providers.StreamSessioner)
	session, err := sessioner.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}
	sr := session.Reader()

	// 读一个 chunk
	_, err = sr.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}

	// 显式 Close
	session.Close()

	// Done 应收敛
	select {
	case <-session.Done():
		// 成功
	case <-time.After(5 * time.Second):
		t.Fatal("explicit Close: Done() did not converge within timeout")
	}
}

// TestBailianStreamSession_EOFSettles 验证 EOF 正常结算。
func TestBailianStreamSession_EOFSettles(t *testing.T) {
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

	sessioner := chat.(providers.StreamSessioner)
	session, err := sessioner.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}
	sr := session.Reader()

	// 消费到 EOF
	var sb strings.Builder
	for {
		msg, rerr := sr.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("unexpected Recv error: %v", rerr)
			}
			break
		}
		if msg != nil {
			sb.WriteString(msg.Content)
		}
	}
	session.Close()

	// 验证内容完整
	if got := sb.String(); got != "Hello world" {
		t.Errorf("content = %q, want %q", got, "Hello world")
	}

	// 验证 Done 收敛
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("EOF settle: Done() did not converge")
	}

	// 验证预算已结算
	snap := budget.Snapshot()
	if snap.AttemptsUsed < 1 {
		t.Errorf("expected at least 1 attempt used after EOF, got %d", snap.AttemptsUsed)
	}
}

// TestBailianStreamSession_ReadError 验证读错误传播。
func TestBailianStreamSession_ReadError(t *testing.T) {
	// 服务器返回 500 错误
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"internal server error"}}`)
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

	// Stream 调用应返回错误（500 在构造阶段就会被检测到）
	_, err = chat.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

// TestBailianStreamSession_ConcurrentCloseEOF 验证并发 Close 和 EOF 幂等。
func TestBailianStreamSession_ConcurrentCloseEOF(t *testing.T) {
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

	sessioner := chat.(providers.StreamSessioner)
	session, err := sessioner.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}
	sr := session.Reader()

	// 消费到 EOF
	for {
		_, rerr := sr.Recv()
		if rerr != nil {
			break
		}
	}

	// 并发 Close 不应 panic
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.Close()
		}()
	}
	wg.Wait()

	// Done 应收敛
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Close+EOF: Done() did not converge")
	}

	// 单次结算
	snap := budget.Snapshot()
	if snap.CommittedAttempts != 1 {
		t.Errorf("expected exactly 1 committed attempt, got %d", snap.CommittedAttempts)
	}
}

// TestBailianStreamSession_NoBudgetFullContent 验证无预算时 ABC 完整到达。
func TestBailianStreamSession_NoBudgetFullContent(t *testing.T) {
	srv := newMultiChunkStreamServer(t)
	defer srv.Close()

	// 无预算限制（budget with zero limits）
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

	sessioner := chat.(providers.StreamSessioner)
	session, err := sessioner.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}
	sr := session.Reader()
	defer session.Close()

	var sb strings.Builder
	for {
		msg, rerr := sr.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("Recv error: %v", rerr)
			}
			break
		}
		if msg != nil {
			sb.WriteString(msg.Content)
		}
	}

	// 无预算时 ABC 完整到达
	if got := sb.String(); got != "ABC" {
		t.Fatalf("content = %q, want %q (no-budget full content)", got, "ABC")
	}
}
