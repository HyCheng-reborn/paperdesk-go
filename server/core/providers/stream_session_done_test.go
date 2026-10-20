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

	session := wrapBudgetStreamReader(streamCancel, srcReader, lease, nil)
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

	session := wrapBudgetStreamReader(streamCancel, srcReader, nil, nil)
	_ = ctx

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

	session := wrapBudgetStreamReader(streamCancel, srcReader, nil, nil)

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
