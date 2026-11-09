package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

// --- 测试用 fake（同包可访问私有 saveChunks / chunkStore / docStatusUpdater）---

type updateCall struct {
	chunkID string
	data    map[string]interface{}
}

// fakeChunkStore 记录所有调用，行为由可选 hook 控制；默认 FindByChunkID 返回 sql.ErrNoRows。
type fakeChunkStore struct {
	mu       sync.Mutex
	findByID func(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error)
	insert   func(ctx context.Context, chunk entity.KnowledgeChunks) error
	update   func(ctx context.Context, chunkID string, data map[string]interface{}) error

	findCalls   []string
	insertCalls []string
	updateCalls []updateCall
}

func (f *fakeChunkStore) FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
	f.mu.Lock()
	f.findCalls = append(f.findCalls, chunkID)
	f.mu.Unlock()
	if f.findByID != nil {
		return f.findByID(ctx, chunkID)
	}
	return nil, sql.ErrNoRows
}

func (f *fakeChunkStore) Insert(ctx context.Context, chunk entity.KnowledgeChunks) error {
	f.mu.Lock()
	f.insertCalls = append(f.insertCalls, chunk.ChunkId)
	f.mu.Unlock()
	if f.insert != nil {
		return f.insert(ctx, chunk)
	}
	return nil
}

func (f *fakeChunkStore) Update(ctx context.Context, chunkID string, data map[string]interface{}) error {
	f.mu.Lock()
	// 复制 data，避免调用方后续修改影响断言
	cp := make(map[string]interface{}, len(data))
	for k, v := range data {
		cp[k] = v
	}
	f.updateCalls = append(f.updateCalls, updateCall{chunkID: chunkID, data: cp})
	f.mu.Unlock()
	if f.update != nil {
		return f.update(ctx, chunkID, data)
	}
	return nil
}

func (f *fakeChunkStore) getFindCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.findCalls...)
}

func (f *fakeChunkStore) getInsertCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.insertCalls...)
}

func (f *fakeChunkStore) getUpdateCalls() []updateCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]updateCall(nil), f.updateCalls...)
}

type statusCall struct {
	docID  int64
	status int
}

type fakeDocStatusUpdater struct {
	mu    sync.Mutex
	calls []statusCall
	err   error
}

func (f *fakeDocStatusUpdater) UpdateDocumentsStatus(_ context.Context, documentsId int64, status int) error {
	f.mu.Lock()
	f.calls = append(f.calls, statusCall{docID: documentsId, status: status})
	f.mu.Unlock()
	return f.err
}

func (f *fakeDocStatusUpdater) getCalls() []statusCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statusCall(nil), f.calls...)
}

// --- 工具函数 ---

func newChunks(ids ...string) []entity.KnowledgeChunks {
	out := make([]entity.KnowledgeChunks, 0, len(ids))
	for _, id := range ids {
		out = append(out, entity.KnowledgeChunks{
			Id:             0,
			KnowledgeDocId: 42,
			ChunkId:        id,
			Content:        "content-" + id,
			Ext:            `{"k":"v"}`,
		})
	}
	return out
}

// joinedErrors 拆出 errors.Join 产生的多错误；非 join 结果返回长度 1 的切片。
func joinedErrors(err error) []error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		return multi.Unwrap()
	}
	return []error{err}
}

// assertLastStatus 断言最后一次文档状态写入的 docID 与 status。
func assertLastStatus(t *testing.T, su *fakeDocStatusUpdater, wantDocID int64, wantStatus int) {
	t.Helper()
	calls := su.getCalls()
	if len(calls) == 0 {
		t.Fatalf("expected status write, got none")
	}
	last := calls[len(calls)-1]
	if last.docID != wantDocID {
		t.Fatalf("status write docID = %d, want %d", last.docID, wantDocID)
	}
	if last.status != wantStatus {
		t.Fatalf("status write status = %d, want %d", last.status, wantStatus)
	}
}

// --- 行为测试 ---

// TestSaveChunks_AllSuccess 全部 insert 成功：返回 nil，状态写 Indexing。
func TestSaveChunks_AllSuccess(t *testing.T) {
	store := &fakeChunkStore{} // 默认 find 返回 sql.ErrNoRows → 走 insert
	su := &fakeDocStatusUpdater{}

	err := saveChunks(context.Background(), 42, newChunks("c1", "c2", "c3"), store, su)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	got := store.getInsertCalls()
	want := []string{"c1", "c2", "c3"}
	if len(got) != len(want) {
		t.Fatalf("insert calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("insert calls = %v, want %v", got, want)
		}
	}
	if len(store.getUpdateCalls()) != 0 {
		t.Fatalf("unexpected update calls: %v", store.getUpdateCalls())
	}
	assertLastStatus(t, su, 42, int(v1.StatusIndexing))
}

// TestSaveChunks_PartialFailure 部分失败：聚合 find 与 insert 两个错误，状态降为 Failed，
// 且失败不中断循环（后续 chunk 仍被处理）。
func TestSaveChunks_PartialFailure(t *testing.T) {
	sentinelFind := errors.New("db connection reset")
	sentinelInsert := errors.New("unique constraint violated")

	store := &fakeChunkStore{
		findByID: func(_ context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
			if chunkID == "c1" {
				return nil, sentinelFind
			}
			return nil, sql.ErrNoRows
		},
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			if chunk.ChunkId == "c2" {
				return sentinelInsert
			}
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(context.Background(), 42, newChunks("c1", "c2", "c3"), store, su)
	if err == nil {
		t.Fatal("expected aggregated error")
	}

	joined := joinedErrors(err)
	if len(joined) != 2 {
		t.Fatalf("expected 2 joined errors, got %d: %v", len(joined), err)
	}
	if !errors.Is(err, sentinelFind) {
		t.Fatalf("error should contain find failure: %v", err)
	}
	if !errors.Is(err, sentinelInsert) {
		t.Fatalf("error should contain insert failure: %v", err)
	}
	if !strings.Contains(err.Error(), "find chunk c1") {
		t.Fatalf("error should name the failing chunk (find): %v", err)
	}
	if !strings.Contains(err.Error(), "insert chunk c2") {
		t.Fatalf("error should name the failing chunk (insert): %v", err)
	}

	// c1 走 find 失败分支，不得降级为 insert；c2、c3 仍被尝试
	got := store.getInsertCalls()
	if len(got) != 2 || got[0] != "c2" || got[1] != "c3" {
		t.Fatalf("insert calls = %v, want [c2 c3]", got)
	}
	assertLastStatus(t, su, 42, int(v1.StatusFailed))
}

// TestSaveChunks_ScanDBError_NotTreatedAsInsert FindByChunkID 返回非 ErrNoRows 错误时，
// 绝不能降级为 Insert。
func TestSaveChunks_ScanDBError_NotTreatedAsInsert(t *testing.T) {
	sentinel := errors.New("connection refused")

	store := &fakeChunkStore{
		findByID: func(_ context.Context, _ string) (*entity.KnowledgeChunks, error) {
			return nil, sentinel
		},
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			t.Errorf("Insert must not be called after find DB error, got chunk %s", chunk.ChunkId)
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(context.Background(), 7, newChunks("c1"), store, su)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error should wrap find failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "find chunk c1") {
		t.Fatalf("error should name the chunk: %v", err)
	}
	if calls := store.getInsertCalls(); len(calls) != 0 {
		t.Fatalf("Insert should not be called, got %v", calls)
	}
	assertLastStatus(t, su, 7, int(v1.StatusFailed))
}

// TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite 验证 saveChunks 在 statusUpdater == nil 时
// 不写状态但仍聚合错误。
func TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite(t *testing.T) {
	sentinelInsert := errors.New("insert rejected")

	store := &fakeChunkStore{
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			if chunk.ChunkId == "c1" {
				return sentinelInsert
			}
			return nil
		},
	}

	// statusUpdater 为 nil
	err := saveChunks(context.Background(), 42, newChunks("c1", "c2"), store, nil)
	if err == nil {
		t.Fatal("expected error from insert failure")
	}
	if !errors.Is(err, sentinelInsert) {
		t.Fatalf("error should wrap insert failure, got: %v", err)
	}

	// c2 应仍被处理（失败不中断循环）
	inserts := store.getInsertCalls()
	if len(inserts) != 2 {
		t.Fatalf("expected 2 insert calls, got %d", len(inserts))
	}

	// 无 panic，无状态写入（因为没有 statusUpdater）
}

// TestSaveChunks_UpdatePath_NoStatusOverwrite 已存在记录走 update 路径，
// 写入的 data map 不含 "status" 键（不覆盖 chunk 自身状态），且不调用 Insert。
func TestSaveChunks_UpdatePath_NoStatusOverwrite(t *testing.T) {
	store := &fakeChunkStore{
		findByID: func(_ context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
			if chunkID == "c1" {
				return &entity.KnowledgeChunks{Id: 101, ChunkId: chunkID, Status: int(v1.StatusActive)}, nil
			}
			return nil, sql.ErrNoRows
		},
		update: func(_ context.Context, _ string, data map[string]interface{}) error {
			if _, ok := data["status"]; ok {
				t.Errorf("update data must not contain \"status\" key, got %v", data)
			}
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(context.Background(), 42, newChunks("c1", "c2"), store, su)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updates := store.getUpdateCalls()
	if len(updates) != 1 || updates[0].chunkID != "c1" {
		t.Fatalf("update calls = %v, want exactly one for c1", updates)
	}
	for _, key := range []string{"knowledge_doc_id", "content", "ext"} {
		if _, ok := updates[0].data[key]; !ok {
			t.Fatalf("update data missing %q key: %v", key, updates[0].data)
		}
	}
	if _, ok := updates[0].data["status"]; ok {
		t.Fatalf("update data must not contain status key: %v", updates[0].data)
	}
	if updates[0].data["knowledge_doc_id"].(int64) != 42 {
		t.Fatalf("update knowledge_doc_id = %v, want 42", updates[0].data["knowledge_doc_id"])
	}

	// 只有 c2 未命中，才走 insert
	inserts := store.getInsertCalls()
	if len(inserts) != 1 || inserts[0] != "c2" {
		t.Fatalf("insert calls = %v, want [c2]", inserts)
	}
	// c1 成功、c2 成功 → Indexing
	assertLastStatus(t, su, 42, int(v1.StatusIndexing))
}

// TestSaveChunks_EmptyChunks 空切片直接返回 nil，不查库、不写状态。
func TestSaveChunks_EmptyChunks(t *testing.T) {
	store := &fakeChunkStore{
		findByID: func(_ context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
			t.Errorf("FindByChunkID must not be called for empty chunks, got %s", chunkID)
			return nil, sql.ErrNoRows
		},
	}
	su := &fakeDocStatusUpdater{}

	for _, chunks := range [][]entity.KnowledgeChunks{nil, {}} {
		if err := saveChunks(context.Background(), 42, chunks, store, su); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	}

	if calls := su.getCalls(); len(calls) != 0 {
		t.Fatalf("status updater should not be called for empty chunks, got %v", calls)
	}
	if calls := store.getFindCalls(); len(calls) != 0 {
		t.Fatalf("store should not be called for empty chunks, got %v", calls)
	}
}

// TestSaveChunks_PreCancelled 已取消 ctx 快速返回 context.Canceled，不执行任何 store/状态操作。
// 证据边界：只证明应用层入口检查，不证明 driver 能中断执行中的 SQL。
func TestSaveChunks_PreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store := &fakeChunkStore{
		findByID: func(_ context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
			t.Errorf("FindByChunkID must not be called on pre-cancelled ctx, got %s", chunkID)
			return nil, sql.ErrNoRows
		},
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			t.Errorf("Insert must not be called on pre-cancelled ctx, got %s", chunk.ChunkId)
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(ctx, 42, newChunks("c1", "c2"), store, su)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls := su.getCalls(); len(calls) != 0 {
		t.Fatalf("status updater should not be called on pre-cancel, got %v", calls)
	}
	if calls := store.getInsertCalls(); len(calls) != 0 {
		t.Fatalf("store should not be called on pre-cancel, got %v", calls)
	}
}

// TestSaveChunks_MidLoopCancel 第 1 个 chunk 处理后取消：仅处理 1 个 chunk，
// 错误含 context.Canceled，状态降为 Failed。
func TestSaveChunks_MidLoopCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &fakeChunkStore{
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			if chunk.ChunkId == "c1" {
				// 模拟第 1 个 chunk 写入完成后调用方取消
				cancel()
				return nil
			}
			t.Errorf("Insert must not be called after cancel, got %s", chunk.ChunkId)
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(ctx, 42, newChunks("c1", "c2", "c3"), store, su)
	if err == nil {
		t.Fatal("expected error after mid-loop cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "c2") {
		t.Fatalf("error should name the unprocessed chunk c2, got %v", err)
	}

	inserts := store.getInsertCalls()
	if len(inserts) != 1 || inserts[0] != "c1" {
		t.Fatalf("insert calls = %v, want [c1] only", inserts)
	}
	if got := len(store.getFindCalls()); got != 1 {
		t.Fatalf("find calls = %d, want 1 (only c1 reached the store; c2 was blocked by the loop cancel check)", got)
	}
	assertLastStatus(t, su, 42, int(v1.StatusFailed))
}

// TestSaveChunks_StatusWriteError_Joined chunks 全部成功但状态写入失败：错误聚合返回，
// 且状态写入确实发生过一次（Indexing）。
func TestSaveChunks_StatusWriteError_Joined(t *testing.T) {
	sentinel := errors.New("status table locked")

	store := &fakeChunkStore{}
	su := &fakeDocStatusUpdater{err: sentinel}

	err := saveChunks(context.Background(), 42, newChunks("c1", "c2"), store, su)
	if err == nil {
		t.Fatal("expected error from status write")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error should wrap status write failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "update doc 42 status") {
		t.Fatalf("error should name the doc id: %v", err)
	}
	// chunks 本身全部成功
	if got := store.getInsertCalls(); len(got) != 2 {
		t.Fatalf("insert calls = %v, want 2", got)
	}
	// 期望状态为 Indexing（没有 chunk 失败），写入失败只影响返回值
	assertLastStatus(t, su, 42, int(v1.StatusIndexing))
	if calls := su.getCalls(); len(calls) != 1 {
		t.Fatalf("status updater should be called exactly once, got %d", len(calls))
	}
}

// TestSaveChunks_ChunkAndStatusErrorsBothJoined 一次调用中让 chunk 写入(insert) 与
// 文档状态更新分别返回不同 sentinel：断言 errors.Is 同时识别两个错误，且消息包含
// 对应的 chunk ID 与 doc ID。
func TestSaveChunks_ChunkAndStatusErrorsBothJoined(t *testing.T) {
	sentinelInsert := errors.New("insert rejected")
	sentinelStatus := errors.New("status write rejected")

	store := &fakeChunkStore{
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			if chunk.ChunkId == "c1" {
				return sentinelInsert
			}
			return nil
		},
	}
	su := &fakeDocStatusUpdater{err: sentinelStatus}

	err := saveChunks(context.Background(), 42, newChunks("c1", "c2"), store, su)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if !errors.Is(err, sentinelInsert) {
		t.Fatalf("errors.Is should recognize chunk insert failure: %v", err)
	}
	if !errors.Is(err, sentinelStatus) {
		t.Fatalf("errors.Is should recognize status write failure: %v", err)
	}
	if !strings.Contains(err.Error(), "insert chunk c1") {
		t.Fatalf("error should include failing chunk id c1: %v", err)
	}
	if !strings.Contains(err.Error(), "update doc 42 status") {
		t.Fatalf("error should include doc id 42: %v", err)
	}
	// chunk 失败 → 文档状态降为 Failed
	assertLastStatus(t, su, 42, int(v1.StatusFailed))
}

// TestSaveChunks_UpdateFailure_Propagated 覆盖与 insert 不同的 update 分支：
// 已存在记录→Update 返回 sentinel，错误应被传播且包含 chunk ID，状态降为 Failed。
func TestSaveChunks_UpdateFailure_Propagated(t *testing.T) {
	sentinelUpdate := errors.New("update rejected")

	store := &fakeChunkStore{
		findByID: func(_ context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
			return &entity.KnowledgeChunks{Id: 5, ChunkId: chunkID}, nil // 命中 → update 路径
		},
		update: func(_ context.Context, _ string, _ map[string]interface{}) error {
			return sentinelUpdate
		},
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			t.Errorf("Insert must not run on the update branch, got %s", chunk.ChunkId)
			return nil
		},
	}
	su := &fakeDocStatusUpdater{}

	err := saveChunks(context.Background(), 7, newChunks("cU"), store, su)
	if err == nil {
		t.Fatal("expected error from update failure")
	}
	if !errors.Is(err, sentinelUpdate) {
		t.Fatalf("errors.Is should recognize update failure: %v", err)
	}
	if !strings.Contains(err.Error(), "update chunk cU") {
		t.Fatalf("error should name the failing chunk on update branch: %v", err)
	}
	if calls := store.getInsertCalls(); len(calls) != 0 {
		t.Fatalf("Insert should not be called on update branch, got %v", calls)
	}
	assertLastStatus(t, su, 7, int(v1.StatusFailed))
}

// TestSaveChunks_IndexingOnlyUpdater_SkipsFailed 验证 indexingOnlyUpdater 写 Indexing 但跳过 Failed
func TestSaveChunks_IndexingOnlyUpdater_SkipsFailed(t *testing.T) {
	sentinelInsert := errors.New("insert failed")

	store := &fakeChunkStore{
		insert: func(_ context.Context, chunk entity.KnowledgeChunks) error {
			if chunk.ChunkId == "c1" {
				return sentinelInsert
			}
			return nil
		},
	}
	inner := &fakeDocStatusUpdater{}
	updater := &indexingOnlyUpdater{inner: inner}

	// chunk 失败时，status 应为 Failed，但 indexingOnlyUpdater 应跳过
	err := saveChunks(context.Background(), 42, newChunks("c1", "c2"), store, updater)
	if err == nil {
		t.Fatal("expected error from insert failure")
	}
	if !errors.Is(err, sentinelInsert) {
		t.Fatalf("error should wrap insert failure, got: %v", err)
	}

	// 验证状态写入：应跳过 Failed，不写任何状态
	calls := inner.getCalls()
	for _, c := range calls {
		if c.status == int(v1.StatusFailed) {
			t.Fatal("indexingOnlyUpdater should skip Failed status write")
		}
	}

	// 成功场景：应写 Indexing
	store2 := &fakeChunkStore{}
	inner2 := &fakeDocStatusUpdater{}
	updater2 := &indexingOnlyUpdater{inner: inner2}

	err = saveChunks(context.Background(), 42, newChunks("c1", "c2"), store2, updater2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 应写 Indexing
	calls2 := inner2.getCalls()
	if len(calls2) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(calls2))
	}
	if calls2[0].status != int(v1.StatusIndexing) {
		t.Fatalf("expected Indexing status, got %d", calls2[0].status)
	}
}

// TestSaveChunks_PublicEntry_Compatibility 验证公开入口行为兼容性
func TestSaveChunks_PublicEntry_Compatibility(t *testing.T) {
	// 验证 indexingOnlyUpdater 与 saveChunks 内核的集成
	store := &fakeChunkStore{}
	inner := &fakeDocStatusUpdater{}
	updater := &indexingOnlyUpdater{inner: inner}

	// 全部成功 → Indexing
	err := saveChunks(context.Background(), 42, newChunks("c1"), store, updater)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := inner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 status call, got %d", len(calls))
	}
	if calls[0].status != int(v1.StatusIndexing) {
		t.Fatalf("expected Indexing, got %d", calls[0].status)
	}
	if calls[0].docID != 42 {
		t.Fatalf("expected docID 42, got %d", calls[0].docID)
	}
}
