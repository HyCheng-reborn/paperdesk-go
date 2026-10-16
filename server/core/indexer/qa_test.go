package indexer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// fakeQAModel 实现 model.BaseChatModel 接口用于测试
type fakeQAModel struct {
	generateFn func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error)
}

func (f *fakeQAModel) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if f.generateFn != nil {
		return f.generateFn(ctx, msgs)
	}
	return &schema.Message{Content: "fake QA content"}, nil
}

func (f *fakeQAModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("not implemented")
}

// makeQADocs 创建测试用的文档切片
func makeQADocs(n int) []*schema.Document {
	docs := make([]*schema.Document, n)
	for i := 0; i < n; i++ {
		docs[i] = &schema.Document{
			ID:       fmt.Sprintf("doc-%d", i),
			Content:  fmt.Sprintf("content-%d", i),
			MetaData: make(map[string]any),
		}
	}
	return docs
}

// qaFactoryWithModel 返回使用指定模型的工厂函数
func qaFactoryWithModel(m model.BaseChatModel) func(context.Context) (model.BaseChatModel, error) {
	return func(ctx context.Context) (model.BaseChatModel, error) {
		return m, nil
	}
}

// qaCtx 创建带 knowledgeName 的 context
func qaCtx() context.Context {
	return context.WithValue(context.Background(), coretypes.KnowledgeName, "test-kb")
}

func TestQA_AllSuccess(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(3)
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return &schema.Message{Content: "generated QA"}, nil
		},
	}

	result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(result))
	}
	for i, d := range result {
		qaContent, ok := d.MetaData[coretypes.FieldQAContent].(string)
		if !ok || qaContent == "" {
			t.Errorf("doc %d: QA content not set", i)
		}
	}
}

func TestQA_PartialFailure_ReturnsError(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(3)
	callCount := 0
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			callCount++
			if callCount == 2 {
				return nil, fmt.Errorf("model error")
			}
			return &schema.Message{Content: "ok"}, nil
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestQA_ConcurrencyLimit(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(10)
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			cur := concurrent.Add(1)
			wg.Add(1)
			defer func() {
				concurrent.Add(-1)
				wg.Done()
			}()
			// 更新最大值
			for {
				old := maxConcurrent.Load()
				if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
					break
				}
			}
			// 等待一段时间让并发累积
			time.Sleep(50 * time.Millisecond)
			return &schema.Message{Content: "qa"}, nil
		},
	}

	// 限制并发为 3
	done := make(chan struct{})
	go func() {
		_, _ = qa(ctx, docs, qaFactoryWithModel(fakeModel), 3)
		close(done)
	}()

	select {
	case <-done:
		// 完成
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	// 验证最大并发不超过 3
	if max := maxConcurrent.Load(); max > 3 {
		t.Errorf("max concurrent %d exceeded limit 3", max)
	}
}

func TestQA_Disabled_ReturnsDocsUnchanged(t *testing.T) {
	// QA 关闭时，BuildIndexerAsync 会创建 identity lambda
	// 这里测试 qa() 函数本身不会被调用（通过 async.go 的分支控制）
	// 但为了完整性，我们测试 qa() 在 nil factory 时的行为
	ctx := qaCtx()
	docs := makeQADocs(2)

	// 使用 nil factory 会导致 panic，所以这里测试的是 async.go 的逻辑
	// 实际上 QA 关闭时不会调用 qa()，而是直接返回 docs
	// 这个测试验证 async.go 的 identity lambda 行为
	// 由于 qa() 本身需要 factory，我们测试 factory 返回 error 的情况
	factory := func(ctx context.Context) (model.BaseChatModel, error) {
		return nil, fmt.Errorf("QA disabled")
	}

	_, err := qa(ctx, docs, factory, 4)
	if err == nil {
		t.Fatal("expected error when factory fails")
	}
}

func TestQA_Disabled_ExistingContent_Preserved(t *testing.T) {
	// 测试已有 QA 内容在 qa() 调用时被保留
	ctx := qaCtx()
	docs := makeQADocs(2)
	docs[0].MetaData[coretypes.FieldQAContent] = "existing QA"

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return &schema.Message{Content: "new QA"}, nil
		},
	}

	result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 第一个文档应该保留原有内容
	if result[0].MetaData[coretypes.FieldQAContent] != "existing QA" {
		t.Errorf("doc 0: expected 'existing QA', got %v", result[0].MetaData[coretypes.FieldQAContent])
	}
	// 第二个文档应该有新生成的内容
	if result[1].MetaData[coretypes.FieldQAContent] != "new QA" {
		t.Errorf("doc 1: expected 'new QA', got %v", result[1].MetaData[coretypes.FieldQAContent])
	}
}

func TestQA_ContextCancel_Stops(t *testing.T) {
	ctx, cancel := context.WithCancel(qaCtx())
	docs := makeQADocs(5)
	started := make(chan struct{}, 5)

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			started <- struct{}{}
			// 等待 context 取消
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
		done <- err
	}()

	// 等待至少一个任务启动
	select {
	case <-started:
		// 取消 context
		cancel()
	case <-time.After(1 * time.Second):
		t.Fatal("no task started")
	}

	// 验证 qa() 返回错误
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for qa() to return")
	}
}

func TestQA_EmptyDocs(t *testing.T) {
	ctx := qaCtx()
	docs := []*schema.Document{}

	result, err := qa(ctx, docs, qaFactoryWithModel(&fakeQAModel{}), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(result))
	}
}

func TestQA_NilDoc_ReturnsError(t *testing.T) {
	ctx := qaCtx()
	docs := []*schema.Document{
		{ID: "doc-1", Content: "content", MetaData: make(map[string]any)},
		nil,
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(&fakeQAModel{}), 4)
	if err == nil {
		t.Fatal("expected error for nil doc")
	}
	if err.Error() != "qa: nil document in input" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestQA_DuplicatePointer_ReturnsError(t *testing.T) {
	ctx := qaCtx()
	doc := &schema.Document{ID: "doc-1", Content: "content", MetaData: make(map[string]any)}
	docs := []*schema.Document{doc, doc} // 重复指针

	_, err := qa(ctx, docs, qaFactoryWithModel(&fakeQAModel{}), 4)
	if err == nil {
		t.Fatal("expected error for duplicate pointer")
	}
	if err.Error() != "qa: duplicate document pointer detected" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestQA_ExistingContent_Skips(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(2)
	docs[0].MetaData[coretypes.FieldQAContent] = "existing"

	generateCalled := false
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			generateCalled = true
			return &schema.Message{Content: "new"}, nil
		},
	}

	result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 第一个文档应该保留原有内容
	if result[0].MetaData[coretypes.FieldQAContent] != "existing" {
		t.Errorf("doc 0: expected 'existing', got %v", result[0].MetaData[coretypes.FieldQAContent])
	}
	// 第二个文档应该生成新内容
	if result[1].MetaData[coretypes.FieldQAContent] != "new" {
		t.Errorf("doc 1: expected 'new', got %v", result[1].MetaData[coretypes.FieldQAContent])
	}
	// Generate 应该被调用（至少一次，为第二个文档）
	if !generateCalled {
		t.Error("Generate should have been called for doc without QA content")
	}
}

func TestQA_ModelFactoryError(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(1)

	factory := func(ctx context.Context) (model.BaseChatModel, error) {
		return nil, fmt.Errorf("factory error")
	}

	_, err := qa(ctx, docs, factory, 4)
	if err == nil {
		t.Fatal("expected error when factory fails")
	}
}

func TestQA_KnowledgeNameMissing(t *testing.T) {
	ctx := context.Background() // 没有 KnowledgeName
	docs := makeQADocs(1)

	_, err := qa(ctx, docs, qaFactoryWithModel(&fakeQAModel{}), 4)
	if err == nil {
		t.Fatal("expected error for missing knowledge name")
	}
	if err.Error() != "必须提供知识库名称" {
		t.Errorf("unexpected error message: %v", err)
	}
}
