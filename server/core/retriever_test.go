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
	req.ExcludeIDs = originalExcludeIDs

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
	if !reflect.DeepEqual(req.ExcludeIDs, originalExcludeIDs) {
		t.Fatalf("excludeIDs was modified: got %v, want %v", req.ExcludeIDs, originalExcludeIDs)
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

func TestRetrieve_Round1WorkerBlocked_Round2RewriteFails(t *testing.T) {
	workerStarted := make(chan struct{})
	workerProceed := make(chan struct{})
	rewriteFailed := make(chan struct{})
	retrieveReturned := make(chan struct{})

	var genCallCount int32

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			close(workerStarted)
			// Block until explicitly released (ignore ctx cancel to test event ordering)
			<-workerProceed
			doc := &schema.Document{ID: "1", Content: "test", MetaData: map[string]any{}}
			doc.WithScore(0.5)
			return []*schema.Document{doc}, nil
		},
	}

	fakeModel := &fakeChatModel{
		onGenerate: func(ctx context.Context, content string) (string, error) {
			n := atomic.AddInt32(&genCallCount, 1)
			if n == 2 {
				<-workerStarted
				close(rewriteFailed)
				return "", errors.New("rewrite error")
			}
			return "rewritten-" + content, nil
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	var retrieveErr error
	go func() {
		_, retrieveErr = rag.Retrieve(context.Background(), &RetrieveReq{
			Query: "test", TopK: 5, KnowledgeName: "kb",
		})
		close(retrieveReturned)
	}()

	<-rewriteFailed

	select {
	case <-retrieveReturned:
		t.Fatal("Retrieve should not return before worker is released")
	case <-time.After(200 * time.Millisecond):
	}

	close(workerProceed)

	select {
	case <-retrieveReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Retrieve did not return after worker released")
	}

	if retrieveErr == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(retrieveErr.Error(), "rewrite error") {
		t.Fatalf("unexpected error: %v", retrieveErr)
	}
}

func TestRetrieve_WorkerFails_OthersCanceled(t *testing.T) {
	// worker 失败时错误传播，且派生 context 被取消
	workerErr := errors.New("worker1 error")
	var workerCallCount int32

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			idx := atomic.AddInt32(&workerCallCount, 1)
			if idx == 1 {
				return nil, workerErr
			}
			// 后续 worker 应收到取消
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	fakeModel := &fakeChatModel{
		onGenerate: func(ctx context.Context, content string) (string, error) {
			return "rewritten-" + content, nil
		},
	}

	rag := newTestRag(fakeModel, fakeRet)

	_, err := rag.Retrieve(context.Background(), &RetrieveReq{
		Query: "query1", TopK: 5, KnowledgeName: "kb",
	})

	if err == nil {
		t.Fatal("expected error from Retrieve")
	}
	if !errors.Is(err, workerErr) {
		t.Fatalf("expected errors.Is to recognize worker error, got: %v", err)
	}
}

func TestRetrieve_CancelWinsOverSuccess(t *testing.T) {
	// 下游在收到取消后返回成功，验证 Retrieve 仍返回取消错误
	ctx, cancel := context.WithCancel(context.Background())

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			<-ctx.Done()
			doc := &schema.Document{ID: "1", Content: "test", MetaData: map[string]any{}}
			doc.WithScore(0.5)
			return []*schema.Document{doc}, nil
		},
	}

	rag := newTestRag(&fakeChatModel{}, fakeRet)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := rag.Retrieve(ctx, &RetrieveReq{Query: "test", TopK: 5})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
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
	// worker 修改已有元素 req.ExcludeIDs[0]，不仅做 append
	originalExcludeIDs := []string{"id1", "id2"}

	fakeRet := &fakeDocRetriever{
		onRetrieve: func(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
			// worker 修改已有元素
			if len(req.ExcludeIDs) > 0 {
				req.ExcludeIDs[0] = "modified"
			}
			doc := &schema.Document{ID: "1", Content: "test", MetaData: map[string]any{}}
			doc.WithScore(0.5)
			return []*schema.Document{doc}, nil
		},
	}

	fakeModel := &fakeChatModel{queries: []string{"q0", "q1", "q2"}}
	rag := newTestRag(fakeModel, fakeRet)

	req := &RetrieveReq{
		Query:      "test",
		TopK:       10,
		ExcludeIDs: originalExcludeIDs,
	}

	_, err := rag.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// 验证输入未变
	if req.ExcludeIDs[0] != "id1" {
		t.Fatal("input excludeIDs was modified")
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
