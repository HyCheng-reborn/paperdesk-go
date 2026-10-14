package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeChatModel 测试用 fake chat model，实现 model.BaseChatModel
type fakeChatModel struct {
	queries    []string // 按顺序返回的优化查询
	idx        int
	mu         sync.Mutex
	err        error
	onGenerate func(ctx context.Context, content string) (string, error)
}

func (f *fakeChatModel) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if f.onGenerate != nil {
		// Extract last user message for callback
		var content string
		for _, m := range msgs {
			if m.Role == "user" || len(msgs) == 1 {
				content = m.Content
			}
		}
		s, err := f.onGenerate(ctx, content)
		if err != nil {
			return nil, err
		}
		return &schema.Message{Content: s}, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.queries) {
		return &schema.Message{Content: "default-query"}, nil
	}
	q := f.queries[f.idx]
	f.idx++
	return &schema.Message{Content: q}, nil
}

func (f *fakeChatModel) Stream(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not implemented")
}

// fakeDocRetriever 测试用 fake docRetriever
type fakeDocRetriever struct {
	mu         sync.Mutex
	results    map[string][]*schema.Document // query -> results
	err        error
	queries    []string // 记录收到的查询
	callCount  int
	barrier    chan struct{}
	onRetrieve func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error)
}

func (f *fakeDocRetriever) retrieveDoOnce(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
	f.mu.Lock()
	f.queries = append(f.queries, req.optQuery)
	f.callCount++
	f.mu.Unlock()

	if f.onRetrieve != nil {
		return f.onRetrieve(ctx, req)
	}

	if f.barrier != nil {
		select {
		case <-f.barrier:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.err != nil {
		return nil, f.err
	}

	docs, ok := f.results[req.optQuery]
	if !ok {
		return nil, nil
	}
	return docs, nil
}

// newTestRag 创建用于测试的 Rag 实例（跳过 New() 的副作用）
func newTestRag(cm model.BaseChatModel, dr docRetriever) *Rag {
	return &Rag{
		docRet:  dr,
		rwModel: cm,
		cm:      cm,
	}
}

func TestRetrieve_NilRequest(t *testing.T) {
	r := newTestRag(nil, nil)
	_, err := r.Retrieve(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if err.Error() != "nil request" {
		t.Fatalf("expected 'nil request', got: %v", err)
	}
}

func TestRetrieve_InvalidTopK(t *testing.T) {
	r := newTestRag(nil, nil)

	_, err := r.Retrieve(context.Background(), &RetrieveReq{Query: "test", TopK: 0})
	if err == nil {
		t.Fatal("expected error for TopK=0")
	}

	_, err = r.Retrieve(context.Background(), &RetrieveReq{Query: "test", TopK: -1})
	if err == nil {
		t.Fatal("expected error for TopK=-1")
	}
}

func TestRetrieve_ThreeRoundsUseOwnQuery(t *testing.T) {
	// 验证三轮分别使用各自优化 query
	expectedQueries := []string{"query-0", "query-1", "query-2"}

	cm := &fakeChatModel{queries: expectedQueries}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"query-0": {{ID: "a", Content: "a"}},
			"query-1": {{ID: "b", Content: "b"}},
			"query-2": {{ID: "c", Content: "c"}},
		},
	}

	r := newTestRag(cm, dr)
	_, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatal(err)
	}

	dr.mu.Lock()
	queries := dr.queries
	dr.mu.Unlock()

	if len(queries) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(queries))
	}

	seen := make(map[string]bool)
	for _, q := range queries {
		seen[q] = true
	}
	for _, eq := range expectedQueries {
		if !seen[eq] {
			t.Fatalf("expected query %q not found in %v", eq, queries)
		}
	}
}

func TestRetrieve_InputRequestUnchanged(t *testing.T) {
	// 验证输入请求（含 excludeIDs 独立快照）在并发多轮中保持不变
	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			// 每个 worker 收到独立副本，修改前先断言首位为原始值
			if len(req.excludeIDs) > 0 {
				if req.excludeIDs[0] != "id1" {
					t.Errorf("worker copy excludeIDs[0] = %q, want %q", req.excludeIDs[0], "id1")
				}
				// worker 修改自己的副本，不得影响输入
				req.excludeIDs[0] = "modified"
			}
			return []*schema.Document{docWithScore("a", "a", 0.5)}, nil
		},
	}

	r := newTestRag(cm, dr)
	req := &RetrieveReq{
		Query:         "original",
		TopK:          5,
		Score:         1.5,
		KnowledgeName: "kb",
	}
	original := []string{"id1", "id2"}
	snapshot := append([]string(nil), original...)
	req.excludeIDs = original

	if _, err := r.Retrieve(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	// 验证输入请求未被修改
	if req.Query != "original" {
		t.Fatalf("Query was modified: %s", req.Query)
	}
	if req.TopK != 5 {
		t.Fatalf("TopK was modified: %d", req.TopK)
	}
	// 最后检查输入的独立快照保持不变
	if !reflect.DeepEqual(req.excludeIDs, snapshot) {
		t.Fatalf("input excludeIDs snapshot changed: got %v, want %v", req.excludeIDs, snapshot)
	}
}

func TestRetrieve_ConcurrentCallsNoPollution(t *testing.T) {
	// 并发调用互不污染
	var mu sync.Mutex
	allQueries := make([]string, 0)

	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {{ID: "a", Content: "a"}},
			"q1": {{ID: "b", Content: "b"}},
			"q2": {{ID: "c", Content: "c"}},
		},
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			mu.Lock()
			allQueries = append(allQueries, req.optQuery)
			mu.Unlock()
			return nil, nil
		},
	}

	r := newTestRag(cm, dr)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Retrieve(context.Background(), &RetrieveReq{
				Query:         "test",
				TopK:          10,
				KnowledgeName: "kb",
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	// 每次调用应该产生 3 轮查询，共 15 次
	if len(allQueries) != 15 {
		t.Fatalf("expected 15 total queries, got %d", len(allQueries))
	}
}

func TestRetrieve_SameIDKeepHighestScore(t *testing.T) {
	// 相同 ID 的多个分数以不同完成顺序返回，始终保留最高分
	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {docWithScore("same-id", "low", 0.3)},
			"q1": {docWithScore("same-id", "high", 0.9)},
			"q2": {docWithScore("same-id", "mid", 0.5)},
		},
	}

	r := newTestRag(cm, dr)
	docs, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(docs) != 1 {
		t.Fatalf("expected 1 unique doc, got %d", len(docs))
	}
	if docs[0].ID != "same-id" {
		t.Fatalf("expected ID 'same-id', got %s", docs[0].ID)
	}
	if docs[0].Score() != 0.9 {
		t.Fatalf("expected highest score 0.9, got %f", docs[0].Score())
	}
	if docs[0].Content != "high" {
		t.Fatalf("expected content 'high', got %s", docs[0].Content)
	}
}

func TestRetrieve_SortAndTopK(t *testing.T) {
	// 排序和 TopK 正确
	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {
				docWithScore("d1", "d1", 0.3),
				docWithScore("d2", "d2", 0.8),
			},
			"q1": {
				docWithScore("d3", "d3", 0.5),
			},
			"q2": {
				docWithScore("d4", "d4", 0.9),
			},
		},
	}

	r := newTestRag(cm, dr)
	docs, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          2,
		KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(docs) != 2 {
		t.Fatalf("expected 2 docs (TopK=2), got %d", len(docs))
	}
	if docs[0].ID != "d4" || docs[0].Score() != 0.9 {
		t.Fatalf("first doc should be d4(0.9), got %s(%f)", docs[0].ID, docs[0].Score())
	}
	if docs[1].ID != "d2" || docs[1].Score() != 0.8 {
		t.Fatalf("second doc should be d2(0.8), got %s(%f)", docs[1].ID, docs[1].Score())
	}
}

func TestRetrieve_WorkerError_Propagates(t *testing.T) {
	// worker 失败的错误传播
	workerErr := errors.New("retrieval failed")
	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		err: workerErr,
	}

	r := newTestRag(cm, dr)
	_, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err == nil {
		t.Fatal("expected error from worker")
	}
	if !errors.Is(err, workerErr) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", err)
	}
}

func TestRetrieve_WorkerSentinelError_Identifiable(t *testing.T) {
	// worker 返回 sentinel error，验证 errors.Is 能识别而不是仅识别 context.Canceled
	sentinelErr := errors.New("sentinel error")

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			return nil, sentinelErr
		},
	}

	rag := newTestRag(&fakeChatModel{}, fakeRet)
	_, err := rag.Retrieve(context.Background(), &RetrieveReq{Query: "test", TopK: 5})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinelErr) {
		t.Fatalf("expected errors.Is to recognize sentinel, got: %v", err)
	}
}

func TestRetrieve_PreCancel(t *testing.T) {
	// 预取消：返回 context.Canceled，不调用模型或 retriever
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fakeRet := &fakeDocRetriever{}
	fakeModel := &fakeChatModel{}

	rag := newTestRag(fakeModel, fakeRet)
	_, err := rag.Retrieve(ctx, &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if fakeRet.callCount != 0 {
		t.Fatal("should not call retriever")
	}
	if fakeModel.idx != 0 {
		t.Fatal("should not call model")
	}
}

// docWithScore 辅助函数：创建带分数的 Document
func docWithScore(id, content string, score float64) *schema.Document {
	doc := &schema.Document{
		ID:       id,
		Content:  content,
		MetaData: map[string]any{},
	}
	doc.WithScore(score)
	return doc
}

// waitSig 等待某个信号在超时内到达，避免测试挂起。
func waitSig(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

// assertClosed 断言某个通道已关闭（表示对应任务已完成），不依赖 goroutine 数量统计。
func assertClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatalf("%s was not completed", what)
	}
}

func TestRetrieve_Round1WorkerBlocked_Round2RewriteFails(t *testing.T) {
	// 第二轮 rewrite 失败触发取消；第一轮 worker 先观察取消，再等待独立释放信号。
	// worker 未释放时 Retrieve 不得返回；释放后返回原始错误（errors.Is 可识别）。
	sentinel := errors.New("rewrite sentinel")

	workerStarted := make(chan struct{})
	workerSawCancel := make(chan struct{})
	workerProceed := make(chan struct{}) // 独立释放信号
	retrieveReturned := make(chan struct{})

	var genSeq int32
	var startedOnce sync.Once

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			startedOnce.Do(func() { close(workerStarted) })
			// 先观察 context 取消
			<-ctx.Done()
			close(workerSawCancel)
			// 再等待独立释放信号
			<-workerProceed
			return []*schema.Document{}, nil
		},
	}

	fakeModel := &fakeChatModel{
		onGenerate: func(ctx context.Context, content string) (string, error) {
			n := atomic.AddInt32(&genSeq, 1)
			if n == 2 {
				// 第二轮 Generate 等待第一轮 worker 启动后返回 sentinel error
				waitSig(t, workerStarted, "round-1 worker start")
				return "", sentinel
			}
			return fmt.Sprintf("q%d", n-1), nil
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	var retrieveErr error
	go func() {
		defer close(retrieveReturned)
		_, retrieveErr = rag.Retrieve(context.Background(), &RetrieveReq{
			Query: "test", TopK: 5, KnowledgeName: "kb",
		})
	}()

	// cleanup 保证断言失败时也释放阻塞任务并等待结束，避免泄漏或挂起
	t.Cleanup(func() {
		select {
		case <-workerProceed:
		default:
			close(workerProceed)
		}
		select {
		case <-retrieveReturned:
		case <-time.After(3 * time.Second):
			t.Error("Retrieve did not finish during cleanup")
		}
	})

	// worker 必须观察到由第二轮 rewrite 失败触发的取消
	waitSig(t, workerSawCancel, "round-1 worker observe cancel")

	// worker 未释放时 Retrieve 不得返回
	select {
	case <-retrieveReturned:
		t.Fatal("Retrieve returned before worker was released")
	case <-time.After(200 * time.Millisecond):
	}

	// 释放 worker 后 Retrieve 应正常返回
	close(workerProceed)
	waitSig(t, retrieveReturned, "Retrieve return")

	if retrieveErr == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(retrieveErr, sentinel) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", retrieveErr)
	}
}

func TestRetrieve_WorkerFails_OthersCanceled(t *testing.T) {
	// 第一轮 worker 等待释放失败信号；第二轮 worker 阻塞等待取消；
	// 第三轮 Generate 进入后阻塞等待取消。测试确认二者均进入后再释放第一轮返回 sentinel。
	// 验证：取消被观察、所有已启动 worker 在 Retrieve 返回前完成、errors.Is 识别 sentinel。
	sentinel := errors.New("worker sentinel failure")

	releaseFail := make(chan struct{})        // 释放第一轮 worker 返回 sentinel
	round2Started := make(chan struct{})      // 第二轮 worker 已进入
	round3GenEntered := make(chan struct{})   // 第三轮 Generate 已进入
	round2SawCancel := make(chan struct{})    // 第二轮 worker 观察到取消
	round3GenSawCancel := make(chan struct{}) // 第三轮 Generate 观察到取消
	worker1Done := make(chan struct{})
	worker2Done := make(chan struct{})
	retrieveReturned := make(chan struct{})

	var workerSeq int32
	var genSeq int32

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			switch atomic.AddInt32(&workerSeq, 1) {
			case 1:
				// 第一轮 worker：等待释放信号后返回 sentinel
				defer close(worker1Done)
				<-releaseFail
				return nil, sentinel
			case 2:
				// 第二轮 worker：启动后阻塞并等待 context 取消
				defer close(worker2Done)
				close(round2Started)
				<-ctx.Done()
				close(round2SawCancel)
				return nil, ctx.Err()
			default:
				// 第三轮 worker 不应在取消后启动
				return nil, nil
			}
		},
	}

	fakeModel := &fakeChatModel{
		onGenerate: func(ctx context.Context, content string) (string, error) {
			n := atomic.AddInt32(&genSeq, 1)
			if n == 3 {
				// 第三轮 Generate 发出"已进入"信号后等待 context 取消
				close(round3GenEntered)
				<-ctx.Done()
				close(round3GenSawCancel)
				return "q2", nil
			}
			return fmt.Sprintf("q%d", n-1), nil
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	var retrieveErr error
	go func() {
		defer close(retrieveReturned)
		_, retrieveErr = rag.Retrieve(context.Background(), &RetrieveReq{
			Query: "query1", TopK: 5, KnowledgeName: "kb",
		})
	}()

	t.Cleanup(func() {
		select {
		case <-releaseFail:
		default:
			close(releaseFail)
		}
		select {
		case <-retrieveReturned:
		case <-time.After(3 * time.Second):
			t.Error("Retrieve did not finish during cleanup")
		}
	})

	// 等待第二轮 worker 和第三轮 Generate 均进入后，再释放第一轮让它返回 sentinel error
	waitSig(t, round2Started, "round-2 worker start")
	waitSig(t, round3GenEntered, "round-3 generate enter")
	close(releaseFail)

	// 验证取消被第二轮 worker 与第三轮 Generate 观察到
	waitSig(t, round2SawCancel, "round-2 worker observe cancel")
	waitSig(t, round3GenSawCancel, "round-3 generate observe cancel")

	// Retrieve 必须在所有已启动 worker 完成后返回
	waitSig(t, retrieveReturned, "Retrieve return")
	assertClosed(t, worker1Done, "round-1 worker")
	assertClosed(t, worker2Done, "round-2 worker")

	if !errors.Is(retrieveErr, sentinel) {
		t.Fatalf("expected errors.Is to recognize sentinel, got: %v", retrieveErr)
	}

	// 取消后第三轮 worker 不应启动，已启动 worker 数为 2
	if got := atomic.LoadInt32(&workerSeq); got != 2 {
		t.Fatalf("expected 2 started workers, got %d", got)
	}
}

func TestRetrieve_CancelWinsOverSuccess(t *testing.T) {
	// 下游收到取消后返回成功，验证 Retrieve 仍返回取消错误。
	// 使用 workerStarted 信号触发取消，不使用固定 sleep。
	workerStarted := make(chan struct{})
	retrieveReturned := make(chan struct{})
	var startedOnce sync.Once

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			startedOnce.Do(func() { close(workerStarted) })
			<-ctx.Done()
			// 收到取消后仍返回成功，测试取消优先级
			doc := docWithScore("1", "test", 0.5)
			return []*schema.Document{doc}, nil
		},
	}

	rag := newTestRag(&fakeChatModel{queries: []string{"q0", "q1", "q2"}}, fakeRet)

	ctx, cancel := context.WithCancel(context.Background())

	var retrieveErr error
	go func() {
		defer close(retrieveReturned)
		_, retrieveErr = rag.Retrieve(ctx, &RetrieveReq{
			Query: "test", TopK: 5, KnowledgeName: "kb",
		})
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-retrieveReturned:
		case <-time.After(3 * time.Second):
			t.Error("Retrieve did not finish during cleanup")
		}
	})

	// 有 worker 启动后再取消，替代固定 sleep
	waitSig(t, workerStarted, "worker start")
	cancel()

	waitSig(t, retrieveReturned, "Retrieve return")

	if !errors.Is(retrieveErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", retrieveErr)
	}
}

func TestRetrieve_InFlightCancel(t *testing.T) {
	// 进行中取消：断言 context.Canceled，且已启动 worker 全部结束（用完成信号，不用 goroutine 计数）。
	workerStarted := make(chan struct{})
	retrieveReturned := make(chan struct{})
	var startedOnce sync.Once
	var started, finished int32

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			atomic.AddInt32(&started, 1)
			startedOnce.Do(func() { close(workerStarted) })
			<-ctx.Done()
			atomic.AddInt32(&finished, 1)
			return nil, ctx.Err()
		},
	}

	rag := newTestRag(&fakeChatModel{queries: []string{"q0", "q1", "q2"}}, fakeRet)

	ctx, cancel := context.WithCancel(context.Background())

	var retrieveErr error
	go func() {
		defer close(retrieveReturned)
		_, retrieveErr = rag.Retrieve(ctx, &RetrieveReq{
			Query: "test", TopK: 10, KnowledgeName: "kb",
		})
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-retrieveReturned:
		case <-time.After(3 * time.Second):
			t.Error("Retrieve did not finish during cleanup")
		}
	})

	// 至少一个 worker 启动后取消
	waitSig(t, workerStarted, "worker start")
	cancel()

	waitSig(t, retrieveReturned, "Retrieve return")

	if !errors.Is(retrieveErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", retrieveErr)
	}

	// Retrieve 返回（内部 wg.Wait）后，已启动 worker 必须全部完成
	if s := atomic.LoadInt32(&started); s < 1 {
		t.Fatalf("expected at least 1 started worker, got %d", s)
	}
	if s, f := atomic.LoadInt32(&started), atomic.LoadInt32(&finished); s != f {
		t.Fatalf("started workers (%d) != finished workers (%d)", s, f)
	}
}

func TestRetrieve_Timeout(t *testing.T) {
	// 超时：worker 阻塞直到 deadline，即使随后返回成功，Retrieve 仍应返回 context.DeadlineExceeded。
	retrieveReturned := make(chan struct{})

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			<-ctx.Done()
			// deadline 后仍返回成功，验证超时优先级
			doc := docWithScore("1", "test", 0.5)
			return []*schema.Document{doc}, nil
		},
	}

	rag := newTestRag(&fakeChatModel{queries: []string{"q0", "q1", "q2"}}, fakeRet)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)

	var retrieveErr error
	go func() {
		defer close(retrieveReturned)
		_, retrieveErr = rag.Retrieve(ctx, &RetrieveReq{
			Query: "test", TopK: 10, KnowledgeName: "kb",
		})
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-retrieveReturned:
		case <-time.After(3 * time.Second):
			t.Error("Retrieve did not finish during cleanup")
		}
	})

	waitSig(t, retrieveReturned, "Retrieve return")

	if !errors.Is(retrieveErr, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", retrieveErr)
	}
}

func TestRetrieve_InjectedDepsRemainUnchanged(t *testing.T) {
	// 已注入依赖（docRet / rwModel）在 Retrieve 期间保持不变，运行期只读，不被写回。
	// 本测试只验证注入边界，不声称覆盖 docRet 为 nil 的生产默认路径。
	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	fakeRet := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {{ID: "1", Content: "test"}},
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	if _, err := rag.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	}); err != nil {
		t.Fatal(err)
	}

	// Retrieve 使用局部变量选择 retriever，不回写共享字段
	if rag.docRet != fakeRet {
		t.Fatal("rag.docRet should remain the injected fakeRet, not written back")
	}
	if rag.rwModel != fakeModel {
		t.Fatal("rag.rwModel should remain the injected fakeModel, not written back")
	}
}

func TestRetrieve_ConcurrentDifferentQueries(t *testing.T) {
	// 使用同一个 Rag，验证并发不同 query 时每个请求只收到自己的结果
	fakeModel := &fakeChatModel{
		onGenerate: func(ctx context.Context, content string) (string, error) {
			return "rewritten-" + content, nil
		},
	}

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			doc := &schema.Document{ID: req.optQuery, Content: "test", MetaData: map[string]any{}}
			doc.WithScore(0.5)
			return []*schema.Document{doc}, nil
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	var wg sync.WaitGroup
	results := make([][]*schema.Document, 2)

	for i, query := range []string{"query1", "query2"} {
		wg.Add(1)
		go func(idx int, q string) {
			defer wg.Done()
			res, err := rag.Retrieve(context.Background(), &RetrieveReq{Query: q, TopK: 5})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			results[idx] = res
		}(i, query)
	}

	wg.Wait()

	if len(results[0]) == 0 {
		t.Fatal("request 0 got empty results")
	}
	if !strings.Contains(results[0][0].ID, "query1") || strings.Contains(results[0][0].ID, "query2") {
		t.Fatalf("request 0 ID should contain query1 only: got %q", results[0][0].ID)
	}
	if len(results[1]) == 0 {
		t.Fatal("request 1 got empty results")
	}
	if !strings.Contains(results[1][0].ID, "query2") || strings.Contains(results[1][0].ID, "query1") {
		t.Fatalf("request 1 ID should contain query2 only: got %q", results[1][0].ID)
	}
}

func TestRetrieve_InputUnchanged_WorkerModifiesExcludeIDs(t *testing.T) {
	// worker 修改已有元素 req.excludeIDs[0]（不仅 append），验证每个 worker 收到独立副本、输入快照保持不变
	original := []string{"id1", "id2"}
	snapshot := append([]string(nil), original...)

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			if len(req.excludeIDs) > 0 {
				// 修改前先断言为原始值：每个 worker 都应看到未被其它 worker 污染的副本
				if req.excludeIDs[0] != "id1" {
					t.Errorf("worker copy excludeIDs[0] = %q, want %q", req.excludeIDs[0], "id1")
				}
				req.excludeIDs[0] = "modified"
			}
			return []*schema.Document{docWithScore("1", "test", 0.5)}, nil
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	req := &RetrieveReq{
		Query:      "test",
		TopK:       10,
		excludeIDs: original,
	}

	if _, err := rag.Retrieve(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	// 最后检查输入的独立快照保持不变
	if !reflect.DeepEqual(req.excludeIDs, snapshot) {
		t.Fatalf("input excludeIDs snapshot changed: got %v, want %v", req.excludeIDs, snapshot)
	}
	if req.excludeIDs[0] != "id1" {
		t.Fatalf("input excludeIDs[0] was modified: %q", req.excludeIDs[0])
	}
}

func TestRetrieve_SameScoreSortByID(t *testing.T) {
	// 相同分数按 ID 排序
	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	fakeRet := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {
				docWithScore("d-b", "b", 0.8),
				docWithScore("d-a", "a", 0.8),
			},
			"q1": {
				docWithScore("d-c", "c", 0.8),
			},
			"q2": {},
		},
	}

	rag := newTestRag(fakeModel, fakeRet)
	docs, err := rag.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(docs) != 3 {
		t.Fatalf("expected 3 docs, got %d", len(docs))
	}

	// 相同分数应按 ID 排序
	if docs[0].ID != "d-a" {
		t.Fatalf("first doc should be d-a, got %s", docs[0].ID)
	}
	if docs[1].ID != "d-b" {
		t.Fatalf("second doc should be d-b, got %s", docs[1].ID)
	}
	if docs[2].ID != "d-c" {
		t.Fatalf("third doc should be d-c, got %s", docs[2].ID)
	}
}
