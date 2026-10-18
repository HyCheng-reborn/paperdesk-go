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
	onIndex   func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error
	callCount int
}

func (f *fakeAsyncIndexer) indexAsyncByDocsID(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
	f.mu.Lock()
	f.callCount++
	fn := f.onIndex
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, knowledgeName, documentsID, docsIDs)
	}
	return nil
}

func (f *fakeAsyncIndexer) getCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCount
}

func newIndexerTestRag(syncIdx syncIndexer, asyncIdx asyncIndexer) *Rag {
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())
	return &Rag{
		syncIndexer:   syncIdx,
		asyncIndexer:  asyncIdx,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 8),
		done:          make(chan struct{}),
		taskTimeout:   5 * time.Minute,
		maxBackground: 8,
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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			close(asyncStarted)
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {} // 注入立即返回，避免真实等待一秒

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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return errors.New("async error")
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
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
	r.asyncWait = func(ctx context.Context) {}

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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
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
	r.asyncWait = func(ctx context.Context) {}

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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			if len(docsIDs) > 0 {
				docsIDs[0] = "modified"
			}
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			close(asyncStarted)
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {
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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			t.Error("onIndex should not run when wait panics")
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {
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
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			bgCh <- asyncCall{
				knowledgeName: knowledgeName,
				documentsID:   documentsID,
				docsIDs:       append([]string(nil), docsIDs...),
			}
			return nil
		},
	}

	r := newIndexerTestRag(fakeSync, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

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

// --- A 组测试：准入、所有权转移、Shutdown ---

func TestIndex_AdmissionClosed_Rejects(t *testing.T) {
	// Shutdown 后 Index 返回 ErrShuttingDown
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// 先 Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Index 应被拒绝
	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("expected ErrShuttingDown, got: %v", err)
	}
}

func TestIndex_AdmissionFull_Rejects(t *testing.T) {
	// 后台容量满时返回 ErrBackgroundFull
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			<-asyncProceed
			return nil
		},
	}

	// 创建容量为 1 的 Rag
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())
	r := &Rag{
		syncIndexer:   &fakeSyncIndexer{ids: []string{"id1"}},
		asyncIndexer:  fakeAsync,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 1),
		done:          make(chan struct{}),
		taskTimeout:   5 * time.Minute,
		maxBackground: 1,
	}
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(asyncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	// 第一个 Index 成功，占用唯一 slot
	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("first Index failed: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 第二个 Index 应返回 ErrBackgroundFull
	_, err = r.Index(context.Background(), &IndexReq{URI: "test2.pdf", KnowledgeName: "kb2", DocumentsId: 2})
	if !errors.Is(err, ErrBackgroundFull) {
		t.Fatalf("expected ErrBackgroundFull, got: %v", err)
	}
}

func TestIndex_SyncFail_ReleasesOnce(t *testing.T) {
	// 同步失败时容量和登记只释放一次，不泄渚
	callCount := 0
	fakeSync := &fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			callCount++
			if callCount == 1 {
				return nil, errors.New("sync error")
			}
			return []string{"id1"}, nil
		},
	}
	r := newIndexerTestRag(fakeSync, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// 第一次失败
	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err == nil {
		t.Fatal("expected sync error")
	}

	// 验证 bgSem 已释放：再发一个应成功
	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 2})
	if err != nil {
		t.Fatalf("Index should succeed after sync fail released slot: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}
}

func TestIndex_SyncSuccess_TransfersOwnershipOnce(t *testing.T) {
	// 同步成功后容量和登记由后台释放，不泄渚
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 等待后台完成
	waitSig(t, asyncDone, "background finish")

	// 验证容量已释放：再发 8 个应全部成功
	for i := 0; i < 8; i++ {
		_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: int64(i + 10)})
		if err != nil {
			t.Fatalf("Index %d should succeed after bg released slot: %v", i, err)
		}
	}
}

func TestIndex_Shutdown_WaitsAdmittedSyncAndBG(t *testing.T) {
	// Shutdown 等待已准入的同步阶段和后台任务
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeSync := &fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			close(syncStarted)
			<-syncProceed
			return []string{"id1"}, nil
		},
	}

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return nil
		},
	}

	r := newIndexerTestRag(fakeSync, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(syncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	// 启动 Index（同步阶段阻塞）
	indexDone := make(chan struct{})
	go func() {
		defer close(indexDone)
		r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// 等待同步开始
	waitSig(t, syncStarted, "sync started")

	// 启动 Shutdown
	shutdownDone := make(chan struct{})
	var shutdownErr error
	go func() {
		defer close(shutdownDone)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr = r.Shutdown(shutdownCtx)
	}()

	// 释放同步
	close(syncProceed)

	// 等待后台完成
	waitSig(t, asyncDone, "background finish")

	// Shutdown 应完成
	waitSig(t, shutdownDone, "shutdown done")
	if shutdownErr != nil {
		t.Fatalf("Shutdown should succeed: %v", shutdownErr)
	}
}

func TestIndex_Shutdown_Timeout_KeepsWaiting(t *testing.T) {
	// Shutdown 超时后返回 ctx.Err()，但后台仍继续等待，done 最终 close
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(asyncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	// Index 成功，后台阻塞
	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shutdown 超时
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	err = r.Shutdown(shortCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")

	// 再次 Shutdown 应成功（done 已 close）
	longCtx, longCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer longCancel()
	err = r.Shutdown(longCtx)
	if err != nil {
		t.Fatalf("second Shutdown should succeed: %v", err)
	}
}

func TestIndex_Shutdown_Idempotent(t *testing.T) {
	// 多次 Shutdown 等价于一次
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		err := r.Shutdown(ctx)
		if err != nil {
			t.Fatalf("Shutdown %d should succeed: %v", i, err)
		}
	}
}

func TestIndex_AsyncWaitReceivesCtx(t *testing.T) {
	// asyncWait 接收的 ctx 是 bgCtx，可取消
	waitCtxCh := make(chan context.Context, 1)
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {
		waitCtxCh <- ctx
	}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 接收 wait 的 ctx
	var waitCtx context.Context
	select {
	case waitCtx = <-waitCtxCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for wait ctx")
	}

	if waitCtx == nil {
		t.Fatal("asyncWait should receive non-nil ctx")
	}

	// 取消 bgRootCtx 应传播到 waitCtx
	r.bgRootCancel()
	select {
	case <-waitCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("waitCtx should be cancelled when bgRootCtx is cancelled")
	}

	waitSig(t, asyncDone, "background finish")
}

func TestIndex_RequestCtxPreservedAfterHTTPReturn(t *testing.T) {
	// HTTP 请求 ctx 取消后，后台 bgCtx 不受影响（从 bgRootCtx 派生）
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})
	ctxReceived := make(chan context.Context, 1)

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			ctxReceived <- ctx
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(asyncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	// 使用可取消的 ctx 调用 Index
	reqCtx, reqCancel := context.WithCancel(context.Background())
	ids, err := r.Index(reqCtx, &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 等待后台确实收到 ctx
	var receivedCtx context.Context
	select {
	case receivedCtx = <-ctxReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for background ctx")
	}

	// 取消请求 ctx
	reqCancel()

	// 后台 ctx 不应被请求取消影响（检查时后台仍在运行）
	if receivedCtx == nil {
		t.Fatal("background should receive non-nil ctx")
	}
	select {
	case <-receivedCtx.Done():
		t.Fatal("background ctx should not be cancelled when request ctx is cancelled")
	default:
		// 正常：后台 ctx 未取消
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")
}

func TestIndex_IndexAndShutdownRace(t *testing.T) {
	// 并发 Index 和 Shutdown 不 panic、不 deadlock
	const n = 5
	asyncDone := make(chan struct{}, n)

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer func() {
				asyncDone <- struct{}{}
			}()
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	var wg sync.WaitGroup
	// 并发 Index
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r.Index(context.Background(), &IndexReq{
				URI:           fmt.Sprintf("doc%d.pdf", idx),
				KnowledgeName: fmt.Sprintf("kb%d", idx),
				DocumentsId:   int64(idx),
			})
		}(i)
	}

	// 等待所有后台完成
	for i := 0; i < n; i++ {
		select {
		case <-asyncDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("timeout waiting for async %d", i)
		}
	}

	// Shutdown 应成功
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed: %v", err)
	}

	wg.Wait()
}

// --- 编排测试 fake 类型（indexAsyncByDocsID 直接调用）---

// fakeDocSearcher 实现 docSearcher 接口
type fakeDocSearcher struct {
	result []*schema.Document
	err    error
	called bool
	events *[]string // 可选：追加 "search"，用于断言事件顺序
}

func (f *fakeDocSearcher) SearchDocumentsByIDs(_ context.Context, _ string, _ []string, _ int) ([]*schema.Document, error) {
	f.called = true
	if f.events != nil {
		*f.events = append(*f.events, "search")
	}
	return f.result, f.err
}

// fakeAsyncRunnable 实现 compose.Runnable[[]*schema.Document, []string]，仅 Invoke 被调用
type fakeAsyncRunnable struct {
	ids         []string
	err         error
	called      bool
	events      *[]string          // 可选：追加 "IndexAsync"
	invokedDocs []*schema.Document // 记录传入 IndexAsync 的文档
}

func (f *fakeAsyncRunnable) Invoke(_ context.Context, docs []*schema.Document, _ ...compose.Option) ([]string, error) {
	f.called = true
	f.invokedDocs = docs
	if f.events != nil {
		*f.events = append(*f.events, "IndexAsync")
	}
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
	asyncRunnable := &fakeAsyncRunnable{ids: []string{"id1"}}
	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		asyncRunnable,
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
	if asyncRunnable.called {
		t.Fatal("idxerAsync should not be called on marshal error")
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

	// 事件序列记录：search → saveChunks → IndexAsync → Active
	var events []string
	evp := &events
	searcher := &fakeDocSearcher{result: docs, events: evp}

	var savedDocID int64
	var savedChunks []entity.KnowledgeChunks
	var updateDocID int64
	var updateStatus int

	asyncRunnable := &fakeAsyncRunnable{ids: []string{"idx1", "idx2"}, events: evp}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, docID int64, chunks []entity.KnowledgeChunks) error {
			savedDocID = docID
			savedChunks = chunks
			events = append(events, "saveChunks")
			return nil
		},
		func(_ context.Context, docID int64, status int) error {
			updateDocID = docID
			updateStatus = status
			if status == int(v1.StatusActive) {
				events = append(events, "Active")
			}
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

	// 精确断言事件顺序
	if !reflect.DeepEqual(events, []string{"search", "saveChunks", "IndexAsync", "Active"}) {
		t.Fatalf("event sequence = %v, want [search saveChunks IndexAsync Active]", events)
	}

	// 保存的 chunk ID / 内容 / docID
	if savedDocID != 42 {
		t.Fatalf("saveChunks docID = %d, want 42", savedDocID)
	}
	if len(savedChunks) != 2 {
		t.Fatalf("savedChunks len = %d, want 2", len(savedChunks))
	}
	wantChunks := map[string]string{"d1": "c1", "d2": "c2"}
	for _, ch := range savedChunks {
		if want, ok := wantChunks[ch.ChunkId]; !ok || ch.Content != want {
			t.Fatalf("unexpected saved chunk: %+v", ch)
		}
		if ch.KnowledgeDocId != 42 {
			t.Fatalf("saved chunk %s KnowledgeDocId = %d, want 42", ch.ChunkId, ch.KnowledgeDocId)
		}
	}

	// 传入 IndexAsync 的文档
	var invokedIDs []string
	for _, d := range asyncRunnable.invokedDocs {
		invokedIDs = append(invokedIDs, d.ID)
	}
	if !reflect.DeepEqual(invokedIDs, []string{"d1", "d2"}) {
		t.Fatalf("IndexAsync invoked docs = %v, want [d1 d2]", invokedIDs)
	}

	// 返回 ids + Active 状态/docID
	if !reflect.DeepEqual(ids, []string{"idx1", "idx2"}) {
		t.Fatalf("ids = %v, want [idx1 idx2]", ids)
	}
	if updateDocID != 42 || updateStatus != int(v1.StatusActive) {
		t.Fatalf("updateDocStatus = (%d, %d), want (42, Active)", updateDocID, updateStatus)
	}
}

// assertNoDownstreamStages 断言非法返回集下三个阶段都未被调用。
func assertNoDownstreamStages(t *testing.T, saveCalled, updateCalled, asyncCalled bool) {
	t.Helper()
	if saveCalled {
		t.Error("saveChunksFn must not be called for illegal return set")
	}
	if asyncCalled {
		t.Error("idxerAsync must not be called for illegal return set")
	}
	if updateCalled {
		t.Error("updateDocStatus must not be called for illegal return set")
	}
}

// TestIndexAsyncByDocsID_UnexpectedExtraDoc 请求 [a]，返回 [a, unrelated]：
// 非预期文档必须拒绝，且 saveChunksFn/idxerAsync/updateDocStatus 均未调用。
func TestIndexAsyncByDocsID_UnexpectedExtraDoc(t *testing.T) {
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
		{ID: "unrelated", Content: "u", MetaData: map[string]any{}},
	}
	var saveCalled, updateCalled bool
	asyncRunnable := &fakeAsyncRunnable{ids: []string{"x"}}
	r := newOrchestrationTestRag(&fakeDocSearcher{result: docs},
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		asyncRunnable,
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected error for unexpected document")
	}
	if !strings.Contains(err.Error(), "unrelated") {
		t.Fatalf("error should name unexpected id, got: %v", err)
	}
	assertNoDownstreamStages(t, saveCalled, updateCalled, asyncRunnable.called)
}

// TestIndexAsyncByDocsID_DuplicateDocID 请求 [a]，返回 [a, a]：重复 ID 明确报错，
// 且三个阶段均未调用（避免重复保存/重复索引）。
func TestIndexAsyncByDocsID_DuplicateDocID(t *testing.T) {
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
	}
	var saveCalled, updateCalled bool
	asyncRunnable := &fakeAsyncRunnable{ids: []string{"x"}}
	r := newOrchestrationTestRag(&fakeDocSearcher{result: docs},
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		asyncRunnable,
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected error for duplicate doc id")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error should indicate duplicate, got: %v", err)
	}
	assertNoDownstreamStages(t, saveCalled, updateCalled, asyncRunnable.called)
}

// TestIndexAsyncByDocsID_NilDoc 返回包含 nil：明确报错，且三个阶段均未调用。
func TestIndexAsyncByDocsID_NilDoc(t *testing.T) {
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
		nil,
	}
	var saveCalled, updateCalled bool
	asyncRunnable := &fakeAsyncRunnable{ids: []string{"x"}}
	r := newOrchestrationTestRag(&fakeDocSearcher{result: docs},
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { saveCalled = true; return nil },
		func(_ context.Context, _ int64, _ int) error { updateCalled = true; return nil },
		asyncRunnable,
	)

	_, err := r.indexAsyncByDocsID(context.Background(), &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})
	if err == nil {
		t.Fatal("expected error for nil document")
	}
	if !strings.Contains(err.Error(), "nil document") {
		t.Fatalf("error should indicate nil document, got: %v", err)
	}
	assertNoDownstreamStages(t, saveCalled, updateCalled, asyncRunnable.called)
}
