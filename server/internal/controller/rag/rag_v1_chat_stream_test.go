package rag

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

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

// TestController_ChatIntegration_WriteFailure uses actual Controller + actual
// Chat.GetAnswerStream + production streamCoordinator + shared SSE orchestration
// to verify the full lifecycle under write failure.
func TestController_ChatIntegration_WriteFailure(t *testing.T) {
	// -- real Chat with fake dependencies --
	fm := &ctxCapturingModel{ctxReceived: make(chan struct{})}
	saver := &fakeSaver{}
	realChat := &chat.Chat{}
	// Inject fake dependencies via exported test helper
	chat.InjectForTest(realChat, fm, saver)

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			return &v1.RetrieverRes{Document: nil}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
			return realChat.GetAnswerStream(ctx, convID, docs, question)
		},
		chatSSEWrite: func(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
			chunk, err := reader.Recv()
			if err != nil {
				return err
			}
			if chunk == nil {
				return errors.New("nil chunk")
			}
			return errors.New("sse write failed")
		},
	}

	_, err := c.ChatStream(context.Background(), &v1.ChatStreamReq{
		ConvID: "conv-1", Question: "q", KnowledgeName: "kb",
	})
	if err == nil {
		t.Fatal("expected error from SSE write failure")
	}

	// Wait for model to receive context
	select {
	case <-fm.ctxReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to receive context")
	}

	// Model context should be cancelled (cleanupFail cancels derived ctx)
	modelCtx := fm.getReceivedCtx()
	if modelCtx.Err() == nil {
		t.Error("model context should be cancelled after cleanup")
	}

	// No assistant save (only user question from docsMessages, if any)
	if saver.getSaveCount() > 1 {
		t.Errorf("expected at most 1 save (user question), got %d", saver.getSaveCount())
	}
}

// --- helper types for integration test ---

// ctxCapturingModel captures the context passed to Stream for assertion.
type ctxCapturingModel struct {
	mu          sync.Mutex
	receivedCtx context.Context
	ctxReceived chan struct{}
}

func (m *ctxCapturingModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "test"}, nil
}

func (m *ctxCapturingModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.receivedCtx = ctx
	m.mu.Unlock()
	close(m.ctxReceived)

	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "chunk"}, nil)
		// Block until context cancelled (cleanup will cancel)
		<-ctx.Done()
	}()
	return sr, nil
}

func (m *ctxCapturingModel) getReceivedCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.receivedCtx
}

// fakeSaver records SaveMessage calls without a real database.
type fakeSaver struct {
	mu         sync.Mutex
	saveCalled int
}

func (f *fakeSaver) SaveMessage(mess *schema.Message, convID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalled++
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
