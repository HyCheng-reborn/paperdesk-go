package rag

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
)

// TestChatStream_AnswerStreamError calls the ACTUAL ChatStream with minimal,
// instance-level, read-only-during-runtime injected dependencies so retrieval
// returns offline documents and stream creation returns a sentinel error.
// It asserts:
//   - a non-nil error, recognizable via errors.Is as the build-stream sentinel;
//   - no (empty) success response is returned;
//   - SSE output is never entered.
//
// No real service is connected, controller logic is not copied into the test,
// and no mutable global function is swapped.
func TestChatStream_AnswerStreamError(t *testing.T) {
	sentinel := errors.New("stream create sentinel")

	var retrieveCalled, answerCalled, sseCalled bool
	var gotDocs []*schema.Document

	c := &ControllerV1{
		chatRetrieve: func(ctx context.Context, req *v1.RetrieverReq) (*v1.RetrieverRes, error) {
			retrieveCalled = true
			return &v1.RetrieverRes{Document: []*schema.Document{{Content: "offline-doc"}}}, nil
		},
		chatAnswerStream: func(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], error) {
			answerCalled = true
			gotDocs = docs
			return nil, sentinel
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
	// Offline retrieval result must be threaded into the stream-creation call.
	if len(gotDocs) != 1 || gotDocs[0].Content != "offline-doc" {
		t.Errorf("expected offline docs passed to answer stream, got: %+v", gotDocs)
	}
	// SSE output must not be entered when stream creation fails.
	if sseCalled {
		t.Fatal("ChatStream must not enter SSE output when stream creation fails")
	}
}
