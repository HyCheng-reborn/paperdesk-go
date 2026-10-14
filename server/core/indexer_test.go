//go:build offline

package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/document"
)

// --- 测试用 fake ---

type fakeSyncIndexer struct {
	ids []string
	err error
}

func (f *fakeSyncIndexer) invoke(_ context.Context, _ string, _ document.Source) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	result := make([]string, len(f.ids))
	copy(result, f.ids)
	return result, nil
}

type fakeAsyncIndexer struct {
	mu        sync.Mutex
	onIndex   func(knowledgeName, documentsID string, docsIDs []string) error
	callCount int
}

func (f *fakeAsyncIndexer) indexAsyncByDocsID(knowledgeName, documentsID string, docsIDs []string) error {
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

// --- 行为测试 ---

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
	// 同步成功时后台仍阻塞，Index 已返回
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	defer close(asyncProceed)

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			close(asyncStarted)
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}

	// 验证后台已启动
	select {
	case <-asyncStarted:
		// 成功
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for async to start")
	}
}

func TestIndex_AsyncFailure_SyncReturnUnchanged(t *testing.T) {
	// 后台失败不改变同步返回的 ids/error
	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			return errors.New("async error")
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}
}

func TestIndex_ModifyReqAfterReturn_AsyncGetsSnapshot(t *testing.T) {
	// 修改调用方 req 后，后台仍收到原始标量快照
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	defer close(asyncProceed)

	knowledgeNameCh := make(chan string, 1)
	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			close(asyncStarted)
			knowledgeNameCh <- knowledgeName
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	req := &IndexReq{URI: "test.pdf", KnowledgeName: "original", DocumentsId: 1}
	ids, err := r.Index(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}

	// 修改 req
	req.KnowledgeName = "modified"

	select {
	case <-asyncStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for async to start")
	}
	// defer close(asyncProceed) 会在测试结束时释放后台

	select {
	case receivedKnowledgeName := <-knowledgeNameCh:
		if receivedKnowledgeName != "original" {
			t.Fatalf("async should receive original snapshot, got %q", receivedKnowledgeName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for knowledgeName")
	}
}

func TestIndex_ModifyReturnedIDs_AsyncDocsIDsUnchanged(t *testing.T) {
	// 修改返回 ids 元素后，后台 DocsIDs 不变
	asyncStarted := make(chan struct{})
	asyncProceed := make(chan struct{})
	defer close(asyncProceed)

	docsIDsCh := make(chan []string, 1)
	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			close(asyncStarted)
			snapshot := make([]string, len(docsIDs))
			copy(snapshot, docsIDs)
			docsIDsCh <- snapshot
			<-asyncProceed
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	ids, _ := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})

	// 修改返回的 ids
	ids[0] = "modified"

	select {
	case <-asyncStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for async to start")
	}
	// defer close(asyncProceed) 会在测试结束时释放后台

	select {
	case receivedDocsIDs := <-docsIDsCh:
		if receivedDocsIDs[0] != "id1" {
			t.Fatalf("async DocsIDs should be unchanged, got %q", receivedDocsIDs[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for docsIDs")
	}
}

func TestIndex_AsyncModifiesOwnDocsIDs_CallerIDsUnchanged(t *testing.T) {
	// 后台修改自己的 DocsIDs 后，调用方返回 ids 不变
	asyncDone := make(chan struct{})
	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			defer close(asyncDone)
			docsIDs[0] = "modified"
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	ids, _ := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})

	// 等待后台完成
	select {
	case <-asyncDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for async to complete")
	}

	if ids[0] != "id1" {
		t.Fatalf("caller ids should be unchanged, got %q", ids[0])
	}
}

func TestIndex_ConcurrentCalls_NoCrossContamination(t *testing.T) {
	// 多次并发 Index 的结果和后台任务互不串用
	var wg sync.WaitGroup
	results := make([][]string, 3)
	asyncCount := int32(0)
	var mu sync.Mutex

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			mu.Lock()
			asyncCount++
			mu.Unlock()
			return nil
		},
	}

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := &IndexReq{URI: "test.pdf", KnowledgeName: fmt.Sprintf("kb%d", idx), DocumentsId: int64(idx)}
			ids, err := r.Index(context.Background(), req)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			results[idx] = ids
		}(i)
	}

	wg.Wait()

	// 等待后台任务完成（goroutine 有 1s sleep）
	time.Sleep(2 * time.Second)

	// 验证每个调用收到自己的结果
	for i := 0; i < 3; i++ {
		if len(results[i]) != 1 || results[i][0] != "id1" {
			t.Fatalf("call %d: expected [id1], got %v", i, results[i])
		}
	}

	// 验证后台被调用了 3 次
	mu.Lock()
	if asyncCount != 3 {
		t.Fatalf("expected 3 async calls, got %d", asyncCount)
	}
	mu.Unlock()
}

func TestIndex_WithCleanup(t *testing.T) {
	// 不设 asyncWait，后台自然完成
	// t.Cleanup 保证断言失败时等待已启动后台任务
	asyncDone := make(chan struct{})

	fakeAsync := &fakeAsyncIndexer{
		onIndex: func(knowledgeName, documentsID string, docsIDs []string) error {
			defer close(asyncDone)
			return nil
		},
	}
	t.Cleanup(func() {
		select {
		case <-asyncDone:
		case <-time.After(5 * time.Second):
		}
	})

	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, fakeAsync)

	ids, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatal("expected 1 id")
	}

	// 等待后台完成（需要越过 1s sleep）
	select {
	case <-asyncDone:
		// 成功
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for async")
	}
}
