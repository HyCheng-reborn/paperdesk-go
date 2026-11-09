package providers

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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Done 覆盖所有任务测试
// ---------------------------------------------------------------------------

// ctxAwareSource creates a context-aware source Pipe. When ctx is cancelled,
// the writer is closed, unblocking any Recv() on the reader.
// This simulates production behavior where HTTP body read is interrupted by ctx cancel.
func ctxAwareSource(ctx context.Context, buf int) (*schema.StreamReader[*schema.Message], *schema.StreamWriter[*schema.Message]) {
	r, w := schema.Pipe[*schema.Message](buf)
	go func() {
		<-ctx.Done()
		w.Close() // close writer → items channel closed → Recv() returns EOF
	}()
	return r, w
}

// TestStreamSession_CloseWaitsRecv verifies Close waits for recv goroutine to exit.
// Uses a context-aware source that responds to ctx cancel by closing the writer.
func TestStreamSession_CloseWaitsRecv(t *testing.T) {
	ctx, streamCancel := context.WithCancel(context.Background())
	srcReader, srcWriter := ctxAwareSource(ctx, 1)

	// Use a lease to verify finalization
	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(ctx)
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	session := WrapBudgetStreamReader(ctx, streamCancel, srcReader, lease, nil)
	reader := session.Reader()

	// Send one chunk
	srcWriter.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)

	// Read the chunk
	_, err := reader.Recv()
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	// Now close the session. The recv goroutine is blocked on bsr.Recv()
	// (srcReader.Recv blocks because srcWriter is still open).
	// Close should: cancel ctx → srcWriter closed by ctx watcher → recv gets EOF → exits.
	closeStarted := time.Now()
	closeDone := make(chan struct{})
	go func() {
		session.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		elapsed := time.Since(closeStarted)
		if elapsed > 5*time.Second {
			t.Errorf("Close took too long: %v", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return within 10s — recv goroutine may be stuck")
	}

	// done should be closed (all goroutines exited)
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session.Done() not closed after Close returned")
	}

	// Lease should be finalized
	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Error("expected lease to be finalized after Close")
	}

	// Clean up: srcWriter is already closed by ctx watcher goroutine
}

// TestStreamSession_ParentCancelBackpressure verifies parent ctx cancel + output
// backpressure (pipe full, consumer not reading) converges.
func TestStreamSession_ParentCancelBackpressure(t *testing.T) {
	ctx, ctxCancel := context.WithCancel(context.Background())
	streamCtx, streamCancel := context.WithCancel(ctx)

	// Create a context-aware source
	srcReader, srcWriter := ctxAwareSource(streamCtx, 1)

	session := WrapBudgetStreamReader(ctx, streamCancel, srcReader, nil, nil)

	reader := session.Reader()

	// Fill the pipe buffer (cap=1) by NOT reading from reader
	// Meanwhile, send data to srcReader to fill the dataCh buffer
	srcWriter.Send(&schema.Message{Role: schema.Assistant, Content: "chunk1"}, nil)
	srcWriter.Send(&schema.Message{Role: schema.Assistant, Content: "chunk2"}, nil)

	// Give goroutines time to start and fill buffers
	time.Sleep(50 * time.Millisecond)

	// Cancel parent ctx — this should cause the session to converge
	// even though the pipe is full and consumer is not reading
	ctxCancel()

	// Close the session — should converge quickly
	closeDone := make(chan struct{})
	go func() {
		session.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("session.Close() hung under backpressure + parent cancel")
	}

	// done should be closed
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session.Done() not closed after Close")
	}

	// Clean up: srcWriter is already closed by ctx watcher
	_ = reader
}

// TestStreamSession_ParentCancelBackpressure_RealCancel verifies that when the
// parent ctx is cancelled, the session's Done channel closes even under backpressure.
func TestStreamSession_ParentCancelBackpressure_RealCancel(t *testing.T) {
	ctx, streamCancel := context.WithCancel(context.Background())

	// Create a context-aware blocking source
	srcReader, srcWriter := ctxAwareSource(ctx, 1)

	session := WrapBudgetStreamReader(ctx, streamCancel, srcReader, nil, nil)

	// Don't read from session — create backpressure
	// Fill dataCh by sending to srcWriter
	srcWriter.Send(&schema.Message{Role: schema.Assistant, Content: "a"}, nil)
	srcWriter.Send(&schema.Message{Role: schema.Assistant, Content: "b"}, nil)

	time.Sleep(50 * time.Millisecond)

	// Cancel via streamCancel (simulates parent ctx cancel)
	streamCancel()

	// Close session — should converge
	done := make(chan struct{})
	go func() {
		session.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session.Close() hung after streamCancel under backpressure")
	}

	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session.Done() not closed")
	}

	// srcWriter is already closed by ctx watcher
}

// TestStreamSession_CloseInterruptsHTTP_RealHTTP verifies Close interrupts a
// real HTTP connection (using httptest, not just FakeChat).
func TestStreamSession_CloseInterruptsHTTP_RealHTTP(t *testing.T) {
	// Track when the HTTP handler's context is cancelled
	handlerCtxCancelled := make(chan struct{})

	// Create a real HTTP server that sends one SSE chunk then blocks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		// Send first chunk
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		flusher.Flush()

		// Wait for client to disconnect (ctx cancelled)
		<-r.Context().Done()
		close(handlerCtxCancelled)
	}))
	defer srv.Close()

	// Create a fake model that wraps the real HTTP connection
	fake := NewFakeChat()
	fake.SetStream(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
		// Make a real HTTP request to the test server
		// The request will be cancelled when ctx is cancelled
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// ctx cancelled during request — return a blocking reader that exits on ctx done
			reader, writer := schema.Pipe[*schema.Message](1)
			go func() {
				defer writer.Close()
				<-ctx.Done()
			}()
			return reader, nil
		}

		// Parse SSE response (simplified: just read the body)
		reader, writer := schema.Pipe[*schema.Message](10)
		go func() {
			defer writer.Close()
			defer resp.Body.Close()
			buf := make([]byte, 4096)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					body := string(buf[:n])
					// Extract content from SSE data
					for _, line := range strings.Split(body, "\n") {
						if strings.HasPrefix(line, "data: ") {
							writer.Send(&schema.Message{Role: schema.Assistant, Content: "first"}, nil)
						}
					}
				}
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) {
						// Connection reset or cancelled — normal during Close
					}
					return
				}
			}
		}()
		return reader, nil
	})

	budget, _ := NewProcessBudget(0, 0, 0, 0)
	role := &budgetChatRole{inner: fake, budget: budget}

	session, err := role.StreamSession(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamSession: %v", err)
	}

	reader := session.Reader()
	_, err = reader.Recv()
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	// Close session — should interrupt the HTTP connection
	session.Close()

	// Handler ctx should be cancelled (HTTP connection interrupted)
	select {
	case <-handlerCtxCancelled:
		// OK — the real HTTP connection was interrupted
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler ctx not cancelled after session.Close() — connection not interrupted")
	}

	// session.Done() should be closed
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session.Done() not closed after Close")
	}
}

// ---------------------------------------------------------------------------
// P1-2: 临时 Recv goroutine 必须登记到 WaitGroup；父 ctx 取消必须主动 cancel
// ---------------------------------------------------------------------------

// blockingRecvSource 返回一个源 reader：它的首次 Recv 会阻塞，直到 gate 关闭。
// entered（缓冲 1）在 Recv 真正进入阻塞前收到一个信号，使测试能确定性地知道
// “临时 goroutine 已经停在 bsr.Recv 内部”，而不是依赖固定 sleep。
//
// 实现上用 StreamReaderWithConvert 包一层 Pipe：阻塞发生在 convert 回调里，
// 因此 closeSrc（closeRecv）无法解除它 —— 只有 gate 能。
func blockingRecvSource(gate <-chan struct{}, entered chan<- struct{}) *schema.StreamReader[*schema.Message] {
	inner, innerW := schema.Pipe[*schema.Message](1)
	innerW.Send(&schema.Message{Role: schema.Assistant, Content: "blocked-chunk"}, nil)
	innerW.Close()
	return schema.StreamReaderWithConvert(inner, func(m *schema.Message) (*schema.Message, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
		return m, nil
	})
}

// closeDetectingSource 返回一个“仅靠 reader Close 才能终止”的源：
// 生产者完全不感知 ctx，只在 Send 返回 true（reader 已 closeRecv）时退出并关闭 writer。
// 因此唯有 cancel() → closeSrc() 能让阻塞中的 Recv 返回 EOF。
func closeDetectingSource(buf int) *schema.StreamReader[*schema.Message] {
	r, w := schema.Pipe[*schema.Message](buf)
	go func() {
		defer w.Close()
		for i := 0; ; i++ {
			msg := &schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("cd%d", i)}
			if w.Send(msg, nil) {
				return // reader 已关闭 → 退出，defer 关闭 writer → Recv 返回 EOF
			}
		}
	}()
	return r
}

// TestStreamSession_TempGoroutineTracked 验证 P1-2：执行 bsr.Recv 的临时 goroutine
// 已登记到 WaitGroup，done/Close 不会在它仍阻塞于 Recv 时提前返回。
//
// 失败条件（未登记时）：Close() 只等 supervisor + forward，两者在 cancelSig 关闭后立即退出，
// done 随之关闭，而临时 goroutine 仍持有上游连接阻塞在 Recv 中。
func TestStreamSession_TempGoroutineTracked(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	streamCtx, streamCancel := context.WithCancel(parentCtx)
	defer streamCancel()

	src := blockingRecvSource(gate, entered)

	budget, _ := NewProcessBudget(0, 0, 0, 0)
	rb, _ := budget.NewRequestBudget(streamCtx)
	lease, _ := rb.Reserve(&TokenEstimate{InputTokens: 10, MaxOutput: 100})
	lease.Commit()

	session := WrapBudgetStreamReader(parentCtx, streamCancel, src, lease, nil)

	// 确定性信号：临时 goroutine 已进入 bsr.Recv 并阻塞
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("temp goroutine never entered bsr.Recv")
	}

	// Close 在独立 goroutine 中执行：它要等 done，而 done 必须等临时 goroutine 退出
	closeReturned := make(chan struct{})
	go func() {
		session.Close()
		close(closeReturned)
	}()

	// 关键断言：gate 未释放前，done 与 Close 都不得完成
	select {
	case <-session.Done():
		t.Fatal("session.Done() closed while temp goroutine is still blocked in Recv — WG does not track it")
	case <-closeReturned:
		t.Fatal("session.Close() returned while temp goroutine is still blocked in Recv — WG does not track it")
	case <-time.After(200 * time.Millisecond):
		// 预期：两者均未完成
	}

	// 释放 gate：临时 goroutine 从 Recv 返回 → wg 归零 → done 关闭 → Close 返回
	close(gate)

	select {
	case <-session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session.Done() not closed after temp goroutine unblocked")
	}
	select {
	case <-closeReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("session.Close() did not return after temp goroutine unblocked")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !lease.Finalized() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !lease.Finalized() {
		t.Error("expected lease to be finalized after all goroutines exited")
	}
}

// TestStreamSession_ParentCancelOnly 验证 P1-2：仅取消父 ctx（消费者不调用
// Close/Drain）也能让 done 关闭 —— 由 parentCtx watcher 主动触发 cancel()，
// 进而 streamCancel + closeSrc + close(cancelSig)。
//
// 源故意不感知 ctx（closeDetectingSource），因此未修复时 done 永远不会关闭。
func TestStreamSession_ParentCancelOnly(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	streamCtx, streamCancel := context.WithCancel(parentCtx)
	defer streamCancel()
	_ = streamCtx // 本用例的源不使用 ctx：只有 closeSrc() 能终止它

	srcReader := closeDetectingSource(0)

	session := WrapBudgetStreamReader(parentCtx, streamCancel, srcReader, nil, nil)

	// 先确认管道已建立（有界等待，不依赖固定 sleep）
	if _, err := session.Reader().Recv(); err != nil {
		t.Fatalf("first Recv: %v", err)
	}

	// 只取消父 ctx，不额外调用 session.Close()/Drain()
	parentCancel()

	select {
	case <-session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session.Done() not closed after parent ctx cancel only — parentCtx watcher missing")
	}

	// 消费者侧 reader 必须终结（forward 已 defer writer.Close()）
	terminal := make(chan error, 1)
	go func() {
		for {
			_, err := session.Reader().Recv()
			if err != nil {
				terminal <- err
				return
			}
		}
	}()
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer Recv did not terminate after parent ctx cancel")
	}
}
