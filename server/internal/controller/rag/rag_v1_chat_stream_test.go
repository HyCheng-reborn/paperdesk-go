package rag

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/common"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/internal/logic/chat"
)

// TestChatStream_AnswerStreamError calls the ACTUAL ChatStream with minimal,
// instance-level, read-only-during-runtime injected dependencies so retrieval
// returns offline documents and stream creation returns a sentinel error.
func TestChatStream_AnswerStreamError(t *testing.T) {
	sentinel := errors.New("stream create sentinel")

	var retrieveCalled, answerCalled, sseCalled bool
	var gotDocs []*schema.Document

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			retrieveCalled = true
			return &v1.RetrieverRes{Document: []*schema.Document{{Content: "offline-doc"}}}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			answerCalled = true
			gotDocs = docs
			return nil, nil, sentinel
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			sseCalled = true
			return nil
		},
	}

	res, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID:        "conv-1",
		Question:      "q",
		KnowledgeName: "kb",
		TopK:          5,
		Score:         0.2,
	})

	if err == nil {
		t.Fatal("expected non-nil error on stream creation failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected errors.Is to recognize build-stream sentinel, got: %v", err)
	}
	if res != nil {
		t.Fatal("ChatStream must not return a (empty) success response when stream creation fails")
	}
	if !retrieveCalled || !answerCalled {
		t.Fatalf("expected retrieve and answerStream boundaries to run (retrieve=%v answer=%v)", retrieveCalled, answerCalled)
	}
	if len(gotDocs) != 1 || gotDocs[0].Content != "offline-doc" {
		t.Errorf("expected offline docs passed to answer stream, got: %+v", gotDocs)
	}
	if sseCalled {
		t.Fatal("ChatStream must not enter SSE output when stream creation fails")
	}
}

// TestChatStream_SSEWriteFailure_CleanupWithFailure verifies that when SSE write
// fails, cleanup is called with Success=false (no save).
func TestChatStream_SSEWriteFailure_CleanupWithFailure(t *testing.T) {
	writeErr := errors.New("sse write failed")
	var cleanupResult chat.StreamResult
	var cleanupCalled bool
	var mu sync.Mutex

	sr, sw := schema.Pipe[*schema.Message](5)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
	}()

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			return sr, func(r chat.StreamResult) {
				mu.Lock()
				defer mu.Unlock()
				cleanupCalled = true
				cleanupResult = r
			}, nil
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			reader.Recv()
			return writeErr
		},
	}

	_, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb",
	})
	if err == nil {
		t.Fatal("expected error from SSE write failure")
	}

	mu.Lock()
	defer mu.Unlock()
	if !cleanupCalled {
		t.Fatal("cleanup must be called after SSE failure")
	}
	if cleanupResult.Success {
		t.Fatal("cleanup must receive Success=false on SSE write failure")
	}
}

// TestChatStream_NormalComplete_CleanupWithSuccess verifies that when SSE
// completes normally, cleanup is called with Success=true.
func TestChatStream_NormalComplete_CleanupWithSuccess(t *testing.T) {
	var cleanupResult chat.StreamResult
	var cleanupCalled bool
	var mu sync.Mutex

	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "hello"},
		{Role: schema.Assistant, Content: " world"},
	})

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			return sr, func(r chat.StreamResult) {
				mu.Lock()
				defer mu.Unlock()
				cleanupCalled = true
				cleanupResult = r
			}, nil
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			for {
				_, err := reader.Recv()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		},
	}

	_, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !cleanupCalled {
		t.Fatal("cleanup must be called after normal completion")
	}
	if !cleanupResult.Success {
		t.Fatal("cleanup must receive Success=true on normal completion")
	}
}

// TestChatStream_CancelPropagation verifies that the derived context is cancelled
// when the controller exits, and the model observes the cancellation.
// No fixed sleep: cleanup releases all resources and waits for coordination.
func TestChatStream_CancelPropagation(t *testing.T) {
	var modelCtx context.Context
	var ctxReceived = make(chan struct{}, 1)

	sr, sw := schema.Pipe[*schema.Message](5)

	cleanupDone := make(chan struct{})

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			modelCtx = ctx
			ctxReceived <- struct{}{}
			return sr, func(r chat.StreamResult) {
				sw.Close()
				close(cleanupDone)
			}, nil
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			return errors.New("sse fail")
		},
	}

	c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb",
	})

	select {
	case <-ctxReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to receive context")
	}

	// Wait for cleanup to complete (no fixed sleep)
	select {
	case <-cleanupDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for cleanup to complete")
	}

	// After ChatStream returns and cleanup completes, the derived context should be cancelled
	if modelCtx.Err() == nil {
		t.Fatal("model context should be cancelled after ChatStream returns")
	}
}

// TestChatStream_ModelReceivesDeadlineAndValue verifies that context deadline
// and values from the parent are propagated to the model through derived context.
func TestChatStream_ModelReceivesDeadlineAndValue(t *testing.T) {
	var modelCtx context.Context
	var ctxReceived = make(chan struct{}, 1)

	sr, sw := schema.Pipe[*schema.Message](5)
	defer sw.Close()

	type ctxKey struct{}
	deadline := time.Now().Add(30 * time.Second)
	parentCtx, parentCancel := context.WithDeadline(context.Background(), deadline)
	defer parentCancel()
	parentCtx = context.WithValue(parentCtx, ctxKey{}, "test-value")

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			modelCtx = ctx
			ctxReceived <- struct{}{}
			return sr, func(r chat.StreamResult) {}, nil
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			return errors.New("sse fail")
		},
	}

	c.ChatStream(parentCtx, &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb",
	})

	select {
	case <-ctxReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to receive context")
	}

	// Model should receive the parent's context value
	if modelCtx.Value(ctxKey{}) != "test-value" {
		t.Fatal("model did not receive the parent's context value")
	}

	// Model should receive the parent's deadline
	modelDeadline, ok := modelCtx.Deadline()
	if !ok {
		t.Fatal("model context should have a deadline")
	}
	if !modelDeadline.Equal(deadline) {
		t.Errorf("model deadline %v != parent deadline %v", modelDeadline, deadline)
	}
}

// TestController_ChatIntegration_WriteFailure wires the ACTUAL
// Controller.ChatStream → actual Chat.GetAnswerStream / streamCoordinator →
// shared production SSE orchestration (common.StreamToResponseWriter) → a low-level
// ResponseWriter whose Write returns a sentinel error, i.e. a real write failure.
// It asserts the full chain, without manually cancelling to fake the failure.
func TestController_ChatIntegration_WriteFailure(t *testing.T) {
	writeSentinel := errors.New("integrated write sentinel")
	// model holds the writer open after one chunk until its ctx is cancelled
	fm := newLifecycleModel([]string{"partial"}, true)
	saver := &fakeSaver{}
	realChat := &chat.Chat{}
	chat.InjectForTest(realChat, fm, saver)

	rw := &intResponseWriter{writeErr: writeSentinel}

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			return realChat.GetAnswerStream(ctx, convID, docs, question)
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			return common.StreamToResponseWriter(ctx, rw, reader, docs)
		},
	}

	t.Cleanup(func() {
		select {
		case <-fm.done:
		case <-time.After(5 * time.Second):
			t.Error("model goroutine did not exit during cleanup")
		}
	})

	_, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb", TopK: 5, Score: 0.2,
	})
	if err == nil {
		t.Fatal("expected error from integrated SSE write failure")
	}
	// errors.Is must reach the low-level writer sentinel through the whole chain
	if !errors.Is(err, writeSentinel) {
		t.Fatalf("expected errors.Is to recognize write sentinel through full chain, got: %v", err)
	}

	// model observed cancellation (cleanupFail cancels the derived context)
	select {
	case <-fm.sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not observe cancellation")
	}
	// completion signal proves the model goroutine exited (not ctx-cancel-as-proof)
	select {
	case <-fm.done:
	case <-time.After(5 * time.Second):
		t.Fatal("model goroutine did not exit")
	}

	// Controller cleanup completed (ChatStream runs cleanup synchronously before
	// returning). No assistant message saved on write failure.
	if as := saver.assistantRecords("conv-1"); len(as) != 0 {
		t.Fatalf("no assistant must be saved on write failure, got %d records", len(as))
	}
}

// TestController_ChatIntegration_NormalComplete drives the same full production
// path but the model outputs a normal EOF and the ResponseWriter succeeds. It
// asserts the exact frames in the output and that the complete assistant message
// was saved exactly once (by role, convID, content) before ChatStream returned.
func TestController_ChatIntegration_NormalComplete(t *testing.T) {
	fm := newLifecycleModel([]string{"Hello", " world"}, false) // EOF after send
	saver := &fakeSaver{}
	realChat := &chat.Chat{}
	chat.InjectForTest(realChat, fm, saver)

	rw := &intResponseWriter{} // records, no error

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			return realChat.GetAnswerStream(ctx, convID, docs, question)
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			return common.StreamToResponseWriter(ctx, rw, reader, docs)
		},
	}

	t.Cleanup(func() {
		select {
		case <-fm.done:
		case <-time.After(5 * time.Second):
			t.Error("model goroutine did not exit during cleanup")
		}
	})

	res, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb", TopK: 5, Score: 0.2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil response on normal completion")
	}

	out := rw.String()
	if !strings.Contains(out, "data:") || !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
		t.Errorf("expected data frames carrying the content, got %q", out)
	}
	if !strings.HasSuffix(out, "data:[DONE]\n\n") {
		t.Errorf("expected trailing data:[DONE] boundary, got %q", out)
	}

	// cleanupOK waited for the background save before returning, so assert now.
	as := saver.assistantRecords("conv-1")
	if len(as) != 1 {
		t.Fatalf("expected exactly 1 assistant save for conv-1, got %d", len(as))
	}
	if as[0].role != string(schema.Assistant) || as[0].content != "Hello world" {
		t.Errorf("assistant record = %+v, want role=assistant content='Hello world'", as[0])
	}

	select {
	case <-fm.done:
	case <-time.After(5 * time.Second):
		t.Fatal("model goroutine did not exit after normal completion")
	}
}

// --- helper types for integration tests ---

// lifecycleModel streams fixed chunks and signals lifecycle events:
//   - ctxReceived: closed when Stream is called (model received a context)
//   - sawCancel: closed when the producer observes context cancellation (block mode)
//   - done: closed when the producer goroutine exits
//
// When blockAfterSend is false the writer is closed after the chunks so readers
// see EOF (normal completion); when true the writer stays open until cancelled.
type lifecycleModel struct {
	chunks         []string
	blockAfterSend bool
	mu             sync.Mutex
	receivedCtx    context.Context
	ctxReceived    chan struct{}
	sawCancel      chan struct{}
	done           chan struct{}
}

func newLifecycleModel(chunks []string, blockAfterSend bool) *lifecycleModel {
	return &lifecycleModel{
		chunks:         chunks,
		blockAfterSend: blockAfterSend,
		ctxReceived:    make(chan struct{}),
		sawCancel:      make(chan struct{}),
		done:           make(chan struct{}),
	}
}

func (m *lifecycleModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "unused"}, nil
}

func (m *lifecycleModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.receivedCtx = ctx
	m.mu.Unlock()
	close(m.ctxReceived)

	sr, sw := schema.Pipe[*schema.Message](16)
	go func() {
		defer close(m.done)
		defer sw.Close()
		for _, c := range m.chunks {
			if sw.Send(&schema.Message{Role: schema.Assistant, Content: c}, nil) {
				return // upstream closed by consumers
			}
		}
		if m.blockAfterSend {
			<-ctx.Done()
			close(m.sawCancel)
		}
	}()
	return sr, nil
}

func (m *lifecycleModel) getReceivedCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receivedCtx
}

// intResponseWriter is an http.ResponseWriter for the shared SSE path. When
// writeErr is set every Write fails with it (real low-level write failure);
// otherwise bytes are recorded for frame assertions.
type intResponseWriter struct {
	writeErr error
	header   http.Header
	buf      bytes.Buffer
}

func (w *intResponseWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.buf.Write(p)
}
func (w *intResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *intResponseWriter) WriteHeader(int) {}
func (w *intResponseWriter) Flush()          {}
func (w *intResponseWriter) String() string  { return w.buf.String() }

// savedRecord captures role/convID/content of each SaveMessage call (not just a count).
type savedRecord struct {
	role    string
	convID  string
	content string
}

// fakeSaver records SaveMessage calls per role/convID/content without a real DB.
type fakeSaver struct {
	mu         sync.Mutex
	records    []savedRecord
	saveCalled int
}

func (f *fakeSaver) SaveMessage(mess *schema.Message, convID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalled++
	f.records = append(f.records, savedRecord{role: string(mess.Role), convID: convID, content: mess.Content})
	return nil
}

func (f *fakeSaver) GetHistory(convID string, limit int) ([]*schema.Message, error) {
	return []*schema.Message{}, nil
}

func (f *fakeSaver) getSaveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveCalled
}

func (f *fakeSaver) assistantRecords(convID string) []savedRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []savedRecord
	for _, r := range f.records {
		if r.role == string(schema.Assistant) && r.convID == convID {
			out = append(out, r)
		}
	}
	return out
}
