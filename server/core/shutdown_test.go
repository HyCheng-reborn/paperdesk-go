package core

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// mockCloser 实现 io.Closer，记录调用顺序
type mockCloser struct {
	name     string
	closed   bool
	closeErr error
	order    *[]string // 记录关闭顺序
}

func (m *mockCloser) Close() error {
	m.closed = true
	if m.order != nil {
		*m.order = append(*m.order, m.name)
	}
	return m.closeErr
}

func TestShutdownCoordinator_ShutdownSuccess_ThenCleanup(t *testing.T) {
	// 后台结束后 Shutdown 成功，随后准确逆序清理
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	var order []string
	closer1 := &mockCloser{name: "dao", order: &order}
	closer2 := &mockCloser{name: "rag", order: &order}

	coordinator := NewShutdownCoordinator(r, closer1, closer2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := coordinator.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed: %v", err)
	}

	// 验证逆序关闭：rag 先于 dao
	if len(order) != 2 {
		t.Fatalf("expected 2 closers called, got %d", len(order))
	}
	if order[0] != "rag" || order[1] != "dao" {
		t.Fatalf("expected [rag, dao], got %v", order)
	}

	if !closer1.closed || !closer2.closed {
		t.Fatal("both closers should be closed")
	}
}

func TestShutdownCoordinator_ShutdownTimeout_NoCleanup(t *testing.T) {
	// Shutdown 超时不调用任何依赖 Cleanup
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

	var order []string
	closer1 := &mockCloser{name: "dao", order: &order}
	closer2 := &mockCloser{name: "rag", order: &order}

	coordinator := NewShutdownCoordinator(r, closer1, closer2)

	// 短超时
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()

	err = coordinator.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected error from timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")

	// Cleanup 不应被调用
	if closer1.closed || closer2.closed {
		t.Fatal("closers should not be called when Shutdown times out")
	}
	if len(order) != 0 {
		t.Fatalf("expected no closers called, got %v", order)
	}
}

func TestShutdownCoordinator_CleanupPartialFailure_ContinuesOthers(t *testing.T) {
	// Cleanup 中一个失败仍按约定清理其他资源
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	var order []string
	errSentinel := errors.New("sentinel close error")
	closer1 := &mockCloser{name: "dao", order: &order}
	closer2 := &mockCloser{name: "rag", order: &order, closeErr: errSentinel}
	closer3 := &mockCloser{name: "es", order: &order}

	coordinator := NewShutdownCoordinator(r, closer1, closer2, closer3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := coordinator.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error from partial cleanup failure")
	}
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}

	// 验证所有 closers 都被调用（逆序：es, rag, dao）
	if len(order) != 3 {
		t.Fatalf("expected 3 closers called, got %d", len(order))
	}
	if order[0] != "es" || order[1] != "rag" || order[2] != "dao" {
		t.Fatalf("expected [es, rag, dao], got %v", order)
	}

	if !closer1.closed || !closer2.closed || !closer3.closed {
		t.Fatal("all closers should be closed despite partial failure")
	}
}

func TestShutdownCoordinator_NoResources(t *testing.T) {
	// 无资源时 Shutdown 仍能成功
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	coordinator := NewShutdownCoordinator(r)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := coordinator.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown should succeed with no resources: %v", err)
	}
}

func TestShutdownCoordinator_NilCloser(t *testing.T) {
	// 包含 nil closer 时应跳过
	r := newIndexerTestRag(&fakeSyncIndexer{ids: []string{"id1"}}, &fakeAsyncIndexer{})
	r.asyncWait = func(ctx context.Context) {}

	var order []string
	closer1 := &mockCloser{name: "dao", order: &order}

	// 传入 nil closer
	coordinator := &ShutdownCoordinator{
		rag:       r,
		resources: []io.Closer{closer1, nil},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 不应 panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Shutdown should not panic on nil closer: %v", r)
		}
	}()

	err := coordinator.Shutdown(ctx)
	// 可能因为 nil closer panic 而失败，也可能跳过
	// 这里只验证不 panic
	_ = err
}

// --- 阻塞 4: 补强测试 ---

func TestShutdownCoordinator_ShutdownTimeout_ThenWaitSuccess(t *testing.T) {
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

	var order []string
	closer1 := &mockCloser{name: "dao", order: &order}

	coordinator := NewShutdownCoordinator(r, closer1)

	// Index 成功，后台阻塞
	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitSig(t, asyncStarted, "async started")

	// 第一次 Shutdown 超时
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	err = coordinator.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected error from timeout")
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")

	// 第二次 Shutdown 应成功（done 已 close，资源应被清理）
	longCtx, longCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer longCancel()
	err = coordinator.Shutdown(longCtx)
	if err != nil {
		t.Fatalf("second Shutdown should succeed: %v", err)
	}

	// 资源应被清理
	if !closer1.closed {
		t.Fatal("closer should be closed after successful Shutdown")
	}
}

func TestShutdownCoordinator_CleanupFailsBeforeConverged(t *testing.T) {
	// Shutdown 超时（未收敛）时，绝不执行资源 Cleanup
	// 即使资源本身可以关闭，也必须等待收敛成功
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

	_, err := r.Index(context.Background(), &IndexReq{URI: "test.pdf", KnowledgeName: "kb1", DocumentsId: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitSig(t, asyncStarted, "async started")

	var order []string
	closer1 := &mockCloser{name: "dao", order: &order}
	closer2 := &mockCloser{name: "es", order: &order}

	coordinator := NewShutdownCoordinator(r, closer1, closer2)

	// Shutdown 超时
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	err = coordinator.Shutdown(shortCtx)
	if err == nil {
		t.Fatal("expected error from timeout")
	}

	// 释放后台
	close(asyncProceed)
	waitSig(t, asyncDone, "background finish")

	// 验证：超时期间资源未被清理
	if closer1.closed || closer2.closed {
		t.Fatal("closers should NOT be closed when Shutdown times out (not converged)")
	}
	if len(order) != 0 {
		t.Fatalf("expected no cleanup calls, got %v", order)
	}
}
