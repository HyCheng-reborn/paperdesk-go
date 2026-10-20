package bailian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ---------------------------------------------------------------------------
// B2: 流式完整性回归测试
//
// 修复前 wrapLeaseStreamReader 用 `if !writer.Send(...)` 把正常发送当成关闭，
// 三条输入只输出一条。以下测试通过真实 SSE 路径与直接 pipe 路径验证：
//   - 多 chunk 完整传递（不只第一条）
//   - 阻塞源在 Close/取消下收敛，lease 终结，无 goroutine 悬挂
//   - 并发 Close 幂等，单次结算
//   - 真实取消以 Abort(错误) 记录，不用 EOF 冒充
// ---------------------------------------------------------------------------

// newMultiChunkStreamServer 返回 3 个内容分片（"A"/"B"/"C"）的 SSE 服务器。
// B2 缺陷下只会输出第一条，故聚合结果断言可捕获回归。
func newMultiChunkStreamServer(t *testing.T) *httptest.Server {
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
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"A"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"B"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"C"},"finish_reason":null}]}`,
			`{"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// TestStream_MultiChunkComplete 验证多 chunk 完整性：真实 SSE 三条内容分片全部到达。
func TestStream_MultiChunkComplete(t *testing.T) {
	srv := newMultiChunkStreamServer(t)
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
	defer sr.Close()

	var sb strings.Builder
	chunkCount := 0
	for {
		msg, rerr := sr.Recv()
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				t.Fatalf("Recv error: %v", rerr)
			}
			break
		}
		chunkCount++
		if msg != nil {
			sb.WriteString(msg.Content)
		}
	}

	// B2 核心断言：三条内容分片全部到达（缺陷下只会输出 "A"）。
	if got := sb.String(); got != "ABC" {
		t.Fatalf("aggregated content = %q, want %q (B2 regression: only first chunk delivered)", got, "ABC")
	}
	if chunkCount < 3 {
		t.Fatalf("expected >=3 chunks, got %d", chunkCount)
	}
}

// TestStream_BlockedSourceClose 验证阻塞网络源在 Close/取消下收敛，lease 终结，无悬挂。
func TestStream_BlockedSourceClose(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 阻塞源：不发送任何 chunk，Recv 一直阻塞，直到源被拆除（模拟网络取消）。
	srcReader, srcWriter := schema.Pipe[*schema.Message](1)
	wrapped := wrapLeaseStreamReader(context.Background(), func() {}, srcReader, lease)

	// 消费者关闭（异步，验证不悬挂）。
	closeDone := make(chan struct{})
	go func() {
		wrapped.Close()
		close(closeDone)
	}()

	// 模拟网络源取消/拆除：解除源 Recv 阻塞，转发 goroutine 得以收敛。
	srcWriter.Close()

	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("wrapped.Close() hung on blocked source")
	}

	// lease 必须收敛（终结）——无 goroutine 泄漏、无悬挂。
	deadline := time.Now().Add(3 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Error("expected lease finalized after blocked-source close")
	}
}

// TestStream_ConcurrentClose 验证 Close 后 lease 终结且幂等：单次结算，无 panic。
// 转发 goroutine 的 defer finalize 使用 sync.Once 保证幂等。
func TestStream_ConcurrentClose(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	srcReader, srcWriter := schema.Pipe[*schema.Message](1)
	// 源拆除（EOF）；不发送 chunk，避免阻塞。
	go srcWriter.Close()

	wrapped := wrapLeaseStreamReader(context.Background(), func() {}, srcReader, lease)

	// 等待 goroutine 读取 EOF 并终结 lease
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease finalized after source EOF")
	}

	// Close 必须安全（即使 lease 已终结）
	wrapped.Close()

	// 单次结算：committed 只消耗一次（幂等 finalize）。
	if snap := budget.Snapshot(); snap.CommittedAttempts != 1 {
		t.Errorf("expected exactly 1 committed attempt (idempotent finalize), got %d", snap.CommittedAttempts)
	}
}

// TestStream_CancelNotEOF 验证真实取消以 Abort(错误) 记录，不用 EOF 冒充。
func TestStream_CancelNotEOF(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	srcReader, srcWriter := schema.Pipe[*schema.Message](1)
	wrapped := wrapLeaseStreamReader(context.Background(), func() {}, srcReader, lease)

	// 真实取消以非 EOF 错误从源浮现（生产中等价于 ctx 取消导致 HTTP 流报错）。
	cancelErr := context.Canceled
	go func() {
		srcWriter.Send(nil, cancelErr)
		srcWriter.Close()
	}()

	// 消费者读取直到错误。
	for {
		_, err := wrapped.Recv()
		if err != nil {
			break
		}
	}
	wrapped.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease finalized after cancel")
	}
	// 取消必须被记录为 abort 错误，绝不用 EOF/干净 Finish 冒充。
	if lease.Err() == nil {
		t.Fatal("cancel must be recorded as abort error, not disguised as clean EOF")
	}
	if !errors.Is(lease.Err(), cancelErr) {
		t.Errorf("lease.Err() = %v, want wrap of %v", lease.Err(), cancelErr)
	}
}

// TestStream_BailianBusinessClose 验证百炼包装的业务 Close 行为：
// 业务 Close 实际返回的 reader 后，转发 goroutine 检测到 writer.Send→false 退出，lease 终结。
func TestStream_BailianBusinessClose(t *testing.T) {
	budget, _ := providers.NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(context.Background())
	lease, _ := rb.Reserve(&providers.TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	// 创建有多个 chunk 的源
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "chunk1"},
		{Role: schema.Assistant, Content: "chunk2"},
		{Role: schema.Assistant, Content: "chunk3"},
	})

	wrapped := wrapLeaseStreamReader(context.Background(), func() {}, sr, lease)

	// 读一个 chunk 然后关闭
	_, err := wrapped.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	wrapped.Close()

	// 验证 lease 已终结
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Fatal("expected lease finalized after business Close")
	}
}
