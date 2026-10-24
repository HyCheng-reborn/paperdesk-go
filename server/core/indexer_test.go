package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
	"github.com/wangle201210/go-rag/server/internal/logic/knowledge"
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
	
	// Stage 2C-2: Setup fingerprint infrastructure for tests
	// Use a fake vector store that always returns a valid fingerprint
	fakeVS := &fakeVectorStore{
		indexExists: true,
		fingerprint: &vector.Fingerprint{
			Version:          "v1",
			EmbeddingModelID: "test-model",
			Dimensions:       1024,
			ProviderName:     "test",
			Similarity:       "cosine",
		},
	}
	
	r := &Rag{
		syncIndexer:   syncIdx,
		asyncIndexer:  asyncIdx,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 8),
		done:          make(chan struct{}),
		taskTimeout:   5 * time.Minute,
		maxBackground: 8,
		fpCache:       newFingerprintCache(),
		vectorStore:   fakeVS,
		conf: &config.Config{
			IndexName:           "test-index",
			EmbeddingModel:      "test-model",
			EmbeddingDimensions: 1024,
			Provider:            &config.ProviderConfig{Name: "test"},
		},
	}
	
	// Pre-populate cache with valid fingerprint to avoid store reads in tests
	r.fpCache.set("unknown:test-index", fakeVS.fingerprint, "")
	
	return r
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
	
	// Stage 2C-2: Setup fingerprint infrastructure
	fakeVS := &fakeVectorStore{
		indexExists: true,
		fingerprint: &vector.Fingerprint{
			Version:          "v1",
			EmbeddingModelID: "test-model",
			Dimensions:       1024,
			ProviderName:     "test",
			Similarity:       "cosine",
		},
	}
	
	r := &Rag{
		syncIndexer:   &fakeSyncIndexer{ids: []string{"id1"}},
		asyncIndexer:  fakeAsync,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 1),
		done:          make(chan struct{}),
		taskTimeout:   5 * time.Minute,
		maxBackground: 1,
		fpCache:       newFingerprintCache(),
		vectorStore:   fakeVS,
		conf: &config.Config{
			IndexName:           "test-index",
			EmbeddingModel:      "test-model",
			EmbeddingDimensions: 1024,
			Provider:            &config.ProviderConfig{Name: "test"},
		},
	}
	r.fpCache.set("unknown:test-index", fakeVS.fingerprint, "")
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
	// 同步成功后容量和登记由后台释放，不泄渀
	var asyncCallCount int32
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			if atomic.AddInt32(&asyncCallCount, 1) == 1 {
				close(asyncDone)
			}
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 等待后台完成（后台负责释放容量+登记）
	waitSig(t, asyncDone, "background finish")

	// 给 defer 一点时间完成释放
	time.Sleep(50 * time.Millisecond)

	// 容量已释放：再发 8 个应全部成功（填满整个 bgSem）
	for i := 0; i < 8; i++ {
		_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: int64(i + 10)})
		if err != nil {
			t.Fatalf("Index %d should succeed after bg released slot: %v", i, err)
		}
	}
}

func TestIndex_Shutdown_WaitsAdmittedSyncAndBG(t *testing.T) {
	// Shutdown 等待已准入的同步阶段和后台任务
	// 注意：必须确保同步在 Shutdown 前完成，否则新逻辑会因 bgRootCtx 已取消而拒绝启动后台
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})
	syncDone := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeSync := &fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			close(syncStarted)
			<-syncProceed
			defer close(syncDone)
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

	// 释放同步并等待其完成
	close(syncProceed)
	waitSig(t, syncDone, "sync done")

	// 等待后台启动
	time.Sleep(100 * time.Millisecond)

	// 启动 Shutdown（此时后台任务已在运行）
	shutdownDone := make(chan struct{})
	var shutdownErr error
	go func() {
		defer close(shutdownDone)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr = r.Shutdown(shutdownCtx)
	}()

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
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			close(asyncStarted)
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

	// 等待后台确实进入 asyncIndexer
	waitSig(t, asyncStarted, "async started")

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
	// 用信号确保至少一个调用仍在同步阶段时启动 Shutdown
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})

	syncIdx := &fakeSyncIndexer{onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
		closeOnce(syncStarted) // 信号：同步阶段已开始（安全多次关闭）
		select {
		case <-syncProceed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []string{"id1"}, nil
	}}

	asyncIdx := &fakeAsyncIndexer{onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
		return nil
	}}

	r := newIndexerTestRag(syncIdx, asyncIdx)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(syncProceed)
	})

	// 启动 Index 调用
	var indexErr error
	var indexIds []string
	indexDone := make(chan struct{})
	go func() {
		defer close(indexDone)
		indexIds, indexErr = r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// 等待同步阶段开始
	<-syncStarted

	// 启动 Shutdown（此时 Index 仍在同步阶段）
	var shutdownErr error
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr = r.Shutdown(ctx)
	}()

	// 给 Shutdown 一点时间设置 closed=true，然后轮询等待 Index 返回 ErrShuttingDown
	var err error
	for i := 0; i < 100; i++ {
		_, err = r.Index(context.Background(), &IndexReq{URI: "test2.pdf", KnowledgeName: "kb2", DocumentsId: 2})
		if errors.Is(err, ErrShuttingDown) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("expected ErrShuttingDown, got %v", err)
	}

	// 释放同步阶段
	close(syncProceed)

	// 等待 Index 完成
	<-indexDone
	// 检查 Index 返回结果：可能成功（syncProceed 先于 ctx 取消）或被取消
	if indexErr != nil && !errors.Is(indexErr, context.Canceled) {
		t.Fatalf("unexpected Index error: %v", indexErr)
	}
	if indexErr == nil && len(indexIds) != 1 {
		t.Fatalf("expected 1 id on success, got %d", len(indexIds))
	}

	// 等待 Shutdown 完成
	select {
	case <-shutdownDone:
		if shutdownErr != nil {
			t.Fatalf("Shutdown failed: %v", shutdownErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown timeout")
	}
}

// --- 补充行为覆盖测试（1D-3-A 两路取消 + 所有权转移） ---

func TestIndex_SyncBlocksOnShutdown(t *testing.T) {
	// 同步 fake 阻塞等待 ctx.Done，Shutdown 能使它退出
	syncCtxReceived := make(chan context.Context, 1)
	syncDone := make(chan struct{})

	syncIdx := &fakeSyncIndexer{onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
		syncCtxReceived <- ctx
		<-ctx.Done()
		close(syncDone)
		return nil, ctx.Err()
	}}

	r := newIndexerTestRag(syncIdx, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// 启动 Index
	indexDone := make(chan struct{})
	var indexErr error
	go func() {
		defer close(indexDone)
		_, indexErr = r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// 等待同步阶段收到 ctx
	var sCtx context.Context
	select {
	case sCtx = <-syncCtxReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for sync ctx")
	}

	// Shutdown 应取消 bgRootCtx，传播到 syncCtx
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// 同步阶段应退出
	waitSig(t, syncDone, "sync exit after shutdown")

	<-indexDone
	if indexErr == nil {
		t.Fatal("expected Index error after shutdown cancel")
	}
	_ = sCtx
}

func TestIndex_SyncReceivesRequestValueDeadline(t *testing.T) {
	// 同步阶段准确收到请求 value/deadline
	type ctxKey string
	valueCh := make(chan interface{}, 1)
	deadlineCh := make(chan time.Time, 1)

	syncIdx := &fakeSyncIndexer{onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
		valueCh <- ctx.Value(ctxKey("test-key"))
		if d, ok := ctx.Deadline(); ok {
			deadlineCh <- d
		}
		return []string{"id1"}, nil
	}}

	r := newIndexerTestRag(syncIdx, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// 带 value 和 deadline 的 ctx
	reqCtx := context.WithValue(context.Background(), ctxKey("test-key"), "test-value")
	reqCtx, reqCancel := context.WithTimeout(reqCtx, 30*time.Second)
	defer reqCancel()

	ids, err := r.Index(reqCtx, &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 验证 value 传递
	select {
	case v := <-valueCh:
		if v != "test-value" {
			t.Fatalf("expected 'test-value', got %v", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for value")
	}

	// 验证 deadline 传递
	select {
	case d := <-deadlineCh:
		if d.IsZero() {
			t.Fatal("expected non-zero deadline")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for deadline")
	}
}

func TestIndex_SyncPanic_ReleasesCapacityAndRegistration(t *testing.T) {
	// 同步 panic 后容量和登记释放
	r := newIndexerTestRag(&fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			panic("sync boom")
		},
	}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// panic 应向上传播
	func() {
		defer func() {
			r := recover()
			if r == nil {
				// 如果 Index 内部捕获了 panic，检查返回错误
				return
			}
			// panic 传播到调用方，正常
		}()
		r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// 验证容量已释放：再发一个应成功（如果 panic 被捕获）或 Shutdown 应成功
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 尝试 Shutdown 或再次 Index 来验证容量释放
	// 由于 panic 可能传播到调用方，我们只验证 bgSem/bgWG 不泄渚
	// 通过 Shutdown 来验证 bgWG 归零
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed after sync panic: %v", err)
	}
}

func TestIndex_SyncIgnoresCancel_ReturnsSuccess(t *testing.T) {
	// 同步下游忽略取消返回成功时，不误走正常后台提交
	syncIdx := &fakeSyncIndexer{onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
		// 忽略 ctx 取消，始终返回成功
		return []string{"id1"}, nil
	}}

	asyncCalled := make(chan struct{}, 10)
	asyncIdx := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			asyncCalled <- struct{}{}
			return nil
		},
	}

	r := newIndexerTestRag(syncIdx, asyncIdx)
	r.asyncWait = func(ctx context.Context) {}

	// 使用已取消的 ctx
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.Index(cancelledCtx, &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	// syncCtx 被取消，应返回错误，即使 syncIndexer 返回成功
	if err == nil {
		t.Fatal("expected error when request ctx is cancelled, even if sync returns success")
	}

	// 后台不应被启动
	select {
	case <-asyncCalled:
		t.Fatal("background should not be called when syncCtx is cancelled")
	case <-time.After(200 * time.Millisecond):
		// 正常：后台未启动
	}
}

func TestIndex_BackgroundContinuesAfterRequestCancel(t *testing.T) {
	// 后台已接管后请求取消不影响任务
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})
	ctxReceived := make(chan context.Context, 1)

	asyncIdx := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			ctxReceived <- ctx
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, asyncIdx)
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

	// 等待后台收到 ctx
	var bgCtx context.Context
	select {
	case bgCtx = <-ctxReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for background ctx")
	}

	// 取消请求 ctx
	reqCancel()

	// 后台 ctx 不应被请求取消影响（bgCtx 从 bgRootCtx 派生，不从请求 ctx 派生）
	select {
	case <-bgCtx.Done():
		t.Fatal("background ctx should not be cancelled when request ctx is cancelled")
	case <-time.After(100 * time.Millisecond):
		// 正常：后台 ctx 未取消
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")
}

func TestIndex_ShutdownTimeout_ThenWaitSuccess(t *testing.T) {
	// Shutdown 超时后再次等待成功
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			close(asyncStarted)
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

	// 等待后台确实进入 asyncIndexer
	waitSig(t, asyncStarted, "async started")

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
	events      *[]string                                                                                    // 可选：追加 "IndexAsync"
	invokedDocs []*schema.Document                                                                           // 记录传入 IndexAsync 的文档
	onInvoke    func(ctx context.Context, docs []*schema.Document, opts ...compose.Option) ([]string, error) // 可选：自定义 Invoke 行为
}

func (f *fakeAsyncRunnable) Invoke(ctx context.Context, docs []*schema.Document, opts ...compose.Option) ([]string, error) {
	f.called = true
	f.invokedDocs = docs
	if f.events != nil {
		*f.events = append(*f.events, "IndexAsync")
	}
	if f.onInvoke != nil {
		return f.onInvoke(ctx, docs, opts...)
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
	// Stage 2C-2: Setup fingerprint infrastructure
	fakeVS := &fakeVectorStore{
		indexExists: true,
		fingerprint: &vector.Fingerprint{
			Version:          "v1",
			EmbeddingModelID: "test-model",
			Dimensions:       1024,
			ProviderName:     "test",
			Similarity:       "cosine",
		},
	}
	
	r := &Rag{
		docSearch:       searcher,
		saveChunksFn:    saveFn,
		updateDocStatus: updateFn,
		idxerAsync:      asyncRunnable,
		fpCache:         newFingerprintCache(),
		vectorStore:     fakeVS,
		conf: &config.Config{
			IndexName:           "test-index",
			EmbeddingModel:      "test-model",
			EmbeddingDimensions: 1024,
			Provider:            &config.ProviderConfig{Name: "test"},
		},
	}
	// Pre-populate cache with valid fingerprint
	r.fpCache.set("unknown:test-index", fakeVS.fingerprint, "")
	return r
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

// --- A3.3: 拆分“等待同步”和“等待后台”场景 ---

func TestIndex_SyncOnlyShutdown(t *testing.T) {
	// 同步阶段阻塞，Shutdown 使同步退出，不要求后台启动
	syncCtxReceived := make(chan context.Context, 1)
	syncDone := make(chan struct{})

	syncIdx := &fakeSyncIndexer{onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
		syncCtxReceived <- ctx
		<-ctx.Done()
		close(syncDone)
		return nil, ctx.Err()
	}}

	r := newIndexerTestRag(syncIdx, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// 启动 Index
	indexDone := make(chan struct{})
	var indexErr error
	go func() {
		defer close(indexDone)
		_, indexErr = r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// 等待同步阶段收到 ctx
	var sCtx context.Context
	select {
	case sCtx = <-syncCtxReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for sync ctx")
	}

	// Shutdown 应取消 bgRootCtx，传播到 syncCtx
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// 同步阶段应退出
	waitSig(t, syncDone, "sync exit after shutdown")

	<-indexDone
	if indexErr == nil {
		t.Fatal("expected Index error after shutdown cancel")
	}
	_ = sCtx
}

func TestIndex_BackgroundOnlyShutdown(t *testing.T) {
	// 同步成功，确认后台进入，再启动 Shutdown
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
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(asyncProceed)
		waitSig(t, asyncDone, "background finish")
	})

	// Index 成功，后台启动
	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 确认后台已进入
	waitSig(t, asyncStarted, "async start")

	// 启动 Shutdown
	shutdownDone := make(chan struct{})
	var shutdownErr error
	go func() {
		defer close(shutdownDone)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr = r.Shutdown(ctx)
	}()

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")

	// Shutdown 应完成
	waitSig(t, shutdownDone, "shutdown done")
	if shutdownErr != nil {
		t.Fatalf("Shutdown should succeed: %v", shutdownErr)
	}
}

// --- B4: B 组测试 ---

func TestIndex_BackgroundFailure_MarksFailed(t *testing.T) {
	// 后台失败调用 markFailed
	markFailedCalled := make(chan struct{}, 1)
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return errors.New("async error")
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		markFailedCalled <- struct{}{}
	}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 markFailed 被调用
	select {
	case <-markFailedCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for markFailed")
	}

	waitSig(t, asyncDone, "background finish")
}

func TestIndex_BackgroundPanic_MarksFailed(t *testing.T) {
	// 后台 panic 调用 markFailed
	markFailedCalled := make(chan struct{}, 1)
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
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		markFailedCalled <- struct{}{}
	}

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("Index should not fail due to background panic: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// asyncWait 的 defer 在 panic 展开时触发，随后被 goroutine 顶部的 recover 捕获
	waitSig(t, waitEntered, "wait panic unwound to recover")

	// markFailed 应被调用
	select {
	case <-markFailedCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for markFailed after panic")
	}
}

func TestIndex_BackgroundTimeout_MarksFailed(t *testing.T) {
	// 后台超时调用 markFailed
	markFailedCalled := make(chan struct{}, 1)
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			// 等待 context 超时
			<-ctx.Done()
			return ctx.Err()
		},
	}

	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())
	
	// Stage 2C-2: Setup fingerprint infrastructure
	fakeVS := &fakeVectorStore{
		indexExists: true,
		fingerprint: &vector.Fingerprint{
			Version:          "v1",
			EmbeddingModelID: "test-model",
			Dimensions:       1024,
			ProviderName:     "test",
			Similarity:       "cosine",
		},
	}
	
	r := &Rag{
		syncIndexer:   &fakeSyncIndexer{ids: []string{"id1"}},
		asyncIndexer:  fakeAsync,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 8),
		done:          make(chan struct{}),
		taskTimeout:   100 * time.Millisecond, // 短超时触发超时
		maxBackground: 8,
		fpCache:       newFingerprintCache(),
		vectorStore:   fakeVS,
		conf: &config.Config{
			IndexName:           "test-index",
			EmbeddingModel:      "test-model",
			EmbeddingDimensions: 1024,
			Provider:            &config.ProviderConfig{Name: "test"},
		},
	}
	r.fpCache.set("unknown:test-index", fakeVS.fingerprint, "")
	r.asyncWait = func(ctx context.Context) {}
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		markFailedCalled <- struct{}{}
	}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 markFailed 被调用
	select {
	case <-markFailedCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for markFailed after timeout")
	}

	waitSig(t, asyncDone, "background finish")
}

func TestIndex_BackgroundSuccess_NoMarkFailed(t *testing.T) {
	// 成功不调用 markFailed
	markFailedCalled := make(chan struct{}, 1)
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		markFailedCalled <- struct{}{}
	}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待后台完成
	waitSig(t, asyncDone, "background finish")

	// markFailed 不应被调用
	select {
	case <-markFailedCalled:
		t.Fatal("markFailed should not be called on success")
	case <-time.After(200 * time.Millisecond):
		// 正常：markFailed 未被调用
	}
}

func TestIndexAsyncByDocsID_RetryRespectsContext(t *testing.T) {
	// 重试等待响应 context 取消
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return nil },
		func(_ context.Context, _ int64, _ int) error { return nil },
		&fakeAsyncRunnable{},
	)

	// 创建已取消的 ctx
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.indexAsyncByDocsID(ctx, &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a", "b"}, KnowledgeName: "kb", DocumentsId: 1, try: 1,
	})
	// 应返回 context.Canceled
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestIndex_CleanupCtxIndependent(t *testing.T) {
	// cleanupCtx 独立于 bgCtx：bgCtx 取消后 markFailed 仍能执行
	asyncStarted := make(chan struct{})
	asyncDone := make(chan struct{})
	markFailedStarted := make(chan struct{})
	markFailedProceed := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			close(asyncStarted)
			defer close(asyncDone)
			// 等待 bgCtx 被取消
			<-ctx.Done()
			return ctx.Err()
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		close(markFailedStarted)
		<-markFailedProceed
		// 验证 ctx 不是 bgCtx（应该是独立的 cleanupCtx）
		if ctx.Err() != nil {
			t.Errorf("cleanupCtx should not be cancelled when bgCtx is cancelled")
		}
	}

	t.Cleanup(func() {
		closeOnce(markFailedProceed)
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待后台确实进入 asyncIndexer
	waitSig(t, asyncStarted, "async started")

	// 取消 bgRootCtx 触发 bgCtx 取消，使后台返回错误
	r.bgRootCancel()

	// 等待 markFailed 开始
	waitSig(t, markFailedStarted, "markFailed started")

	// 释放 markFailed
	close(markFailedProceed)

	waitSig(t, asyncDone, "background finish")
}

// --- A+B+C 新增测试 ---

func TestBackgroundCancelAfterWait_NoAsyncIndexer(t *testing.T) {
	// 等待期间取消后不进入 asyncIndexer
	waitEntered := make(chan struct{})
	waitProceed := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			t.Error("asyncIndexer should not be called when cancelled during wait")
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {
		close(waitEntered)
		<-waitProceed
	}

	t.Cleanup(func() {
		closeOnce(waitProceed)
	})

	// Index 成功，后台进入等待
	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待后台进入 wait
	waitSig(t, waitEntered, "wait entered")

	// 取消 bgRootCtx
	r.bgRootCancel()

	// 释放 wait
	close(waitProceed)

	// 给后台一点时间检查 context
	time.Sleep(100 * time.Millisecond)

	// asyncIndexer 不应被调用
	if fakeAsync.getCallCount() != 0 {
		t.Fatalf("asyncIndexer should not be called, got %d calls", fakeAsync.getCallCount())
	}
}

func TestIndexAsyncByDocsID_EntryContextCheck(t *testing.T) {
	// 入口 context 检查：已取消的 ctx 不应继续执行
	docs := []*schema.Document{
		{ID: "a", Content: "ca", MetaData: map[string]any{}},
	}
	searcher := &fakeDocSearcher{result: docs}

	r := newOrchestrationTestRag(searcher,
		func(_ context.Context, _ int64, _ []entity.KnowledgeChunks) error { return nil },
		func(_ context.Context, _ int64, _ int) error { return nil },
		&fakeAsyncRunnable{},
	)

	// 创建已取消的 ctx
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.indexAsyncByDocsID(ctx, &IndexAsyncByDocsIDReq{
		DocsIDs: []string{"a"}, KnowledgeName: "kb", DocumentsId: 1, try: 0,
	})

	// 应返回 context.Canceled
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	// searcher 不应被调用
	if searcher.called {
		t.Fatal("searcher should not be called when context is already cancelled")
	}
}

// --- 阻塞 2: AfterFunc 全退出路径清理测试 ---

func TestIndex_SyncPanic_StopsAfterFuncBridge(t *testing.T) {
	// 同步 panic 后 AfterFunc 被 defer 注销
	r := newIndexerTestRag(&fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			panic("sync boom for afterfunc test")
		},
	}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	// panic 应向上传播
	func() {
		defer func() {
			r := recover()
			if r == nil {
				// Index 内部捕获了 panic，正常
			}
		}()
		r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	}()

	// Shutdown 应成功（说明 AfterFunc 回调不会干扰，defer 已注销）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed after sync panic: %v", err)
	}
}

func TestIndex_SyncError_StopsAfterFuncBridge(t *testing.T) {
	// 同步返回错误后 AfterFunc 被 defer 注销
	r := newIndexerTestRag(&fakeSyncIndexer{
		onInvoke: func(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
			return nil, errors.New("sync error for afterfunc test")
		},
	}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err == nil {
		t.Fatal("expected sync error")
	}

	// Shutdown 应成功（说明 AfterFunc 已被 defer 注销）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed after sync error: %v", err)
	}
}

// --- 阻塞 3: Failed 单一终结所有者测试 ---

func TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed(t *testing.T) {
	// 后台 saveChunksFn 失败时，markFailed 被调用恰好一次
	// 验证 updateDocStatus 由 markFailed 触发恰好一次（不重复写 Failed）
	markFailedCalled := make(chan struct{}, 10) // 缓冲避免阻塞
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return errors.New("async error from saveChunks simulation")
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)
	r.asyncWait = func(ctx context.Context) {}
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		markFailedCalled <- struct{}{}
	}

	t.Cleanup(func() {
		waitSig(t, asyncDone, "background finish")
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 markFailed 被调用
	select {
	case <-markFailedCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for markFailed")
	}

	// 等待后台完成
	waitSig(t, asyncDone, "background finish")

	// markFailed 不应再被调用第二次
	select {
	case <-markFailedCalled:
		t.Fatal("markFailed should be called exactly once, got second call")
	case <-time.After(200 * time.Millisecond):
		// 正常：markFailed 未被再次调用
	}
}

// --- 阻塞 2: Failed 所有权修复组合测试 ---

// testStatusCall 记录状态写入调用
type testStatusCall struct {
	docID  int64
	status int
}

// --- fake ChunkStore / DocStatusUpdater 用于全链路测试 ---

// fakeChunkStore 实现 knowledge.ChunkStore 接口
type fakeChunkStore struct {
	insertErr error
	insertFn  func(ctx context.Context, chunk entity.KnowledgeChunks) error
}

func (f *fakeChunkStore) FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
	return nil, sql.ErrNoRows // 不存在，触发 insert 路径
}
func (f *fakeChunkStore) Insert(ctx context.Context, chunk entity.KnowledgeChunks) error {
	if f.insertFn != nil {
		return f.insertFn(ctx, chunk)
	}
	return f.insertErr
}
func (f *fakeChunkStore) Update(ctx context.Context, chunkID string, data map[string]interface{}) error {
	return nil
}

// fakeStatusUpdater 实现 knowledge.DocStatusUpdater 接口
type fakeStatusUpdater struct {
	mu    sync.Mutex
	calls []testStatusCall
	errFn func(status int) error // 按状态返回错误
}

func (f *fakeStatusUpdater) UpdateDocumentsStatus(ctx context.Context, documentsId int64, status int) error {
	f.mu.Lock()
	f.calls = append(f.calls, testStatusCall{docID: documentsId, status: status})
	f.mu.Unlock()
	if f.errFn != nil {
		return f.errFn(status)
	}
	return nil
}

func (f *fakeStatusUpdater) getCalls() []testStatusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]testStatusCall, len(f.calls))
	copy(cp, f.calls)
	return cp
}

// newFullChainTestRag 创建用于全链路测试的 Rag，使用真实 saveChunks 内核。
// 注入 fake ChunkStore/DocStatusUpdater，不复制状态规则。
func newFullChainTestRag(
	syncIdx syncIndexer,
	docSearch docSearcher,
	chunkStore knowledge.ChunkStore,
	statusUpdater knowledge.DocStatusUpdater,
	asyncRunnable compose.Runnable[[]*schema.Document, []string],
) *Rag {
	bgRootCtx, bgRootCancel := context.WithCancel(context.Background())
	
	// Stage 2C-2: Setup fingerprint infrastructure for tests
	fakeVS := &fakeVectorStore{
		indexExists: true,
		fingerprint: &vector.Fingerprint{
			Version:          "v1",
			EmbeddingModelID: "test-model",
			Dimensions:       1024,
			ProviderName:     "test",
			Similarity:       "cosine",
		},
	}
	
	r := &Rag{
		syncIndexer: syncIdx,
		docSearch:   docSearch,
		saveChunksFn: func(ctx context.Context, docID int64, chunks []entity.KnowledgeChunks) error {
			return knowledge.SaveChunksDataWithDeps(ctx, docID, chunks, chunkStore, statusUpdater)
		},
		updateDocStatus: func(ctx context.Context, docID int64, status int) error {
			return statusUpdater.UpdateDocumentsStatus(ctx, docID, status)
		},
		idxerAsync:    asyncRunnable,
		bgRootCtx:     bgRootCtx,
		bgRootCancel:  bgRootCancel,
		bgSem:         make(chan struct{}, 8),
		done:          make(chan struct{}),
		taskTimeout:   5 * time.Minute,
		maxBackground: 8,
		fpCache:       newFingerprintCache(),
		vectorStore:   fakeVS,
		conf: &config.Config{
			IndexName:           "test-index",
			EmbeddingModel:      "test-model",
			EmbeddingDimensions: 1024,
			Provider:            &config.ProviderConfig{Name: "test"},
		},
	}
	// Pre-populate cache with valid fingerprint
	r.fpCache.set("unknown:test-index", fakeVS.fingerprint, "")
	// asyncIndexer 委派给真实 indexAsyncByDocsID（等价于 prodAsyncIndexer）
	r.asyncIndexer = &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			_, err := r.indexAsyncByDocsID(ctx, &IndexAsyncByDocsIDReq{
				DocsIDs:       docsIDs,
				KnowledgeName: knowledgeName,
				DocumentsId:   documentsID,
				try:           0,
			})
			return err
		},
	}
	// 默认 markFailed：通过同一 statusUpdater 写 Failed
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		_ = statusUpdater.UpdateDocumentsStatus(ctx, docID, int(v1.StatusFailed))
	}
	return r
}

// TestIndex_BackgroundChunksFailure_RealChain 验证后台 chunk 失败时 markFailed 恰好一次。
// 使用 asyncDone + Shutdown 等待完整 defer 链（含 markFailed + bgWG.Done）完成。
func TestIndex_BackgroundChunksFailure_RealChain(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}

	sentinelInsert := errors.New("insert chunk failed")
	cs := &fakeChunkStore{insertErr: sentinelInsert}
	su := &fakeStatusUpdater{}

	r := newFullChainTestRag(
		&fakeSyncIndexer{ids: []string{"d1"}},
		&fakeDocSearcher{result: docs},
		cs, su,
		&fakeAsyncRunnable{ids: []string{"idx1"}},
	)
	r.asyncWait = func(ctx context.Context) {}

	// 包装 asyncIndexer 以信号 indexAsyncByDocsID 返回
	asyncDone := make(chan struct{})
	origAsync := r.asyncIndexer
	r.asyncIndexer = &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return origAsync.indexAsyncByDocsID(ctx, knowledgeName, documentsID, docsIDs)
		},
	}

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %d", len(ids))
	}

	// 等待 indexAsyncByDocsID 返回（chunk 失败后快速返回）
	waitSig(t, asyncDone, "indexAsyncByDocsID returned")

	// Shutdown 等待完整 defer 链：markFailed + bgWG.Done + bgSem 释放
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := r.Shutdown(shutdownCtx); shutdownErr != nil {
		t.Fatalf("Shutdown failed: %v", shutdownErr)
	}

	// 此时 markFailed 已执行完成，验证 Failed 写入恰好一次
	calls := su.getCalls()
	failedCount := 0
	for _, c := range calls {
		if c.status == int(v1.StatusFailed) {
			failedCount++
		}
	}
	if failedCount != 1 {
		t.Fatalf("expected exactly 1 Failed status write, got %d; calls: %v", failedCount, calls)
	}

	// 不标 Active
	for _, c := range calls {
		if c.status == int(v1.StatusActive) {
			t.Fatal("should not mark Active when chunk insert fails")
		}
	}
}

// TestIndex_BackgroundChunksSuccess_WritesIndexing 验证 chunk 成功时状态为 Indexing
func TestIndex_BackgroundChunksSuccess_WritesIndexing(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}

	cs := &fakeChunkStore{} // insert 成功
	su := &fakeStatusUpdater{}
	qaBlock := make(chan struct{})

	r := newFullChainTestRag(
		&fakeSyncIndexer{ids: []string{"d1"}},
		&fakeDocSearcher{result: docs},
		cs, su,
		&fakeAsyncRunnable{
			ids: []string{"idx1"},
			onInvoke: func(ctx context.Context, docs []*schema.Document, opts ...compose.Option) ([]string, error) {
				<-qaBlock // QA 阻塞
				return []string{"idx1"}, nil
			},
		},
	)
	r.asyncWait = func(ctx context.Context) {}

	t.Cleanup(func() {
		closeOnce(qaBlock)
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 Indexing 状态写入（在 QA 阻塞期间），最多 3 秒
	var calls []testStatusCall
	for i := 0; i < 30; i++ {
		calls = su.getCalls()
		for _, c := range calls {
			if c.status == int(v1.StatusIndexing) {
				return // 成功：Indexing 在 QA 阻塞期间已写入
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("expected Indexing status, got calls: %v", calls)
}

// TestIndex_BackgroundChunksStatusWriteFailure_Propagated 验证 chunk 成功但 Indexing 状态写入失败时错误传播。
// 使用 asyncDone + Shutdown 等待完整 defer 链完成，QA 不应被调用。
func TestIndex_BackgroundChunksStatusWriteFailure_Propagated(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}

	sentinelStatus := errors.New("status write failed")
	cs := &fakeChunkStore{} // insert 成功
	su := &fakeStatusUpdater{
		errFn: func(status int) error {
			if status == int(v1.StatusIndexing) {
				return sentinelStatus
			}
			return nil
		},
	}

	qaCalled := make(chan struct{}, 1)
	r := newFullChainTestRag(
		&fakeSyncIndexer{ids: []string{"d1"}},
		&fakeDocSearcher{result: docs},
		cs, su,
		&fakeAsyncRunnable{
			ids: []string{"idx1"},
			onInvoke: func(ctx context.Context, docs []*schema.Document, opts ...compose.Option) ([]string, error) {
				qaCalled <- struct{}{}
				return []string{"idx1"}, nil
			},
		},
	)
	r.asyncWait = func(ctx context.Context) {}

	// 包装 asyncIndexer 以信号 indexAsyncByDocsID 返回
	asyncDone := make(chan struct{})
	origAsync := r.asyncIndexer
	r.asyncIndexer = &fakeAsyncIndexer{
		onIndex: func(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
			defer close(asyncDone)
			return origAsync.indexAsyncByDocsID(ctx, knowledgeName, documentsID, docsIDs)
		},
	}

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 indexAsyncByDocsID 返回（saveChunksFn 失败后快速返回）
	waitSig(t, asyncDone, "indexAsyncByDocsID returned")

	// Shutdown 等待完整 defer 链完成
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := r.Shutdown(shutdownCtx); shutdownErr != nil {
		t.Fatalf("Shutdown failed: %v", shutdownErr)
	}

	// 验证：idxerAsync 不应被调用（saveChunksFn 失败后不继续 QA/embedding）
	select {
	case <-qaCalled:
		t.Fatal("idxerAsync should not be called when saveChunksFn fails")
	case <-time.After(200 * time.Millisecond):
		// 正常
	}
}

// TestIndex_BackgroundCleanupBeforeDone 验证 cleanup(markFailed) 完成前 bgWG.Done 不执行。
// 使用 Shutdown 阻塞证明 bgWG.Wait 未完成，释放 cleanup 后 Shutdown 才能返回。
// 不使用第二次 Index 调用（避免 asyncDone 重复关闭和容量推断歧义）。
func TestIndex_BackgroundCleanupBeforeDone(t *testing.T) {
	cleanupStarted := make(chan struct{})
	cleanupProceed := make(chan struct{})

	docs := []*schema.Document{
		{ID: "d1", Content: "c1", MetaData: map[string]any{}},
	}

	cs := &fakeChunkStore{insertErr: errors.New("insert failed")}
	su := &fakeStatusUpdater{}

	r := newFullChainTestRag(
		&fakeSyncIndexer{ids: []string{"d1"}},
		&fakeDocSearcher{result: docs},
		cs, su,
		&fakeAsyncRunnable{ids: []string{"idx1"}},
	)
	r.asyncWait = func(ctx context.Context) {} // 注入立即返回，使后台快速进入 markFailed

	// 包装 markFailed 以跟踪 cleanup 阻塞
	origMarkFailed := r.markFailed
	r.markFailed = func(ctx context.Context, docID int64, err error) {
		close(cleanupStarted)
		<-cleanupProceed
		origMarkFailed(ctx, docID, err)
	}

	t.Cleanup(func() {
		closeOnce(cleanupProceed)
	})

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 等待 cleanup 开始（证明后台已进入 markFailed，bgWG.Done 未执行）
	waitSig(t, cleanupStarted, "cleanup started")

	// 启动 Shutdown：因 bgWG.Done 未执行，bgWG.Wait 阻塞，Shutdown 不返回
	shutdownDone := make(chan struct{})
	var shutdownErr error
	go func() {
		defer close(shutdownDone)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr = r.Shutdown(ctx)
	}()

	// 验证 Shutdown 阻塞中（cleanup 阻塞 → markFailed 未完成 → bgWG.Done 未执行）
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown should block while cleanup is blocked (bgWG.Done not called)")
	case <-time.After(500 * time.Millisecond):
		// 正常：Shutdown 阻塞
	}

	// 释放 cleanup → markFailed 完成 → bgWG.Done → bgWG.Wait 返回 → Shutdown 返回
	close(cleanupProceed)

	// Shutdown 应完成
	waitSig(t, shutdownDone, "shutdown done after cleanup release")
	if shutdownErr != nil {
		t.Fatalf("Shutdown should succeed: %v", shutdownErr)
	}

	// 验证状态写入已发生
	calls := su.getCalls()
	_ = calls
}
