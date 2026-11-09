package core

// A-4 / P-8 组件级（L2）验证入口之二：core 包生产共享实现。
//
// 与 core/retriever/a4_component_entry_test.go 的分工：
//   - 那一份覆盖「生产 newRetriever + es8 组件」对响应/错误/取消/响应体的处理；
//   - 本文件覆盖 core 包自身的生产代码：retriever.BuildRetriever（生产图构建）
//     与 Rag.retrieve（生产 filter 构造、esTopK、score-1 归一化、错误透传）。
//
// 目标边界：
//   - 只连 httptest 本机假 ES；embedding 用 testutil 的本机桩，不调用真实模型。
//   - 本文件没有任何真实目标开关，也不读取环境变量。
//
// 结论边界：
//   - 不经过 Rag.Retrieve 的 checkFingerprint / VerifyFingerprint / schema 门禁；
//   - 不经过 retrieveDoOnce 的双路合并、BM25、RRF 与 rerank；
//   - 缺字段时 ES 是否返回 400 由 L0 直查样本回答
//     （verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md）；
//   - 不能写成应用全链（L3）验证，也不能判定 A-4 或 Stage 2D 通过。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/wangle201210/go-rag/server/core/config"
	coreretriever "github.com/wangle201210/go-rag/server/core/retriever"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/testutil"
)

const (
	a4pIndexName = "a4-core-index"
	// a4pDims 与 L0 样本一致：1024 维。
	a4pDims    = 1024
	a4pHitBody = `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},` +
		`"hits":{"total":{"value":1,"relation":"eq"},"max_score":1.42,"hits":[{"_index":"a4-core-index",` +
		`"_id":"doc-1","_score":1.42,"_source":{"content":"hello","content_vector":[0.1,0.2],` +
		`"_knowledge_name":"kb-a4","ext":""}}]}}`
	a4pESErrorBody = `{"error":{"root_cause":[{"type":"script_exception","reason":"hidden"}],` +
		`"type":"search_phase_execution_exception","reason":"hidden"},"status":400}`
)

// a4pObs 记录假 ES 收到的请求路径与请求体。
type a4pObs struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
}

func (o *a4pObs) add(path string, body map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.paths = append(o.paths, path)
	o.bodies = append(o.bodies, body)
}

func (o *a4pObs) snapshot() ([]string, []map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.paths...), append([]map[string]any(nil), o.bodies...)
}

func a4pNewFake(t *testing.T, status int, body string) (*a4pObs, string) {
	t.Helper()
	obs := &a4pObs{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		obs.add(r.URL.Path, got)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return obs, srv.URL
}

// a4pBuild 用生产 retriever.BuildRetriever 构造 Rag 的两条检索路。
// conf 中只有假 ES 与假 embedding 地址；Client 非 nil 使 retrieve 走 ES 分支。
func a4pBuild(t *testing.T, target string, dims int) *Rag {
	t.Helper()
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{target}})
	if err != nil {
		t.Fatalf("new es client: %v", err)
	}
	emb := testutil.Embedding(t, dims)
	conf := &config.Config{
		Client:              client,
		IndexName:           a4pIndexName,
		APIKey:              "offline",
		BaseURL:             emb.URL,
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: dims,
	}
	content, err := coreretriever.BuildRetriever(context.Background(), conf)
	if err != nil {
		t.Fatalf("BuildRetriever(content): %v", err)
	}
	qaCtx := context.WithValue(context.Background(), coretypes.RetrieverFieldKey, coretypes.FieldQAContentVector)
	qa, err := coreretriever.BuildRetriever(qaCtx, conf)
	if err != nil {
		t.Fatalf("BuildRetriever(qa): %v", err)
	}
	return &Rag{rtrvr: content, qaRtrvr: qa, conf: conf}
}

// a4pScriptAndFilter 取出生产请求的 script_score 形状。
func a4pScriptAndFilter(t *testing.T, body map[string]any) (string, map[string]any) {
	t.Helper()
	scriptScore, ok := body["query"].(map[string]any)["script_score"].(map[string]any)
	if !ok {
		t.Fatalf("query.script_score missing: %v", body["query"])
	}
	script, _ := scriptScore["script"].(map[string]any)["source"].(string)
	filters, ok := scriptScore["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	if !ok || len(filters) != 1 {
		t.Fatalf("script_score.query.bool.filter = %v, want one filter", scriptScore["query"])
	}
	inner, ok := filters[0].(map[string]any)["bool"].(map[string]any)
	if !ok {
		t.Fatalf("filter[0].bool missing: %v", filters[0])
	}
	return script, inner
}

// a4pSentVector 取出实际发出的查询向量（script_score.script.params.embedding）。
func a4pSentVector(t *testing.T, body map[string]any) []float64 {
	t.Helper()
	script, ok := body["query"].(map[string]any)["script_score"].(map[string]any)["script"].(map[string]any)
	if !ok {
		t.Fatalf("script_score.script missing: %v", body["query"])
	}
	raw, ok := script["params"].(map[string]any)["embedding"].([]any)
	if !ok {
		t.Fatalf("script.params.embedding missing: %v", script)
	}
	vec := make([]float64, len(raw))
	for i, v := range raw {
		f, ok := v.(float64)
		if !ok {
			t.Fatalf("embedding[%d] is %T", i, v)
		}
		vec[i] = f
	}
	return vec
}

// TestA4ComponentRag_RequestShapeAndScoreNorm 覆盖生产 Rag.retrieve：
// esTopK=50、_knowledge_name 过滤、excludeIDs 的 must_not、score>1 减 1。
func TestA4ComponentRag_RequestShapeAndScoreNorm(t *testing.T) {
	cases := []struct {
		name     string
		qa       bool
		exclude  []string
		wantFld  string
		wantMust bool
	}{
		{name: "content with exclude", qa: false, exclude: []string{"x1", "x2"}, wantFld: "content_vector", wantMust: true},
		{name: "content without exclude", qa: false, wantFld: "content_vector"},
		{name: "qa path uses qa_content_vector", qa: true, wantFld: "qa_content_vector"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, target := a4pNewFake(t, http.StatusOK, a4pHitBody)
			rag := a4pBuild(t, target, a4pDims)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req := &RetrieveReq{
				Query: "q", optQuery: "q", TopK: 5,
				KnowledgeName: "kb-a4", excludeIDs: tc.exclude,
			}
			docs, err := rag.retrieve(ctx, req, tc.qa)
			if err != nil {
				t.Fatalf("retrieve: %v", err)
			}
			if len(docs) != 1 || docs[0].ID != "doc-1" {
				t.Fatalf("docs = %d, want 1 doc-1", len(docs))
			}
			// 生产 retrieve 对 score>1 减 1；组件层 newRetriever 不做该归一化。
			if got := docs[0].Score(); math.Abs(got-0.42) > 1e-9 {
				t.Fatalf("score = %v, want 1.42-1 = 0.42", got)
			}

			paths, bodies := obs.snapshot()
			if len(paths) != 1 || paths[0] != "/"+a4pIndexName+"/_search" {
				t.Fatalf("paths = %v, want one /%s/_search", paths, a4pIndexName)
			}
			if size, ok := bodies[0]["size"].(float64); !ok || int(size) != esTopK {
				t.Fatalf("size = %v, want esTopK=%d", bodies[0]["size"], esTopK)
			}
			script, inner := a4pScriptAndFilter(t, bodies[0])
			if want := "cosineSimilarity(params.embedding, '" + tc.wantFld + "') + 1.0"; script != want {
				t.Fatalf("script = %q, want %q", script, want)
			}
			must, _ := inner["must"].([]any)
			if len(must) != 1 {
				t.Fatalf("must = %v, want one knowledge filter", inner["must"])
			}
			match, _ := must[0].(map[string]any)["match"].(map[string]any)
			kn, _ := match[coretypes.KnowledgeName].(map[string]any)
			if kn["query"] != "kb-a4" {
				t.Fatalf("knowledge filter = %v, want %s=kb-a4", match, coretypes.KnowledgeName)
			}
			mustNot, hasMustNot := inner["must_not"].([]any)
			if tc.wantMust {
				if !hasMustNot || len(mustNot) != 1 {
					t.Fatalf("must_not = %v, want one terms _id", inner["must_not"])
				}
				terms, _ := mustNot[0].(map[string]any)["terms"].(map[string]any)["_id"].([]any)
				if len(terms) != 2 || terms[0] != "x1" || terms[1] != "x2" {
					t.Fatalf("terms _id = %v, want [x1 x2]", terms)
				}
			} else if hasMustNot {
				t.Fatalf("unexpected must_not: %v", inner["must_not"])
			}
			// 生产请求不带 _source 过滤（与 L0 直查的 _source:false 形状不同）。
			if _, ok := bodies[0]["_source"]; ok {
				t.Fatalf("unexpected _source filter: %v", bodies[0]["_source"])
			}
			// 实际发出的向量：1024 维非零 one-hot（与 L0 合成向量一致，非全零）。
			vec := a4pSentVector(t, bodies[0])
			want := testutil.OneHotVector(a4pDims, testutil.EmbeddingHotIndex)
			if len(vec) != len(want) {
				t.Fatalf("sent embedding len = %d, want %d", len(vec), len(want))
			}
			nonZero := 0
			for i := range want {
				if vec[i] != want[i] {
					t.Fatalf("embedding[%d] = %v, want %v (one-hot at %d)", i, vec[i], want[i], testutil.EmbeddingHotIndex)
				}
				if vec[i] != 0 {
					nonZero++
				}
			}
			if nonZero != 1 {
				t.Fatalf("embedding has %d non-zero components, want 1 (non-zero one-hot)", nonZero)
			}
		})
	}
}

// TestA4ComponentRag_ErrorPropagated 生产 Rag.retrieve 不吞 ES 4xx。
func TestA4ComponentRag_ErrorPropagated(t *testing.T) {
	obs, target := a4pNewFake(t, http.StatusBadRequest, a4pESErrorBody)
	rag := a4pBuild(t, target, a4pDims)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	docs, err := rag.retrieve(ctx, &RetrieveReq{Query: "q", optQuery: "q", TopK: 5, KnowledgeName: "kb-a4"}, true)
	if err == nil {
		t.Fatalf("expected error, got %d docs", len(docs))
	}
	if len(docs) != 0 {
		t.Fatalf("expected no docs on error, got %d", len(docs))
	}
	var esErr *types.ElasticsearchError
	if !errors.As(err, &esErr) {
		t.Fatalf("error is not *types.ElasticsearchError: %T %v", err, err)
	}
	if esErr.Status != http.StatusBadRequest || esErr.ErrorCause.Type != "search_phase_execution_exception" {
		t.Fatalf("unexpected ES error: status=%d type=%q", esErr.Status, esErr.ErrorCause.Type)
	}
	if paths, _ := obs.snapshot(); len(paths) != 1 {
		t.Fatalf("requests = %d, want 1 (no retry)", len(paths))
	}
}

// TestA4ComponentRag_CanceledContext 已取消的 ctx 不会发出检索请求。
func TestA4ComponentRag_CanceledContext(t *testing.T) {
	obs, target := a4pNewFake(t, http.StatusOK, a4pHitBody)
	rag := a4pBuild(t, target, a4pDims)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	docs, err := rag.retrieve(ctx, &RetrieveReq{Query: "q", optQuery: "q", TopK: 5, KnowledgeName: "kb-a4"}, false)
	if err == nil {
		t.Fatalf("expected cancellation error, got %d docs", len(docs))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error does not wrap context.Canceled: %v", err)
	}
	if paths, _ := obs.snapshot(); len(paths) != 0 {
		t.Fatalf("requests = %d, want 0 after cancellation", len(paths))
	}
}

// 编译期确认生产签名未漂移（本文件只做断言，不引入新的生产入口）。
var _ = func(ctx context.Context, conf *config.Config) (any, error) {
	return coreretriever.BuildRetriever(ctx, conf)
}
