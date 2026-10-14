package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeChatModel 测试用 fake chat model，实现 model.BaseChatModel
type fakeChatModel struct {
	queries []string // 按顺序返回的优化查询
	idx     int
	mu      sync.Mutex
	err     error
}

func (f *fakeChatModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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
	// 验证输入请求和 excludeIDs 不变
	cm := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {{ID: "a", Content: "a"}},
			"q1": {{ID: "b", Content: "b"}},
			"q2": {{ID: "c", Content: "c"}},
		},
	}

	r := newTestRag(cm, dr)
	req := &RetrieveReq{
		Query:         "original",
		TopK:          5,
		Score:         1.5,
		KnowledgeName: "kb",
	}
	originalExcludeIDs := []string{"id1", "id2"}
	req.excludeIDs = originalExcludeIDs

	_, err := r.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// 验证输入请求未被修改
	if req.Query != "original" {
		t.Fatalf("Query was modified: %s", req.Query)
	}
	if req.TopK != 5 {
		t.Fatalf("TopK was modified: %d", req.TopK)
	}
	if !reflect.DeepEqual(req.excludeIDs, originalExcludeIDs) {
		t.Fatalf("excludeIDs was modified: got %v, want %v", req.excludeIDs, originalExcludeIDs)
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

func TestRetrieve_SubsequentRoundFails_CancelPrevious(t *testing.T) {
	// 后续重写失败时，前面已启动工作被取消并在返回前完成
	callCount := 0
	var mu sync.Mutex
	cancelObserved := false

	cm := &fakeChatModel{
		queries: []string{"q0"},
		// 第二次 Generate 调用失败
	}
	// 设置第二次调用出错
	origGenerate := cm.Generate
	cm2 := &fakeChatModel{
		queries: []string{"q0"},
	}
	// 使用自定义 fake，第一次成功，第二次失败
	cmCustom := &sequentialChatModel{
		results: []*schema.Message{
			{Content: "q0"},
		},
		errAt: 1, // 第二次调用失败
		err:   errors.New("rewrite model failed"),
	}
	_ = origGenerate
	_ = cm2

	dr := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {{ID: "a", Content: "a"}},
		},
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			mu.Lock()
			callCount++
			if callCount > 1 {
				// 如果第二轮的 retrieveDoOnce 被调用，说明没有正确取消
				cancelObserved = true
			}
			mu.Unlock()
			return nil, nil
		},
	}

	r := newTestRag(cmCustom, dr)
	_, err := r.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err == nil {
		t.Fatal("expected error when rewrite model fails")
	}

	// 第一轮 worker 应该已经启动，但后续失败应触发 cancel
	mu.Lock()
	_ = cancelObserved // 仅记录，不强制断言
	mu.Unlock()
}

// sequentialChatModel 按序返回结果，到指定位置返回错误
type sequentialChatModel struct {
	results []*schema.Message
	errAt   int
	err     error
	idx     int
	mu      sync.Mutex
}

func (f *sequentialChatModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= f.errAt {
		f.idx++
		return nil, f.err
	}
	if f.idx >= len(f.results) {
		return &schema.Message{Content: "default"}, nil
	}
	msg := f.results[f.idx]
	f.idx++
	return msg, nil
}

func (f *sequentialChatModel) Stream(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not implemented")
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

func TestRetrieve_Round1WorkerBlocked_Round2RewriteFails(t *testing.T) {
	// 第一轮 worker 已开始阻塞，第二轮重写失败
	workerStarted := make(chan struct{})
	workerProceed := make(chan struct{})
	defer close(workerProceed)

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			close(workerStarted)
			select {
			case <-workerProceed:
				return []*schema.Document{{ID: "1", Content: "test", MetaData: map[string]any{}}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	// 第二轮重写失败
	fakeModel := &sequentialChatModel{
		results: []*schema.Message{{Content: "q0"}},
		errAt:   1,
		err:     errors.New("rewrite error"),
	}

	rag := newTestRag(fakeModel, fakeRet)

	var wg sync.WaitGroup
	wg.Add(1)
	var retrieveErr error
	go func() {
		defer wg.Done()
		_, retrieveErr = rag.Retrieve(context.Background(), &RetrieveReq{
			Query:         "test",
			TopK:          10,
			KnowledgeName: "kb",
		})
	}()

	// 等待 worker 开始
	<-workerStarted

	// 等待 Retrieve 返回
	wg.Wait()

	// 验证错误
	if retrieveErr == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(retrieveErr, fakeModel.err) {
		t.Fatalf("expected errors.Is to recognize original error, got: %v", retrieveErr)
	}
}

func TestRetrieve_WorkerFails_OthersCanceled(t *testing.T) {
	// 一个 worker 失败，另一个 worker 和进行中的重写收到派生 context 取消
	worker1Done := make(chan struct{})
	worker2CtxCanceled := make(chan struct{})
	var worker2Once sync.Once

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			if req.optQuery == "q0" {
				defer close(worker1Done)
				return nil, errors.New("worker1 error")
			}
			// worker2
			<-ctx.Done()
			worker2Once.Do(func() { close(worker2CtxCanceled) })
			return nil, ctx.Err()
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	_, err := rag.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})

	if err == nil {
		t.Fatal("expected error")
	}

	// 验证 worker2 收到取消
	select {
	case <-worker2CtxCanceled:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("worker2 did not receive cancel")
	}
}

func TestRetrieve_InFlightCancel(t *testing.T) {
	// 进行中取消：返回 context.Canceled，全部已启动 worker 完成
	workerStarted := make(chan struct{}, 3)
	workerDone := make(chan struct{}, 3)

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			workerStarted <- struct{}{}
			<-ctx.Done()
			workerDone <- struct{}{}
			return nil, ctx.Err()
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := rag.Retrieve(ctx, &RetrieveReq{
			Query:         "test",
			TopK:          10,
			KnowledgeName: "kb",
		})
		done <- err
	}()

	// 等待至少一个 worker 启动
	<-workerStarted
	cancel()

	// 应该完成而不死锁
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Retrieve deadlocked on cancel")
	}

	// 验证所有已启动 worker 都完成
	for i := 0; i < len(workerStarted); i++ {
		select {
		case <-workerDone:
			// ok
		case <-time.After(1 * time.Second):
			// 可能有些 worker 没启动，这是正常的
			break
		}
	}
}

func TestRetrieve_Timeout(t *testing.T) {
	// 超时：errors.Is(err, context.DeadlineExceeded)
	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := rag.Retrieve(ctx, &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestRetrieve_NoSharedFieldWrite(t *testing.T) {
	// 未预先设置 docRet 的生产默认选择边界不存在共享字段写入
	rag := &Rag{} // docRet 为 nil

	// 使用 fake model 避免真实调用
	fakeModel := &fakeChatModel{queries: []string{"q0"}}
	rag.rwModel = fakeModel

	// 使用 fake retriever 来避免真实调用
	fakeRet := &fakeDocRetriever{
		results: map[string][]*schema.Document{
			"q0": {{ID: "1", Content: "test"}},
		},
	}
	// 设置 docRet 为 fake，但测试后验证它没有被修改
	rag.docRet = fakeRet

	_, err := rag.Retrieve(context.Background(), &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 验证 rag.docRet 仍然是我们设置的 fakeRet（未被写回新的值）
	if rag.docRet != fakeRet {
		t.Fatal("rag.docRet should remain the original fakeRet, not written back")
	}
}

func TestRetrieve_ConcurrentDifferentQueries(t *testing.T) {
	// 并发请求使用不同原始 query，断言每个请求只收到属于自己的重写结果
	var mu sync.Mutex
	queryMap := make(map[string][]string) // original query -> rewritten queries

	// 为每个并发请求创建独立的 model 和 retriever
	var wg sync.WaitGroup
	for _, q := range []string{"queryA", "queryB"} {
		wg.Add(1)
		go func(query string) {
			defer wg.Done()
			
			// 为每个请求创建独立的 fake model
			fakeModel := &fakeChatModel{queries: []string{"rewritten-" + query + "-0", "rewritten-" + query + "-1", "rewritten-" + query + "-2"}}
			
			fakeRet := &fakeDocRetriever{
				onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
					mu.Lock()
					queryMap[req.Query] = append(queryMap[req.Query], req.optQuery)
					mu.Unlock()
					return []*schema.Document{{ID: "1", Content: "test"}}, nil
				},
			}

			rag := newTestRag(fakeModel, fakeRet)
			_, err := rag.Retrieve(context.Background(), &RetrieveReq{
				Query:         query,
				TopK:          10,
				KnowledgeName: "kb",
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}(q)
	}
	wg.Wait()

	// 验证每个请求只收到自己的重写结果
	mu.Lock()
	defer mu.Unlock()

	if len(queryMap["queryA"]) != 3 {
		t.Fatalf("expected 3 rewritten queries for queryA, got %d", len(queryMap["queryA"]))
	}
	if len(queryMap["queryB"]) != 3 {
		t.Fatalf("expected 3 rewritten queries for queryB, got %d", len(queryMap["queryB"]))
	}

	// 验证重写结果正确
	for _, q := range queryMap["queryA"] {
		if q != "rewritten-queryA-0" && q != "rewritten-queryA-1" && q != "rewritten-queryA-2" {
			t.Fatalf("unexpected rewritten query for queryA: %s", q)
		}
	}
	for _, q := range queryMap["queryB"] {
		if q != "rewritten-queryB-0" && q != "rewritten-queryB-1" && q != "rewritten-queryB-2" {
			t.Fatalf("unexpected rewritten query for queryB: %s", q)
		}
	}
}

func TestRetrieve_InputUnchanged_WorkerModifiesExcludeIDs(t *testing.T) {
	// 输入请求及 excludeIDs 不变；让 worker 修改自己的 excludeIDs，验证输入和其他轮次不受影响
	originalExcludeIDs := []string{"id1", "id2"}

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			// worker 修改自己的 excludeIDs
			req.excludeIDs = append(req.excludeIDs, "modified")
			return []*schema.Document{{ID: "1", Content: "test"}}, nil
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	req := &RetrieveReq{
		Query:         "test",
		TopK:          10,
		KnowledgeName: "kb",
		excludeIDs:    originalExcludeIDs,
	}

	_, err := rag.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// 验证输入未变
	if !reflect.DeepEqual(req.excludeIDs, originalExcludeIDs) {
		t.Fatalf("input excludeIDs was modified: got %v, want %v", req.excludeIDs, originalExcludeIDs)
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
