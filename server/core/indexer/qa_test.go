package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	var callCount atomic.Int32
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			n := callCount.Add(1)
			if n == 2 {
				return nil, fmt.Errorf("model error")
			}
			return &schema.Message{Content: "ok"}, nil
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 1) // 串行确保第2个失败
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestQA_ConcurrencyLimit(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(10)
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			cur := concurrent.Add(1)
			defer concurrent.Add(-1)
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
	ctx := qaCtx()
	docs := makeQADocs(2)

	factory := func(ctx context.Context) (model.BaseChatModel, error) {
		return nil, fmt.Errorf("QA disabled")
	}

	_, err := qa(ctx, docs, factory, 4)
	if err == nil {
		t.Fatal("expected error when factory fails")
	}
}

func TestQA_Disabled_ExistingContent_Preserved(t *testing.T) {
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

// --- 1D-2 新增测试 ---

// TestQA_PreCancelled 传入已取消 ctx，验证快速返回 error，不调用模型
func TestQA_PreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(qaCtx())
	cancel() // 立即取消

	docs := makeQADocs(3)
	modelCalled := false
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			modelCalled = true
			return &schema.Message{Content: "qa"}, nil
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err == nil {
		t.Fatal("expected error for pre-cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if modelCalled {
		t.Error("model should not be called for pre-cancelled ctx")
	}
}

// TestQA_MidCancel 进行中取消，验证返回 error，不发布部分结果
func TestQA_MidCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(qaCtx())
	docs := makeQADocs(5)
	firstStarted := make(chan struct{})

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			select {
			case firstStarted <- struct{}{}:
			default:
			}
			// 等待取消
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() {
		result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 1)
		if result != nil {
			// 不应该发布部分结果
			for i, d := range result {
				if _, ok := d.MetaData[coretypes.FieldQAContent]; ok {
					t.Errorf("doc %d: QA content should not be set on mid-cancel", i)
				}
			}
		}
		done <- err
	}()

	select {
	case <-firstStarted:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("no task started")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after mid-cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for qa() to return")
	}
}

// TestQA_Timeout 超时场景，验证返回 error
func TestQA_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(qaCtx(), 50*time.Millisecond)
	defer cancel()

	docs := makeQADocs(3)
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

// TestQA_SharedMetaData_InputUnchanged_OutputIsolated 两个 Document 共享同一 MetaData map
func TestQA_SharedMetaData_InputUnchanged_OutputIsolated(t *testing.T) {
	ctx := qaCtx()
	sharedMeta := map[string]any{"key": "value"}
	docs := []*schema.Document{
		{ID: "doc-a", Content: "content-a", MetaData: sharedMeta},
		{ID: "doc-b", Content: "content-b", MetaData: sharedMeta},
	}

	// 按 doc.Content 决定返回结果，避免并发计数器竞争
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			// 从 user message 提取 doc content
			userMsg := msgs[len(msgs)-1]
			return &schema.Message{Content: fmt.Sprintf("qa-for-%s", userMsg.Content)}, nil
		},
	}

	result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 输入不变
	if _, ok := sharedMeta[coretypes.FieldQAContent]; ok {
		t.Error("input shared MetaData should not be modified")
	}

	// 输出独立
	if len(result) != 2 {
		t.Fatalf("expected 2 output docs, got %d", len(result))
	}
	qaA := result[0].MetaData[coretypes.FieldQAContent].(string)
	qaB := result[1].MetaData[coretypes.FieldQAContent].(string)
	if qaA == qaB {
		t.Error("output docs should have different QA content")
	}
	// 精确断言各文档内容
	if qaA != "qa-for-content-a" {
		t.Errorf("doc 0: expected 'qa-for-content-a', got %q", qaA)
	}
	if qaB != "qa-for-content-b" {
		t.Errorf("doc 1: expected 'qa-for-content-b', got %q", qaB)
	}
	// 输出 MetaData 是独立的 map
	if fmt.Sprintf("%p", result[0].MetaData) == fmt.Sprintf("%p", result[1].MetaData) {
		t.Error("output docs should have independent MetaData maps")
	}
}

// TestQA_ConcurrentCalls_Isolated 同一组件对同一组只读输入并发调用，结果隔离
func TestQA_ConcurrentCalls_Isolated(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(3)

	var callCount atomic.Int32
	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			n := callCount.Add(1)
			return &schema.Message{Content: fmt.Sprintf("qa-%d", n)}, nil
		},
	}

	const numCalls = 3
	results := make([][]*schema.Document, numCalls)
	errs := make([]error, numCalls)
	var wg sync.WaitGroup

	for i := 0; i < numCalls; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, e := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
			results[idx] = r
			errs[idx] = e
		}(i)
	}
	wg.Wait()

	for i := 0; i < numCalls; i++ {
		if errs[i] != nil {
			t.Errorf("call %d: unexpected error: %v", i, errs[i])
		}
		if len(results[i]) != 3 {
			t.Errorf("call %d: expected 3 docs, got %d", i, len(results[i]))
		}
	}

	// 输入不变
	for i, d := range docs {
		if _, ok := d.MetaData[coretypes.FieldQAContent]; ok {
			t.Errorf("input doc %d should not be modified", i)
		}
	}
}

// TestQA_WaitDrains_Success 正常路径等待已启动任务退出
func TestQA_WaitDrains_Success(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(4)
	var completed atomic.Int32

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
			return &schema.Message{Content: "qa"}, nil
		},
	}

	result, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 所有任务应完成
	if c := completed.Load(); c != 4 {
		t.Errorf("expected 4 completed tasks, got %d", c)
	}
	if len(result) != 4 {
		t.Fatalf("expected 4 result docs, got %d", len(result))
	}
}

// TestQA_WaitDrains_Failure 失败路径等待已启动任务退出
func TestQA_WaitDrains_Failure(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(3)
	var started atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			n := started.Add(1)
			if n == 1 {
				// 第一个任务：通知已启动，然后等待释放后失败
				close(firstStarted)
				<-releaseFirst
				return nil, fmt.Errorf("sentinel failure")
			}
			// 其他任务：等待 context 取消
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() {
		_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
		done <- err
	}()

	// 等待第一个 worker 启动
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not start")
	}

	// 释放第一个 worker，让它失败
	close(releaseFirst)

	// 验证 qa() 返回错误
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
		// 验证错误包含 sentinel failure
		if !strings.Contains(err.Error(), "sentinel failure") {
			t.Errorf("expected sentinel failure, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for qa() to return")
	}
}

// TestQA_NilModelFromFactory 工厂返回 nil model，验证返回错误
func TestQA_NilModelFromFactory(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(1)

	factory := func(ctx context.Context) (model.BaseChatModel, error) {
		return nil, nil // nil model, nil error
	}

	_, err := qa(ctx, docs, factory, 4)
	if err == nil {
		t.Fatal("expected error for nil model from factory")
	}
	if err.Error() != "qa: nil model from factory" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestQA_OutputDoesNotModifyInput 验证输出不修改输入文档
func TestQA_OutputDoesNotModifyInput(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(2)
	// 记录输入原始状态
	origMeta0 := make(map[string]any)
	origMeta1 := make(map[string]any)
	for k, v := range docs[0].MetaData {
		origMeta0[k] = v
	}
	for k, v := range docs[1].MetaData {
		origMeta1[k] = v
	}

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return &schema.Message{Content: "new QA"}, nil
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 输入 MetaData 不应被修改
	if len(docs[0].MetaData) != len(origMeta0) {
		t.Error("input doc 0 MetaData was modified")
	}
	if len(docs[1].MetaData) != len(origMeta1) {
		t.Error("input doc 1 MetaData was modified")
	}
	if _, ok := docs[0].MetaData[coretypes.FieldQAContent]; ok {
		t.Error("input doc 0 should not have QA content")
	}
	if _, ok := docs[1].MetaData[coretypes.FieldQAContent]; ok {
		t.Error("input doc 1 should not have QA content")
	}
}

// TestQA_NilFactory 传入 nil factory，验证返回错误
func TestQA_NilFactory(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(1)

	_, err := qa(ctx, docs, nil, 4)
	if err == nil {
		t.Fatal("expected error for nil factory")
	}
	if err.Error() != "qa: nil model factory" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestQA_GenerateReturnsNil 模型 Generate 返回 (nil, nil) 时返回明确错误
func TestQA_GenerateReturnsNil(t *testing.T) {
	ctx := qaCtx()
	docs := makeQADocs(1)

	fakeModel := &fakeQAModel{
		generateFn: func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
			return nil, nil
		},
	}

	_, err := qa(ctx, docs, qaFactoryWithModel(fakeModel), 4)
	if err == nil {
		t.Fatal("expected error when Generate returns nil")
	}
	if !strings.Contains(err.Error(), "nil response") {
		t.Errorf("expected nil response error, got: %v", err)
	}
}
