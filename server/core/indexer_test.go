package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

// --- 测试用 fake ---

type fakeSyncIndexer struct {
	ids      []string
	err      error
	onInvoke func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error)
}

func (f *fakeSyncIndexer) invoke(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
	if f.onInvoke != nil {
		return f.onInvoke(ctx, knowledgeName, source)
	}
	if f.err != nil {
		return nil, f.err
	}
	result := make([]string, len(f.ids))
	copy(result, f.ids)
	return result, nil
}

// asyncCall 记录后台收到的完整参数快照
type asyncCall struct {
	knowledgeName string
	documentsID   int64
	docsIDs       []string
}

type fakeAsyncIndexer struct {
	mu        sync.Mutex
	onIndex   func(knowledgeName string, documentsID int64, docsIDs []string) error
	callCount int
}

func (f *fakeAsyncIndexer) indexAsyncByDocsID(knowledgeName string, documentsID int64, docsIDs []string) error {
	f.mu.Lock()
	f.callCount++
	f.mu.Unlock()
	if f.onIndex != nil {
		return f.onIndex(knowledgeName, documentsID, docsIDs)
	}
	return nil
}

func (f *fakeAsyncIndexer) getCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func newIndexerTestRag(syncIdx syncIndexer, asyncIdx asyncIndexer) *Rag {
	return &Rag{
		syncIndexer:  syncIdx,
		asyncIndexer: asyncIdx,
	}
}

// closeOnce 幂等关闭无缓冲信号 channel，用于 cleanup 释放阻塞的后台任务
func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// --- 行为测试（默认运行，无 build tag）---

func TestIndex_SyncFailure_NoAsync(t *testing.T) {
	// 同步失败不启动后台任务
	fakeAsync := &fakeAsyncIndexer{}
	r := newIndexerTestRag(&fakeSyncIndexer{err: errors.New("sync error")}, fakeAsync)

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if fakeAsync.getCallCount() != 0 {
		t.Fatal("async should not be called on sync failure")
	}
}

func TestIndex_SyncSuccess_AsyncStillBlocked(t *testing.T) {
	// 同步成功时 Index 已返回，后台仍阻塞在 fake 内
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			close(asyncStarted)
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {} // 注入立即返回，避免真实等待一秒

	t.Cleanup(func() {
		closeOnce(asyncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}

	// Index 已返回，后台进入并阻塞
	waitSig(t, asyncStarted, "async start")

	// 未释放前后台不得完成
	select {
	case <-asyncDone:
		t.Fatal("background finished before release")
	case <-time.After(100 * time.Millisecond):
	}

	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")
}

func TestIndex_AsyncFailure_SyncReturnUnchanged(t *testing.T) {
	// 必须等待后台 fake 确实返回失败后，再断言同步返回的 ids/error 不变
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return errors.New("async error")
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})

	// 等待后台确实运行并返回失败
	waitSig(t, asyncDone, "background failure return")

	// 再断言同步结果不受后台影响
	if err != nil {
		t.Fatalf("sync err should be nil, got: %v", err)
	}
	if len(ids) != 1 || ids[0] != "id1" {
		t.Fatalf("sync ids changed: %v", ids)
	}
	if got := fakeAsync.getCallCount(); got != 1 {
		t.Fatalf("expected background to run exactly once, got %d", got)
	}
}

func TestIndex_ModifyReqAfterReturn_AsyncGetsSnapshot(t *testing.T) {
	// Index 返回后同时修改 KnowledgeName 与 DocumentsId，后台仍应收到原始标量快照
	got := make(chan asyncCall, 1)
	release := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			got <- asyncCall{
				knowledgeName: knowledgeName,
				documentsID:   documentsID,
				docsIDs:       append([]string(nil), docsIDs...),
			}
			<-release
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {}

	t.Cleanup(func() {
		closeOnce(release)
		waitSig(t, asyncDone, "background finish")
	})

	req := &IndexReq{URI: "test.pdf", KnowledgeName: "original", DocumentsId: 42}
	ids, err := r.Index(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}

	// 返回后修改调用方 req 的两个标量字段
	req.KnowledgeName = "modified"
	req.DocumentsId = 999

	var call asyncCall
	select {
	case call = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for background params")
	}

	if call.knowledgeName != "original" {
		t.Fatalf("async should receive original KnowledgeName, got %q", call.knowledgeName)
	}
	if call.documentsID != 42 {
		t.Fatalf("async should receive original DocumentsId 42, got %d", call.documentsID)
	}
}

func TestIndex_ModifyReturnedIDs_AsyncDocsIDsUnchanged(t *testing.T) {
	// 修改返回 ids 元素后，后台 DocsIDs 仍为原始快照
	got := make(chan asyncCall, 1)
	release := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			got <- asyncCall{
				knowledgeName: knowledgeName,
				documentsID:   documentsID,
				docsIDs:       append([]string(nil), docsIDs...),
			}
			<-release
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1", "id2"}}, fakeAsync)
	r.asyncWait = func() {}

	t.Cleanup(func() {
		closeOnce(release)
		waitSig(t, asyncDone, "background finish")
	})

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 修改调用方持有的返回切片
	ids[0] = "modified"

	var call asyncCall
	select {
	case call = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for background params")
	}
	if call.docsIDs[0] != "id1" {
		t.Fatalf("async DocsIDs should be original snapshot, got %q", call.docsIDs[0])
	}
}

func TestIndex_AsyncModifiesOwnDocsIDs_CallerIDsUnchanged(t *testing.T) {
	// 后台修改自己的 DocsIDs 副本后，调用方返回 ids 不变
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			if len(docsIDs) > 0 {
				docsIDs[0] = "modified"
			}
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitSig(t, asyncDone, "background finish")

	if ids[0] != "id1" {
		t.Fatalf("caller ids should be unchanged, got %q", ids[0])
	}
}

func TestIndex_AsyncWaitInvoked_BeforeBackground(t *testing.T) {
	// 验证等待使用注入函数，且发生在后台执行之前（替代固定 sleep）
	waitEntered := make(chan struct{})
	waitRelease := make(chan struct{})
	asyncStarted := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			close(asyncStarted)
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {
		close(waitEntered)
		<-waitRelease
	}

	t.Cleanup(func() {
		closeOnce(waitRelease)
		waitSig(t, asyncDone, "background finish")
	})

	if _, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 注入等待被调用
	waitSig(t, waitEntered, "injected wait entered")

	// 等待释放前，后台 indexAsyncByDocsID 不得执行
	select {
	case <-asyncStarted:
		t.Fatal("background ran before wait released")
	case <-time.After(100 * time.Millisecond):
	}

	close(waitRelease)
	waitSig(t, asyncStarted, "background started after wait")
	waitSig(t, asyncDone, "background finish")
}

func TestIndex_BackgroundPanic_Recovered(t *testing.T) {
	// recover 覆盖等待阶段：asyncWait 内 panic 不应影响 Index 返回，也不应使进程崩溃
	waitEntered := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			t.Error("onIndex should not run when wait panics")
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func() {
		defer close(waitEntered)
		panic("boom in wait")
	}

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("Index should not fail due to background panic: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// asyncWait 的 defer 在 panic 展开时触发，随后被 goroutine 顶部的 recover 捕获
	waitSig(t, waitEntered, "wait panic unwound to recover")
}

func TestIndex_ConcurrentCalls_Isolation(t *testing.T) {
	// 同一个 Rag 并发不同 URI/KnowledgeName/DocumentsId，逐项验证调用方与后台一一对应、无串用
	const n = 3

	fakeSync := &fakeSyncIndexer{
		onInvoke: func(_ context.Context, knowledgeName string, source document.Source) ([]string, error) {
			// 根据输入返回不同 IDs，保证每个调用结果可区分
			return []string{"chunk-" + knowledgeName, "chunk-" + source.URI}, nil
		},
	}

	bgCh := make(chan asyncCall, n*4)
	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName string, documentsID int64, docsIDs []string) error {
			bgCh <- asyncCall{
				knowledgeName: knowledgeName,
				documentsID:   documentsID,
				docsIDs:       append([]string(nil), docsIDs...),
			}
			return nil
		},
	}

	r := newIndexerTestRag(fakeSync, fakeAsync)
	r.asyncWait = func() {}

	callerIDs := make([][]string, n)
	callerErr := make([]error, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := &IndexReq{
				URI:           fmt.Sprintf("doc%d.pdf", idx),
				KnowledgeName: fmt.Sprintf("kb%d", idx),
				DocumentsId:   int64(idx),
			}
			ids, err := r.Index(context.Background(), req)
			callerIDs[idx] = ids
			callerErr[idx] = err
		}(i)
	}
	wg.Wait()

	// 用完成信号（channel）收集 n 条后台记录，不用固定 sleep
	seen := make(map[string]asyncCall, n)
	for i := 0; i < n; i++ {
		select {
		case c := <-bgCh:
			if _, dup := seen[c.knowledgeName]; dup {
				t.Errorf("duplicate background record for %s", c.knowledgeName)
			}
			seen[c.knowledgeName] = c
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for background record %d", i)
		}
	}

	// 每个任务恰好一次：读取 n 条后不应再有额外记录
drain:
	for {
		select {
		case c := <-bgCh:
			t.Errorf("unexpected extra background record: %s", c.knowledgeName)
		default:
			break drain
		}
	}

	if len(seen) != n {
		t.Fatalf("expected %d distinct background records, got %d", n, len(seen))
	}

	for i := 0; i < n; i++ {
		kn := fmt.Sprintf("kb%d", i)
		wantIDs := []string{"chunk-" + kn, fmt.Sprintf("chunk-doc%d.pdf", i)}

		if callerErr[i] != nil {
			t.Errorf("call %d: unexpected error %v", i, callerErr[i])
		}
		if !reflect.DeepEqual(callerIDs[i], wantIDs) {
			t.Errorf("call %d: caller ids = %v, want %v", i, callerIDs[i], wantIDs)
		}

		c, ok := seen[kn]
		if !ok {
			t.Fatalf("no background record for %s", kn)
		}
		if c.documentsID != int64(i) {
			t.Errorf("cross-task on %s: documentsID=%d want %d", kn, c.documentsID, i)
		}
		if !reflect.DeepEqual(c.docsIDs, wantIDs) {
			t.Errorf("cross-task on %s: background docsIDs=%v want %v", kn, c.docsIDs, wantIDs)
		}
	}
}

// --- 编排测试 fake 类型（indexAsyncByDocsID 直接调用）---

// fakeDocSearcher 实现 docSearcher 接口
type fakeDocSearcher struct {
	result []*schema.Document
	err    error
	called bool
}

func (f *fakeDocSearcher) SearchDocumentsByIDs(_ context.Context, _ string, _ []string, _ int) ([]*schema.Document, error) {
	f.called = true
	return f.result, f.err
}

// fakeAsyncRunnable 实现 compose.Runnable[[]*schema.Document, []string]，仅 Invoke 被调用
type fakeAsyncRunnable struct {
	ids    []string
	err    error
	called bool
}

func (f *fakeAsyncRunnable) Invoke(_ context.Context, _ []*schema.Document, _ ...compose.Option) ([]string, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.ids, nil
}

func (f *fakeAsyncRunnable) Stream(_ context.Context, _ []*schema.Document, _ ...compose.Option) (*schema.StreamReader[[]string], error) {
	panic("not implemented in test")
}

func (f *fakeAsyncRunnable) Collect(_ context.Context, _ *schema.StreamReader[[]*schema.Document], _ ...compose.Option) ([]string, error) {
	panic("not implemented in test")
}

func (f *fakeAsyncRunnable) Transform(_ context.Context, _ *schema.StreamReader[[]*schema.Document], _ ...compose.Option) (*schema.StreamReader[[]string], error) {
	panic("not implemented in test")
}

// newOrchestrationTestRag 构建仅用于 indexAsyncByDocsID 编排测试的 Rag
func newOrchestrationTestRag(
	searcher docSearcher,
	saveFn func(ctx context.Context, docID int64, chunks []entity.KnowledgeChunks) error,
	updateFn func(ctx context.Context, docID int64, status int) error,
	asyncRunnable compose.Runnable[[]*schema.Document, []string],
) *Rag {
	return &Rag{
		docSearch:       searcher,
		saveChunksFn:    saveFn,
		updateDocStatus: updateFn,
		idxerAsync:      asyncRunnable,
	}
}

var errSentinel = fmt.Errorf("sentinel test error")

func TestIndexAsyncByDocsID_Over1000Docs_Error(t *testing.T) {
	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}

	searcher := &fakeDocSearcher{}
	saveCalled := false
	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { return nil },
		&fakeAsyncRunnable{},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: ids, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected over-limit error")
	}
	if searcher.called {
		t.Fatal("docSearch should not be called when over limit")
	}
	if saveCalled {
		t.Fatal("saveChunksFn should not be called when over limit")
	}
}

func TestIndexAsyncByDocsID_MarshalError_NoActive(t *testing.T) {
	docs := []*schema.Document{
		{ID: "doc1", Content: "ok", MetaData: map[string]any{"bad": make(chan int)}},
	}
	searcher := &fakeDocSearcher{result: docs}

	saveCalled := false
	updateCalled := false
	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		&fakeAsyncRunnable{ids: []string{"id1"}},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"doc1"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected marshal error")
	}
	if !strings.Contains(err.Error(), "doc1") {
		t.Fatalf("error should mention doc ID, got: %v", err)
	}
	if saveCalled {
		t.Fatal("saveChunksFn should not be called on marshal error")
	}
	if updateCalled {
		t.Fatal("updateDocStatus should not be called on marshal error")
	}
}

func TestIndexAsyncByDocsID_MissingDocs_ReturnsError(t *testing.T) {
	// docSearch 返回 ["a","c"]，但期望 ["a","b"]，try=0 不重试直接报错
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
		{ID: "c", Content: "cc", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return nil },
		func(_ context.Context, _ int64, _ int) error { return nil },
		&fakeAsyncRunnable{},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a", "b"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected missing docs error")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Fatalf("error should mention missing ID 'b', got: %v", err)
	}
}

func TestIndexAsyncByDocsID_RetryExhausted_Missing(t *testing.T) {
	// try=1，两次都返回部分缺失
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return nil },
		func(_ context.Context, _ int64, _ int) error { return nil },
		&fakeAsyncRunnable{},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a", "b"}, KnowledgeName: "kb", DocumentsId: 1, try: 1,
	})
	if err == nil {
		t.Fatal("expected error after retry exhausted")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Fatalf("error should mention missing ID 'b', got: %v", err)
	}
}

func TestIndexAsyncByDocsID_SaveChunksError_Stops(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	updateCalled := false
	asyncRunnable := &fakeAsyncRunnable{ids: []string{"id1"}}
	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return errSentinel },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		asyncRunnable,
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"d1"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	if asyncRunnable.called {
		t.Fatal("idxerAsync should not be called when saveChunks fails")
	}
	if updateCalled {
		t.Fatal("updateDocStatus should not be called Active when saveChunks fails")
	}
}

func TestIndexAsyncByDocsID_IndexAsyncError_NoActive(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	saveCalled := false
	updateCalled := false
	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		&fakeAsyncRunnable{err: errSentinel},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"d1"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	if !saveCalled {
		t.Fatal("saveChunksFn should have been called")
	}
	if updateCalled {
		t.Fatal("updateDocStatus should not be called Active when IndexAsync fails")
	}
}

func TestIndexAsyncByDocsID_ActiveUpdateError(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return nil },
		func(_ context.Context, _ int64, _ int) error { return errSentinel },
		&fakeAsyncRunnable{ids: []string{"id1"}},
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"d1"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error from updateDocStatus, got: %v", err)
	}
}

func TestIndexAsyncByDocsID_FullSuccess(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
		{ID: "d2", Content: "c2", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	saveCalled := false
	var savedDocID int64
	var savedChunks []entity.KnowledgeChunks
	var updateStatus int
	var updateDocID int64

	asyncRunnable := &fakeAsyncRunnable{ids: []string{"idx1", "idx2"}}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, docID int64, chunks []entity.KnowledgeChunks) error {
			saveCalled = true
			savedDocID = docID
			savedChunks = chunks
			return nil
		},
		func(_ context.Context, docID int64, status int) error {
			updateDocID = docID
			updateStatus = status
			return nil
		},
		asyncRunnable,
	)

	ids, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"d1", "d2"}, KnowledgeName: "kb", DocumentsId: 42, try: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !searcher.called {
		t.Fatal("docSearch should have been called")
	}
	if !saveCalled {
		t.Fatal("saveChunksFn should have been called")
	}
	if savedDocID != 42 {
		t.Fatalf("saveChunksFn docID = %d, want 42", savedDocID)
	}
	if len(savedChunks) != 2 {
		t.Fatalf("savedChunks len = %d, want 2", len(savedChunks))
	}
	if !asyncRunnable.called {
		t.Fatal("idxerAsync should have been called")
	}
	if !reflect.DeepEqual(ids, []string{"idx1", "idx2"}) {
		t.Fatalf("ids = %v, want [idx1 idx2]", ids)
	}
	if updateDocID != 42 {
		t.Fatalf("updateDocStatus docID = %d, want 42", updateDocID)
	}
	if updateStatus != int(v1.StatusActive) {
		t.Fatalf("updateDocStatus status = %d, want %d", updateStatus, int(v1.StatusActive))
	}
}
