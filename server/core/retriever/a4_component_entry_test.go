package retriever

// A-4 / P-8 组件级（L2）验证入口（design/stage2d-a4-l2-component.md）。
//
// 本文件的检索器一律由生产 newRetriever 构造：es8 组件、cosine search_mode、
// EsHit2Document 解析器、go-elasticsearch v8 客户端均为生产依赖；embedding 使用
// 本机 httptest 桩（1024 维非零 one-hot 合成向量），不会调用真实模型。
//
// 目标边界：
//   - 默认只启动 httptest 本机假 ES；真实 ES 分支在
//     a4_component_real_branch_test.go，默认 skip，需独立开关 + 授权短语。
//
// 结论边界：
//   - 本入口不经过 core.Rag 的 fingerprint/schema 门禁（checkFingerprint /
//     VerifyFingerprint / ValidateSchemaStrict），也不经过 retrieveDoOnce 的双路
//     合并、RRF 与 rerank。它只说明"生产检索器组件在给定响应下的行为"。
//   - 缺字段时 ES 是否返回 400 由 L0 直查样本回答（
//     verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md），不由本文件回答。
//   - 本入口不能写成应用全链（L3）验证，也不能判定 Stage 2D 或 A-4 通过。
//
// 独立复核修复（2026-10-31）：
//   - 请求记录/上限/只读白名单下沉到发送传输层 a4cSender，在真正发送前判定；
//   - elasticsearch.Config.DisableRetry=true（默认会对 502/503/504 重试 3 次），
//     且传输层对 3xx 直接拒绝，不跟随重定向；
//   - fake embedding 改为 1024 维非零 one-hot，并断言实际发出的向量。
//
// 独立复核修复（第二轮，错误分类/向量对齐）：
//   - 合成向量对齐 L0：e0 = [1,0,…,0]（1024 维，非零下标 0），仍断言真实发出的向量；
//   - 真实分支的 4xx 只按声明表放行（见 a4_component_real_branch_test.go）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/retriever/es8"
	er "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/wangle201210/go-rag/server/core/config"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

const (
	a4cIndexName  = "a4-component-index"
	a4cContentFld = coretypes.FieldContentVector
	// a4cDims 与 L0 样本一致：1024 维。
	a4cDims = 1024
	// a4cHotIndex 是 one-hot 非零分量下标，与 L0 具体合成向量对齐（下标 0）：
	// L0 只留下“1024 维 one-hot”的文字记录，未留存向量字节；对齐依据是
	// 复核报告指出的“L0 非零下标 0”。具体值为 e0 = [1,0,…,0]，全零向量在
	// cosine 脚本下退化，因此必须非零。
	a4cHotIndex = 0
	// a4cMaxRequests = GET <index> 1 + GET _mapping/field 1 + POST _search 2。
	a4cMaxRequests = 4
	// a4cMappingFields 一次只读请求同时核验两个向量字段。
	a4cMappingFields   = "content_vector,qa_content_vector"
	a4cMaxRequestBody  = 1 << 20
	a4cContentScript   = "cosineSimilarity(params.embedding, 'content_vector') + 1.0"
	a4cQAScriptSnippet = "'qa_content_vector'"
)

// a4cOneHotVector 返回 dims 维 one-hot：仅 hot 处为 1.0，其余为 0。
func a4cOneHotVector(dims, hot int) []float64 {
	v := make([]float64, dims)
	if hot >= 0 && hot < dims {
		v[hot] = 1
	}
	return v
}

// a4cSent 是一次“已被传输层放行并尝试发送”的请求记录。
type a4cSent struct {
	Kind   string // meta / mapping / search
	Method string
	Path   string
	Status int    // 0 表示未拿到响应（传输错误/取消）
	Err    string // 脱敏后的错误种类，不含 URL/凭据
}

// a4Obs 是并发安全的请求观测：sent 在发送前登记，refused 记录被拒绝的请求。
type a4Obs struct {
	mu      sync.Mutex
	sent    []a4cSent
	refused []string
	bodies  []map[string]any
}

func (o *a4Obs) begin(kind, method, path string, max int) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if max <= 0 {
		max = 1
	}
	if len(o.sent) >= max {
		o.refused = append(o.refused, "budget "+method+" "+path)
		return -1, fmt.Errorf("a4c: request budget exhausted (max=%d), %s %s not sent", max, method, path)
	}
	o.sent = append(o.sent, a4cSent{Kind: kind, Method: method, Path: path})
	return len(o.sent) - 1, nil
}

func (o *a4Obs) refuse(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refused = append(o.refused, reason)
}

func (o *a4Obs) finish(i, status int, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i < 0 || i >= len(o.sent) {
		return
	}
	o.sent[i].Status = status
	o.sent[i].Err = a4cErrorKind(err)
}

func (o *a4Obs) addBody(m map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bodies = append(o.bodies, m)
}

func (o *a4Obs) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.sent)
}

func (o *a4Obs) refusedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.refused)
}

func (o *a4Obs) records() []a4cSent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]a4cSent(nil), o.sent...)
}

func (o *a4Obs) kinds() []string {
	out := []string{}
	for _, r := range o.records() {
		out = append(out, r.Kind)
	}
	return out
}

func (o *a4Obs) snapshot() ([]string, []int, []map[string]any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	paths := make([]string, 0, len(o.sent))
	statuses := make([]int, 0, len(o.sent))
	for _, r := range o.sent {
		paths = append(paths, r.Path)
		statuses = append(statuses, r.Status)
	}
	return paths, statuses, append([]map[string]any(nil), o.bodies...)
}

// a4cErrorKind 把错误归为固定种类，避免把 URL/凭据写进证据。
func a4cErrorKind(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "budget exhausted"):
		return "budget"
	case strings.Contains(msg, "redirect"):
		return "redirect"
	case strings.Contains(msg, "outside read-only whitelist"):
		return "refused"
	}
	return "transport"
}

// a4BodyTracker 统计 HTTP 响应体 open/close 次数；open>close 视为泄漏。
type a4BodyTracker struct {
	mu     sync.Mutex
	opened int
	closed int
}

func (b *a4BodyTracker) addOpen() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opened++
}

func (b *a4BodyTracker) addClose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed++
}

func (b *a4BodyTracker) snapshot() (opened, closed int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opened, b.closed
}

type a4TrackedBody struct {
	io.ReadCloser
	tracker *a4BodyTracker
}

func (b *a4TrackedBody) Close() error {
	b.tracker.addClose()
	return b.ReadCloser.Close()
}

// a4cSender 是唯一的发送出口：在真正调用底层 RoundTrip 之前完成
// ①只读方法/路径白名单 ②并发安全的请求上限 ③请求登记；响应返回后再补记状态。
// 3xx 一律拒绝（不跟随重定向），保证“重定向”不会被当成成功响应。
type a4cSender struct {
	base    http.RoundTripper
	tracker *a4BodyTracker
	obs     *a4Obs
	index   string
	max     int
}

func a4cClassifyRequest(method, path, index string) (string, bool) {
	switch {
	case method == http.MethodPost && path == "/"+index+"/_search":
		return "search", true
	case method == http.MethodGet && path == "/"+index:
		return "meta", true
	case method == http.MethodGet && path == "/"+index+"/_mapping/field/"+a4cMappingFields:
		return "mapping", true
	}
	return "", false
}

// a4cPeekBody 读取并回填请求体，用于断言实际发出的查询（含向量）。
func a4cPeekBody(req *http.Request) (map[string]any, error) {
	if req.Body == nil {
		return nil, errors.New("empty body")
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, a4cMaxRequestBody))
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *a4cSender) RoundTrip(req *http.Request) (*http.Response, error) {
	kind, ok := a4cClassifyRequest(req.Method, req.URL.Path, s.index)
	if !ok {
		s.obs.refuse(req.Method + " " + req.URL.Path)
		return nil, fmt.Errorf("a4c: refused request %s %s (outside read-only whitelist)", req.Method, req.URL.Path)
	}
	idx, err := s.obs.begin(kind, req.Method, req.URL.Path, s.max)
	if err != nil {
		return nil, err
	}
	if kind == "search" {
		if body, bodyErr := a4cPeekBody(req); bodyErr == nil {
			s.obs.addBody(body)
		}
	}
	res, err := s.base.RoundTrip(req)
	if err != nil {
		s.obs.finish(idx, 0, err)
		return nil, err
	}
	s.tracker.addOpen()
	res.Body = &a4TrackedBody{ReadCloser: res.Body, tracker: s.tracker}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		_ = res.Body.Close()
		redirectErr := fmt.Errorf("a4c: refused redirect status %d (redirects disabled)", res.StatusCode)
		s.obs.finish(idx, res.StatusCode, redirectErr)
		return nil, redirectErr
	}
	s.obs.finish(idx, res.StatusCode, nil)
	if kind == "search" {
		if capture, ok := req.Context().Value(a4cCaptureKey{}).(*a4cCapture); ok {
			capture.status = res.StatusCode
			// Read at most limit+1 bytes. Oversize/read failure closes the body and
			// fails the request; the SDK never receives a truncated success response.
			raw, readErr := io.ReadAll(io.LimitReader(res.Body, a4cMaxResponseBody+1))
			if readErr != nil || len(raw) > a4cMaxResponseBody {
				_ = res.Body.Close()
				capture.invalid = true
				return nil, errors.New("a4c: response observation unavailable")
			}
			capture.observe(raw)
			res.Body = &a4cReplayBody{Reader: bytes.NewReader(raw), original: res.Body}
		}
	}
	return res, nil
}

type a4cReplayBody struct {
	*bytes.Reader
	original io.Closer
}

func (b *a4cReplayBody) Close() error { return b.original.Close() }

// a4cEmbedding 是本机 fake embedding：记录调用次数/维度，返回 1024 维 one-hot。
type a4cEmbedding struct {
	mu    sync.Mutex
	srv   *httptest.Server
	calls int
	dims  []int
}

func a4cNewEmbedding(t *testing.T, dims int) *a4cEmbedding {
	t.Helper()
	e := &a4cEmbedding{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
			Model      string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("embedding request decode: %v", err)
		}
		e.mu.Lock()
		e.calls++
		e.dims = append(e.dims, req.Dimensions)
		e.mu.Unlock()
		if req.Dimensions != dims {
			t.Errorf("embedding dimensions=%d want=%d", req.Dimensions, dims)
		}
		data := []any{}
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": a4cOneHotVector(dims, a4cHotIndex)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": req.Model})
	}))
	t.Cleanup(e.srv.Close)
	a4RequireLoopback(t, e.srv.URL)
	return e
}

func (e *a4cEmbedding) url() string { return e.srv.URL }

func (e *a4cEmbedding) stats() (int, []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, append([]int(nil), e.dims...)
}

// a4Component 是组件入口句柄：两条向量路的检索器（均来自生产 newRetriever）。
type a4Component struct {
	Content er.Retriever
	QA      er.Retriever
	Index   string
	Target  string
	Real    bool
	Client  *elasticsearch.Client
	emb     *a4cEmbedding
	obs     *a4Obs
	bodies  *a4BodyTracker
}

func a4cNewFake(t *testing.T, status int, body string) *a4Component {
	t.Helper()
	return a4cNewFakeLimit(t, status, body, a4cMaxRequests)
}

func a4cNewFakeLimit(t *testing.T, status int, body string, max int) *a4Component {
	t.Helper()
	return a4cNewFakeHandler(t, max, func(w http.ResponseWriter, r *http.Request) {
		a4cWriteJSON(w, status, body)
	})
}

// a4cNewFakeHandler 允许自定义 handler（取消测试需要阻塞连接）。
func a4cNewFakeHandler(t *testing.T, max int, h func(w http.ResponseWriter, r *http.Request)) *a4Component {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(srv.Close)
	a4RequireLoopback(t, srv.URL)
	return a4cBuild(t, srv.URL, a4cIndexName, a4cDims, "", "", false, &a4Obs{}, max)
}

func a4cWriteJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func a4cBuild(t *testing.T, target, index string, dims int, user, pass string, real bool, obs *a4Obs, max int) *a4Component {
	t.Helper()
	if !real {
		a4RequireLoopback(t, target)
	}
	tracker := &a4BodyTracker{}
	sender := &a4cSender{base: http.DefaultTransport, tracker: tracker, obs: obs, index: index, max: max}
	cfg := elasticsearch.Config{
		Addresses: []string{target},
		Transport: sender,
		// 默认 DisableRetry=false + MaxRetries=3，会对 502/503/504 自动重试；
		// 这里显式关闭，保证“每条请求只发一次”由客户端行为保证，而非事后统计。
		DisableRetry: true,
	}
	if user != "" {
		cfg.Username, cfg.Password = user, pass
	}
	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		t.Fatal("new es client: invalid configuration")
	}
	emb := a4cNewEmbedding(t, dims)
	conf := &config.Config{
		Client:              client,
		IndexName:           index,
		APIKey:              "offline",
		BaseURL:             emb.url(),
		EmbeddingModel:      "text-embedding-3-large",
		EmbeddingDimensions: dims,
	}
	build := func(field string) er.Retriever {
		ctx := context.WithValue(context.Background(), coretypes.RetrieverFieldKey, field)
		ret, buildErr := newRetriever(ctx, conf)
		if buildErr != nil {
			t.Fatalf("newRetriever(%s): construction failed", field)
		}
		return ret
	}
	return &a4Component{
		Content: build(a4cContentFld),
		QA:      build(coretypes.FieldQAContentVector),
		Index:   index,
		Target:  target,
		Real:    real,
		Client:  client,
		emb:     emb,
		obs:     obs,
		bodies:  tracker,
	}
}

func (c *a4Component) retrieve(ctx context.Context, r er.Retriever) ([]*schema.Document, error) {
	return r.Retrieve(ctx, "a4-component-query", er.WithTopK(50), es8.WithFilters(a4QAFieldFilter()))
}

// a4cFilter 复制生产 Rag.retrieve 的 _knowledge_name 过滤形状（同字段/同 match）。
// 真实分支由测试复制而非调用 Rag.retrieve，因此不覆盖生产 filter 构造；
// 生产 filter 构造由 core 包入口（Rag.retrieve）覆盖。
func a4cFilter(kb string) []types.Query {
	return []types.Query{{Bool: &types.BoolQuery{Must: []types.Query{{
		Match: map[string]types.MatchQuery{coretypes.KnowledgeName: {Query: kb}},
	}}}}}
}

// a4cSentVector 取出请求体里的查询向量（script_score.script.params.embedding）。
func a4cSentVector(t *testing.T, body map[string]any) []float64 {
	t.Helper()
	script := body["query"].(map[string]any)["script_score"].(map[string]any)["script"].(map[string]any)
	params, ok := script["params"].(map[string]any)
	if !ok {
		t.Fatalf("script.params missing: %v", script)
	}
	raw, ok := params["embedding"].([]any)
	if !ok {
		t.Fatalf("params.embedding missing: %v", params)
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

// a4cAssertOneHot 断言真实发出的向量是 1024 维非零 one-hot（L0 合成向量形状）。
func a4cAssertOneHot(t *testing.T, vec []float64) {
	t.Helper()
	if len(vec) != a4cDims {
		t.Fatalf("sent embedding len = %d, want %d", len(vec), a4cDims)
	}
	sum := 0.0
	nonZero := 0
	for i, v := range vec {
		if v == 0 {
			continue
		}
		nonZero++
		if i != a4cHotIndex || v != 1 {
			t.Fatalf("embedding[%d] = %v, want single 1.0 at %d", i, v, a4cHotIndex)
		}
		sum += v
	}
	if nonZero != 1 || sum != 1 {
		t.Fatalf("embedding non-zero=%d sum=%v, want one-hot (1 non-zero, sum 1)", nonZero, sum)
	}
}

// TestA4Component_NormalResponse 正常 200 响应：请求形状、生产解析、向量、响应体释放。
//
// 生产共享：es8 组件 + cosine search_mode + EsHit2Document；fake 仅有响应体与 embedding。
func TestA4Component_NormalResponse(t *testing.T) {
	body := `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},` +
		`"hits":{"total":{"value":1,"relation":"eq"},"max_score":1.42,"hits":[{"_index":"a4-component-index",` +
		`"_id":"doc-1","_score":1.42,"_source":{"content":"hello","content_vector":[0.1,0.2],` +
		`"_knowledge_name":"a4-kb","ext":""}}]}}`
	c := a4cNewFake(t, http.StatusOK, body)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	docs, err := c.retrieve(ctx, c.Content)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	if docs[0].ID != "doc-1" || docs[0].Content != "hello" {
		t.Fatalf("unexpected doc: id=%q content=%q", docs[0].ID, docs[0].Content)
	}
	if got := docs[0].Score(); got != 1.42 {
		t.Fatalf("score = %v, want raw ES score 1.42 (组件层不做 -1 归一化)", got)
	}
	if v := docs[0].DenseVector(); len(v) != 2 {
		t.Fatalf("dense vector len = %d, want 2 (解析器不校验维度)", len(v))
	}
	if got := docs[0].MetaData[coretypes.KnowledgeName]; got != "a4-kb" {
		t.Fatalf("knowledge name = %v, want a4-kb", got)
	}

	paths, _, bodies := c.obs.snapshot()
	if len(paths) != 1 || paths[0] != "/a4-component-index/_search" {
		t.Fatalf("paths = %v, want one /a4-component-index/_search", paths)
	}
	q := bodies[0]["query"].(map[string]any)["script_score"].(map[string]any)
	if script := q["script"].(map[string]any)["source"]; script != a4cContentScript {
		t.Fatalf("script = %v, want %q", script, a4cContentScript)
	}
	if size, ok := bodies[0]["size"].(float64); !ok || int(size) != 50 {
		t.Fatalf("size = %v, want 50", bodies[0]["size"])
	}
	// 生产请求不带 _source 过滤（与 L0 的 _source:false 形状不同）。
	if _, ok := bodies[0]["_source"]; ok {
		t.Fatalf("unexpected _source filter in production-shaped request: %v", bodies[0]["_source"])
	}
	filter := q["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
	match := filter[0].(map[string]any)["bool"].(map[string]any)["must"].([]any)[0].(map[string]any)["match"].(map[string]any)
	if kn, ok := match[coretypes.KnowledgeName].(map[string]any); !ok || kn["query"] != "a4-kb" {
		t.Fatalf("knowledge filter = %v, want %s=a4-kb", match, coretypes.KnowledgeName)
	}
	// 实际发出的查询向量：1024 维非零 one-hot（与 L0 合成向量一致，非全零）。
	a4cAssertOneHot(t, a4cSentVector(t, bodies[0]))
	calls, dims := c.emb.stats()
	if calls != 1 || len(dims) != 1 || dims[0] != a4cDims {
		t.Fatalf("embedding calls=%d dims=%v, want 1 call with %d dims", calls, dims, a4cDims)
	}

	opened, closed := c.bodies.snapshot()
	if opened != 1 || closed != 1 {
		t.Fatalf("response body open/close = %d/%d, want 1/1", opened, closed)
	}
}

// TestA4Component_ESErrorPropagated ES 4xx 透传：不静默成 0 条，错误类型由生产依赖保留。
func TestA4Component_ESErrorPropagated(t *testing.T) {
	body := `{"error":{"root_cause":[{"type":"script_exception","reason":"hidden"}],` +
		`"type":"search_phase_execution_exception","reason":"hidden"},"status":400}`
	c := a4cNewFake(t, http.StatusBadRequest, body)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	docs, err := c.retrieve(ctx, c.QA)
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
	if esErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", esErr.Status)
	}
	if esErr.ErrorCause.Type != "search_phase_execution_exception" {
		t.Fatalf("error type = %q, want search_phase_execution_exception", esErr.ErrorCause.Type)
	}
	if n := c.obs.count(); n != 1 {
		t.Fatalf("requests = %d, want 1 (no retry)", n)
	}
	_, _, bodies := c.obs.snapshot()
	if len(bodies) != 1 || !strings.Contains(bodies[0]["query"].(map[string]any)["script_score"].(map[string]any)["script"].(map[string]any)["source"].(string), a4cQAScriptSnippet) {
		t.Fatalf("qa request did not carry qa_content_vector script: %v", bodies)
	}
	opened, closed := c.bodies.snapshot()
	if opened != 1 || closed != 1 {
		t.Fatalf("error response body open/close = %d/%d, want 1/1", opened, closed)
	}
}

// TestA4Component_ContextCancel 取消：ctx 传导到生产 HTTP 层，调用在有限时间内返回。
func TestA4Component_ContextCancel(t *testing.T) {
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release) // 先于 httptest.Server.Close 执行，避免 handler 永久阻塞
	c := a4cNewFakeHandler(t, a4cMaxRequests, func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		<-release // 客户端取消后不再写任何响应
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-arrived:
			cancel()
		case <-time.After(3 * time.Second):
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := c.retrieve(ctx, c.Content)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("error does not carry cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retrieve did not return after cancellation")
	}
	recs := c.obs.records()
	if len(recs) > 1 {
		t.Fatalf("requests = %d, want <=1 (no retry after cancel)", len(recs))
	}
	if len(recs) == 1 && recs[0].Err != "canceled" && recs[0].Err != "transport" {
		t.Fatalf("send record error kind = %q, want canceled/transport", recs[0].Err)
	}
}

// TestA4Component_ResponseBodyReleased 正常与错误两条路径都必须关闭响应体。
func TestA4Component_ResponseBodyReleased(t *testing.T) {
	okBody := `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},` +
		`"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
	errBody := `{"error":{"root_cause":[],"type":"search_phase_execution_exception","reason":"hidden"},"status":400}`

	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "normal", status: http.StatusOK, body: okBody},
		{name: "error", status: http.StatusBadRequest, body: errBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := a4cNewFake(t, tc.status, tc.body)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = c.retrieve(ctx, c.Content)
			opened, closed := c.bodies.snapshot()
			if opened != 1 || closed != 1 {
				t.Fatalf("body open/close = %d/%d, want 1/1", opened, closed)
			}
		})
	}
}
