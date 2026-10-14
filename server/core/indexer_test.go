package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/document"
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
