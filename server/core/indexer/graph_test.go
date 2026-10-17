package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// buildTestGraph 构建测试用 Graph，注入 fake QA factory 和 fake indexer
// 模拟 BuildIndexerAsync 的 Graph 装配逻辑，但不依赖真实 ES/Qdrant
func buildTestGraph(
	qaFactory func(context.Context) (model.BaseChatModel, error),
	qaEnabled bool,
	maxConcurrent int,
	indexerFn func(ctx context.Context, docs []*schema.Document) ([]string, error),
) (compose.Runnable[[]*schema.Document, []string], error) {
	const (
		IndexerNode = "Indexer"
		QANode      = "QA"
	)

	g := compose.NewGraph[[]*schema.Document, []string]()

	// Indexer 节点：使用 fake
	_ = g.AddLambdaNode(IndexerNode, compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]string, error) {
		if indexerFn != nil {
			return indexerFn(ctx, docs)
		}
		ids := make([]string, len(docs))
		for i, d := range docs {
			ids[i] = d.ID
		}
		return ids, nil
	}))

	// QA 节点
	if qaEnabled && qaFactory != nil {
		factory := qaFactory
		mc := maxConcurrent
		if mc <= 0 {
			mc = 4
		}
		_ = g.AddLambdaNode(QANode, compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return qa(ctx, docs, factory, mc)
		}))
	} else {
		// QA 关闭：identity lambda
		_ = g.AddLambdaNode(QANode, compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return docs, nil
		}))
	}

	_ = g.AddEdge(compose.START, QANode)
	_ = g.AddEdge(QANode, IndexerNode)
	_ = g.AddEdge(IndexerNode, compose.END)

	r, err := g.Compile(context.Background(), compose.WithGraphName("test_indexer_async"))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// TestGraph_QAError_StopsIndexer QA 返回 sentinel → 下游 Indexer 不执行 → errors.Is 识别原始错误
func TestGraph_QAError_StopsIndexer(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")
	sentinel := errors.New("qa sentinel error")

	var indexerCalled atomic.Bool

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return nil, sentinel
		},
	}

	graph, err := buildTestGraph(
		func(ctx context.Context) (model.BaseChatModel, error) {
			return fakeModel, nil
		},
		true, // qaEnabled
		4,    // maxConcurrent
		func(ctx context.Context, docs []*schema.Document) ([]string, error) {
			indexerCalled.Store(true)
			ids := make([]string, len(docs))
			for i, d := range docs {
				ids[i] = d.ID
			}
			return ids, nil
		},
	)
	if err != nil {
		t.Fatalf("buildTestGraph failed: %v", err)
	}

	docs := []*schema.Document{
		{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)},
	}

	_, err = graph.Invoke(ctx, docs)
	if err == nil {
		t.Fatal("expected error from graph")
	}

	// 错误链应包含 sentinel
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error in chain, got: %v", err)
	}

	// Indexer 不应被调用
	if indexerCalled.Load() {
		t.Error("Indexer should not be called when QA fails")
	}
}

// TestGraph_QADisabled_FactoryNotCalled QA 关闭 → factory 调用次数为 0 → 下游正常接收文档
func TestGraph_QADisabled_FactoryNotCalled(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")

	var factoryCalled atomic.Bool
	var indexerReceived []*schema.Document

	graph, err := buildTestGraph(
		func(ctx context.Context) (model.BaseChatModel, error) {
			factoryCalled.Store(true)
			return &fakeQAModel{}, nil
		},
		false, // qaEnabled = false
		4,
		func(ctx context.Context, docs []*schema.Document) ([]string, error) {
			indexerReceived = docs
			ids := make([]string, len(docs))
			for i, d := range docs {
				ids[i] = d.ID
			}
			return ids, nil
		},
	)
	if err != nil {
		t.Fatalf("buildTestGraph failed: %v", err)
	}

	docs := []*schema.Document{
		{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)},
		{ID: "doc-2", Content: "content-2", MetaData: make(map[string]any)},
	}

	ids, err := graph.Invoke(ctx, docs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// factory 不应被调用
	if factoryCalled.Load() {
		t.Error("QA factory should not be called when QA is disabled")
	}

	// Indexer 应正常接收文档
	if len(indexerReceived) != 2 {
		t.Fatalf("expected 2 docs in indexer, got %d", len(indexerReceived))
	}

	// 返回 IDs 正确
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %d", len(ids))
	}
	if ids[0] != "doc-1" || ids[1] != "doc-2" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

// TestGraph_QAEnabled_Success QA 启用且成功，验证完整流程
func TestGraph_QAEnabled_Success(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")

	var indexerReceived []*schema.Document

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return &schema.Message{Content: "generated QA"}, nil
		},
	}

	graph, err := buildTestGraph(
		func(ctx context.Context) (model.BaseChatModel, error) {
			return fakeModel, nil
		},
		true, // qaEnabled
		4,
		func(ctx context.Context, docs []*schema.Document) ([]string, error) {
			indexerReceived = docs
			ids := make([]string, len(docs))
			for i, d := range docs {
				ids[i] = d.ID
			}
			return ids, nil
		},
	)
	if err != nil {
		t.Fatalf("buildTestGraph failed: %v", err)
	}

	docs := []*schema.Document{
		{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)},
	}

	ids, err := graph.Invoke(ctx, docs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Indexer 收到的文档应有 QA 内容
	if len(indexerReceived) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(indexerReceived))
	}
	qaContent, ok := indexerReceived[0].MetaData[coretypes.FieldQAContent].(string)
	if !ok || qaContent == "" {
		t.Error("expected QA content in doc received by indexer")
	}
	if qaContent != "generated QA" {
		t.Errorf("QA content = %q, want 'generated QA'", qaContent)
	}

	// IDs 正确
	if len(ids) != 1 || ids[0] != "doc-1" {
		t.Errorf("unexpected ids: %v", ids)
	}
}

// TestGraph_QAFactoryError QA factory 失败，错误传播
func TestGraph_QAFactoryError(t *testing.T) {
	ctx := context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")
	sentinel := fmt.Errorf("factory error")

	var indexerCalled bool

	graph, err := buildTestGraph(
		func(ctx context.Context) (model.BaseChatModel, error) {
			return nil, sentinel
		},
		true, // qaEnabled
		4,
		func(ctx context.Context, docs []*schema.Document) ([]string, error) {
			indexerCalled = true
			return nil, nil
		},
	)
	if err != nil {
		t.Fatalf("buildTestGraph failed: %v", err)
	}

	docs := []*schema.Document{
		{ID: "doc-1", Content: "content-1", MetaData: make(map[string]any)},
	}

	_, err = graph.Invoke(ctx, docs)
	if err == nil {
		t.Fatal("expected error")
	}

	// 错误链应包含 sentinel
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in error chain, got: %v", err)
	}

	if indexerCalled {
		t.Error("Indexer should not be called when factory fails")
	}
}
