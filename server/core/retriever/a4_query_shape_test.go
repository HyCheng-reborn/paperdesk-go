package retriever

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/retriever/es8"
	er "github.com/cloudwego/eino/components/retriever"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/testutil"
)

// A-4 / P-8 离线骨架（design/stage2d-a4-es-plan.md v0.1）。
//
// 本文件只证明“项目发出的请求形状、错误传播与响应解析”，全部目标为 httptest 本机
// 假 ES；它不证明真实 Elasticsearch 在字段缺失/文档无值时的行为，也不接入真实集群。
// 任何后续真实目标工具必须默认拒绝非回环地址（见 a4RequireLoopback）。

// a4ExpectedQAScript 是 es8 search_mode 在 cosine 模式下的脚本文本（字段名为 qa_content_vector）。
const a4ExpectedQAScript = "cosineSimilarity(params.embedding, 'qa_content_vector') + 1.0"

// a4RequireLoopback 断言目标只指向回环地址。后续若把本骨架改成可配目标，
// 必须先过此检查，避免默认连到非本机集群。
func a4RequireLoopback(t *testing.T, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse target url: %v", err)
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		t.Fatalf("non-loopback target refused: %q", u.Hostname())
	}
}

// a4NewFakeES 启动只返回固定响应的本机假 ES，并记录最后一次请求。
func a4NewFakeES(t *testing.T, status int, body string) (*elasticsearch.Client, func() (string, map[string]any)) {
	t.Helper()
	var (
		mu     sync.Mutex
		path   string
		parsed map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Lock()
		path, parsed = r.URL.Path, got
		mu.Unlock()
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	a4RequireLoopback(t, srv.URL)
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{srv.URL}})
	if err != nil {
		t.Fatalf("new es client: %v", err)
	}
	return client, func() (string, map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		return path, parsed
	}
}

// a4NewRetriever 用生产 newRetriever 构造 QA 字段检索器；嵌入由 testutil 本机桩提供。
func a4NewRetriever(t *testing.T, client *elasticsearch.Client, dims int) er.Retriever {
	t.Helper()
	emb := testutil.Embedding(t, dims)
	t.Cleanup(emb.Close)
	conf := &config.Config{
		Client:              client,
		IndexName:           "a4-offline-index",
		APIKey:              "offline",
		BaseURL:             emb.URL,
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: dims,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ctx = context.WithValue(ctx, coretypes.RetrieverFieldKey, coretypes.FieldQAContentVector)
	ret, err := newRetriever(ctx, conf)
	if err != nil {
		t.Fatalf("new retriever: %v", err)
	}
	return ret
}

func a4EmptyHits(t *testing.T, extra string) string {
	t.Helper()
	base := `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
	if extra == "" {
		return base
	}
	return strings.Replace(base, `"took":1,`, extra+`"took":1,`, 1)
}

func a4QAFieldFilter() []types.Query {
	return []types.Query{{
		Bool: &types.BoolQuery{Must: []types.Query{{
			Match: map[string]types.MatchQuery{coretypes.KnowledgeName: {Query: "a4-kb"}},
		}}},
	}}
}

// TestA4_QAFieldRequestShape 固定“项目实际发出的 QA 向量查询”形状：
// POST /<index>/_search，script_score + cosineSimilarity(params.embedding,'qa_content_vector')，
// size=TopK，过滤条件落在 bool.filter 内且使用 _knowledge_name。
func TestA4_QAFieldRequestShape(t *testing.T) {
	client, last := a4NewFakeES(t, http.StatusOK, a4EmptyHits(t, ""))
	ret := a4NewRetriever(t, client, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ret.Retrieve(ctx, "a4-query", er.WithTopK(50), es8.WithFilters(a4QAFieldFilter())); err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	path, body := last()
	if path != "/a4-offline-index/_search" {
		t.Fatalf("index path = %q, want /a4-offline-index/_search", path)
	}
	scriptScore, ok := body["query"].(map[string]any)["script_score"].(map[string]any)
	if !ok {
		t.Fatalf("query is not script_score: %v", body["query"])
	}
	script := scriptScore["script"].(map[string]any)
	if got := script["source"]; got != a4ExpectedQAScript {
		t.Fatalf("script source = %v, want %q", got, a4ExpectedQAScript)
	}
	vec, ok := script["params"].(map[string]any)["embedding"].([]any)
	if !ok || len(vec) != 1024 {
		t.Fatalf("embedding param = %T len=%d, want 1024", script["params"].(map[string]any)["embedding"], len(vec))
	}
	if size, ok := body["size"].(float64); !ok || int(size) != 50 {
		t.Fatalf("size = %v, want 50", body["size"])
	}
	// 生产把 match 过滤放进 bool.filter；字段名是 _knowledge_name。
	filter := scriptScore["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	match := filter[0].(map[string]any)["bool"].(map[string]any)["must"].([]any)[0].(map[string]any)["match"].(map[string]any)
	kn, ok := match[coretypes.KnowledgeName].(map[string]any)
	if !ok || kn["query"] != "a4-kb" {
		t.Fatalf("knowledge filter = %v, want field %q = a4-kb", match, coretypes.KnowledgeName)
	}
}

// TestA4_ESErrorIsPropagated 证明 ES 返回 400 时项目检索器把错误返回给调用方，
// 不把它变成“0 条命中”的静默成功。真实 ES 是否在字段缺失时返回该错误属待验证项。
func TestA4_ESErrorIsPropagated(t *testing.T) {
	body := `{"error":{"root_cause":[{"type":"search_phase_execution_exception","reason":"all shards failed"}],"type":"search_phase_execution_exception","reason":"all shards failed"},"status":400}`
	client, _ := a4NewFakeES(t, http.StatusBadRequest, body)
	ret := a4NewRetriever(t, client, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	docs, err := ret.Retrieve(ctx, "a4-query", er.WithTopK(50), es8.WithFilters(a4QAFieldFilter()))
	if err == nil {
		t.Fatalf("expected error, got %d docs", len(docs))
	}
	if len(docs) != 0 {
		t.Fatalf("expected no docs on error, got %d", len(docs))
	}
	if !strings.Contains(err.Error(), "search_phase_execution_exception") {
		t.Fatalf("error does not carry ES error type: %v", err)
	}
}

// TestA4_PartialShardFailureNotSurfaced 记录现状（不是期望行为）：项目不读取
// _shards.failed / timed_out，因此“部分分片失败 + 0 命中”与“正常 0 命中”不可区分。
// A-4 判定必须依据原始 HTTP 响应，而不能只看项目返回的文档数。
func TestA4_PartialShardFailureNotSurfaced(t *testing.T) {
	body := `{"took":3,"timed_out":true,"_shards":{"total":2,"successful":1,"skipped":0,"failed":1},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
	client, _ := a4NewFakeES(t, http.StatusOK, body)
	ret := a4NewRetriever(t, client, 1024)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	docs, err := ret.Retrieve(ctx, "a4-query", er.WithTopK(50), es8.WithFilters(a4QAFieldFilter()))
	if err != nil {
		t.Fatalf("current behavior returns nil error, got %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(docs))
	}
}

// TestA4_HitParsing 固定响应侧解析规则：_source 缺少 qa_content_vector 不影响解析；
// 出现白名单之外的字段则整体报错（真实索引若带额外字段会先于 A-4 结论失败）。
func TestA4_HitParsing(t *testing.T) {
	id := "doc-1"
	score := types.Float64(1)
	full := types.Hit{
		Id_:     &id,
		Score_:  &score,
		Source_: json.RawMessage(`{"content":"hello","content_vector":[0.1,0.2],"_knowledge_name":"a4-kb","ext":""}`),
	}
	doc, err := EsHit2Document(context.Background(), full)
	if err != nil {
		t.Fatalf("parse hit without qa_content_vector: %v", err)
	}
	if doc.Content != "hello" || doc.ID != "doc-1" || doc.Score() != 1 {
		t.Fatalf("unexpected doc: id=%q content=%q score=%v", doc.ID, doc.Content, doc.Score())
	}

	unexpected := types.Hit{
		Id_:     &id,
		Source_: json.RawMessage(`{"content":"hello","title":"extra"}`),
	}
	if _, err := EsHit2Document(context.Background(), unexpected); err == nil {
		t.Fatal("expected error for unexpected source field")
	} else if !strings.Contains(err.Error(), "unexpected field=title") {
		t.Fatalf("unexpected error text: %v", err)
	}
}
