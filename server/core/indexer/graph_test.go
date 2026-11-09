package indexer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// fakeIndexer 实现实际 indexer.Indexer 接口，注入到共享 buildAsyncGraph helper。
// 记录是否被调用及收到的文档，用于验证 QA 前置阶段的短路/放行行为。
type fakeIndexer struct {
	fn       func(ctx context.Context, docs []*schema.Document) ([]string, error)
	called   atomic.Bool
	received []*schema.Document
}

func (f *fakeIndexer) Store(ctx context.Context, docs []*schema.Document, _ ...indexer.Option) ([]string, error) {
	f.called.Store(true)
	f.received = docs
	if f.fn != nil {
		return f.fn(ctx, docs)
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return ids, nil
}

// TestGraph_QAError_StopsIndexer：QA Generate 返回 sentinel → 下游 Indexer 不执行 → errors.Is 可识别。
func TestGraph_QAError_StopsIndexer(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")
	sentinel := errors.New("qa sentinel error")

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return nil, sentinel
		},
	}
	idx := &fakeIndexer{}

	graph, err := buildAsyncGraph(ctx, idx, true, 4,
		func(ctx context.Context) (model.BaseChatModel, error) { return fakeModel, nil })
	if err != nil {
		t.Fatalf("buildAsyncGraph failed: %v", err)
	}

	docs := []*schema.Document{{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)}}
	_, err = graph.Invoke(ctx, docs)
	if err == nil {
		t.Fatal("expected error from graph")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in error chain, got: %v", err)
	}
	if idx.called.Load() {
		t.Error("Indexer must not run when QA fails")
	}
}

// TestGraph_QAFactoryError：factory 返回 sentinel → 下游不执行。
func TestGraph_QAFactoryError(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")
	sentinel := errors.New("factory error")

	idx := &fakeIndexer{}
	graph, err := buildAsyncGraph(ctx, idx, true, 4,
		func(ctx context.Context) (model.BaseChatModel, error) { return nil, sentinel })
	if err != nil {
		t.Fatalf("buildAsyncGraph failed: %v", err)
	}

	docs := []*schema.Document{{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)}}
	_, err = graph.Invoke(ctx, docs)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in error chain, got: %v", err)
	}
	if idx.called.Load() {
		t.Error("Indexer must not run when factory fails")
	}
}

// TestGraph_QADisabled_FactoryNotCalled：QA 关闭 → factory 与 Generate 均不调用，Indexer 正常接收文档。
func TestGraph_QADisabled_FactoryNotCalled(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")

	var factoryCalled atomic.Bool
	idx := &fakeIndexer{}

	graph, err := buildAsyncGraph(ctx, idx, false /* qaEnabled=false */, 4,
		func(ctx context.Context) (model.BaseChatModel, error) {
			factoryCalled.Store(true)
			return &fakeQAModel{}, nil
		})
	if err != nil {
		t.Fatalf("buildAsyncGraph failed: %v", err)
	}

	docs := []*schema.Document{
		{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)},
		{ID: "doc-2", Content: "content-2", MetaData: make(map[string]any)},
	}
	ids, err := graph.Invoke(ctx, docs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if factoryCalled.Load() {
		t.Error("QA factory must not be called when QA disabled")
	}
	if !idx.called.Load() {
		t.Fatal("Indexer should run when QA disabled")
	}
	if len(idx.received) != 2 {
		t.Fatalf("Indexer received %d docs, want 2", len(idx.received))
	}
	if len(ids) != 2 || ids[0] != "doc-1" || ids[1] != "doc-2" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

// TestGraph_QAEnabled_Success：QA 成功 → 下游收到准确 QA 内容与正确 IDs。
func TestGraph_QAEnabled_Success(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return &schema.Message{Content: "generated QA"}, nil
		},
	}
	idx := &fakeIndexer{}

	graph, err := buildAsyncGraph(ctx, idx, true, 4,
		func(ctx context.Context) (model.BaseChatModel, error) { return fakeModel, nil })
	if err != nil {
		t.Fatalf("buildAsyncGraph failed: %v", err)
	}

	docs := []*schema.Document{{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)}}
	ids, err := graph.Invoke(ctx, docs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(idx.received) != 1 {
		t.Fatalf("Indexer received %d docs, want 1", len(idx.received))
	}
	qaContent, ok := idx.received[0].MetaData[coretypes.FieldQAContent].(string)
	if !ok || qaContent != "generated QA" {
		t.Errorf("Indexer doc QA content = %q (present=%v), want 'generated QA'", qaContent, ok)
	}
	if len(ids) != 1 || ids[0] != "doc-1" {
		t.Errorf("unexpected ids: %v", ids)
	}
}
