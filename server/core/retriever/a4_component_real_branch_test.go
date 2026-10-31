package retriever

// A-4 / P-8 组件级（L2）真实分支：目标解析、只读预检、结果分类与离线覆盖。
//
// 边界（不可省略）：
//   - 真实目标默认不可达：必须同时满足 ALLOW/AUTHZ/URL/INDEX/INDEX_UUID/KB，
//     非回环目标还需 ALLOW_REMOTE=1；任一不满足都显式拒绝，不静默回退假 ES。
//   - 所有请求都经 a4cSender：发送前做只读方法/路径白名单 + 并发安全上限，
//     3xx 直接拒绝（不跟随重定向），SDK 重试已由 DisableRetry 关闭。
//   - 预检只读：GET <index>（uuid/aliases）与 GET <index>/_mapping/field/<两个向量字段>；
//     任一不可判读、uuid 不符、别名/多目标、content_vector 维度不符 → fail closed。
//   - 目标 UUID 必须完整、逐字节精确相等（区分大小写）；不做 EqualFold、
//     前缀/后缀或空白归一，不匹配时只发 1 条只读 GET 即终止。
//   - 结果分类区分“已发请求 / 对照查询成功 / QA 可判定 / 不完整”；
//     分类严格按 a4cClassifyError 的声明表：只有 2xx 可解析与
//     (400, search_phase_execution_exception, root_cause script_exception) 可判定；
//     404/index_not_found、未知 error.type、401/403/408/429、5xx、不可解析响应、
//     对照查询失败一律 incomplete；不把“任意非认证 4xx”当预期错误。
//   - 可判定的脚本错误只报告实际状态与安全类型（note=cause_not_attributed），
//     不用状态码推断“缺字段”是原因。
//   - 网络、认证、5xx、意外状态、提前退出一律 incomplete，测试必须失败，
//     不得用 PASS 代替观察结论；Observed 只在完整观察时置真。
//   - 本入口绕过 core.Rag 的 fingerprint/schema 门禁，不经过双路合并/RRF/rerank；
//     不构成 L3 全链验证，也不判定 A-4 或 Stage 2D 通过。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/retriever/es8"
	er "github.com/cloudwego/eino/components/retriever"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/core/vector"
)

const (
	a4cAllowEnv     = "A4_COMPONENT_ALLOW_REAL_ES"
	a4cAuthzEnv     = "A4_COMPONENT_AUTHZ"
	a4cURLEnv       = "A4_COMPONENT_ES_URL"
	a4cIndexEnv     = "A4_COMPONENT_INDEX"
	a4cIndexUUIDEnv = "A4_COMPONENT_INDEX_UUID"
	a4cKBEnv        = "A4_COMPONENT_KB"
	a4cDimsEnv      = "A4_COMPONENT_DIMS"
	a4cRemoteEnv    = "A4_COMPONENT_ALLOW_REMOTE"
	a4cUserEnv      = "A4_COMPONENT_ES_USER"
	a4cPassEnv      = "A4_COMPONENT_ES_PASS"
	// a4cAuthzPhrase 是显式授权短语：仅设置 URL 不会触发真实目标。
	a4cAuthzPhrase = "I-AUTHORIZE-A4-COMPONENT-READONLY"
)

// a4RealTarget 是经显式授权后才允许使用的真实目标。
type a4RealTarget struct {
	URL   string
	Index string
	UUID  string // 预期物理索引 uuid（只读预检比对）
	KB    string
	Dims  int
	User  string
	Pass  string
}

// a4cResolveRealTarget 解析真实目标开关；全空返回启用=false。
// 任何单独设置 URL、缺少授权短语、缺少预期 uuid 的情况都拒绝执行。
func a4cResolveRealTarget(getenv func(string) string) (a4RealTarget, bool, error) {
	rawURL := strings.TrimSpace(getenv(a4cURLEnv))
	allow := strings.TrimSpace(getenv(a4cAllowEnv))
	authz := strings.TrimSpace(getenv(a4cAuthzEnv))
	if rawURL == "" && allow == "" && authz == "" {
		return a4RealTarget{}, false, nil
	}
	if allow != "1" {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s must be \"1\"", a4cAllowEnv)
	}
	if authz != a4cAuthzPhrase {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s must equal the documented authorization phrase", a4cAuthzEnv)
	}
	if rawURL == "" {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s is empty", a4cURLEnv)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return a4RealTarget{}, false, fmt.Errorf("refused: invalid %s", a4cURLEnv)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return a4RealTarget{}, false, errors.New("refused: unsupported URL scheme")
	}
	if u.User != nil {
		return a4RealTarget{}, false, fmt.Errorf("refused: credentials must not be embedded in %s; use %s/%s", a4cURLEnv, a4cUserEnv, a4cPassEnv)
	}
	if !a4cIsLoopback(u.Hostname()) && strings.TrimSpace(getenv(a4cRemoteEnv)) != "1" {
		return a4RealTarget{}, false, fmt.Errorf("refused: non-loopback host requires %s=1", a4cRemoteEnv)
	}
	index := strings.TrimSpace(getenv(a4cIndexEnv))
	if index == "" {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s is empty", a4cIndexEnv)
	}
	if err := vector.ValidatePhysicalName(index); err != nil {
		return a4RealTarget{}, false, fmt.Errorf("refused: invalid %s", a4cIndexEnv)
	}
	// UUID 逐字节比对，因此输入链任何位置都不做归一：首尾空白直接拒绝，
	// 不做 TrimSpace 后再比（否则会把带空白的两个不同字节串当成同一目标）。
	rawUUID := getenv(a4cIndexUUIDEnv)
	if rawUUID != strings.TrimSpace(rawUUID) {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s must not contain leading/trailing whitespace", a4cIndexUUIDEnv)
	}
	uuid := rawUUID
	if len(uuid) < 8 || strings.ContainsAny(uuid, " \t") {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s must be the previously confirmed index uuid (>=8 chars)", a4cIndexUUIDEnv)
	}
	kb := strings.TrimSpace(getenv(a4cKBEnv))
	if kb == "" {
		return a4RealTarget{}, false, fmt.Errorf("refused: %s is empty (filter must match production)", a4cKBEnv)
	}
	dims := a4cDims
	if raw := strings.TrimSpace(getenv(a4cDimsEnv)); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 || parsed > 4096 {
			return a4RealTarget{}, false, fmt.Errorf("refused: invalid %s", a4cDimsEnv)
		}
		dims = parsed
	}
	return a4RealTarget{
		URL: u.String(), Index: index, UUID: uuid, KB: kb, Dims: dims,
		User: strings.TrimSpace(getenv(a4cUserEnv)), Pass: getenv(a4cPassEnv),
	}, true, nil
}

func a4cIsLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// a4cProbeKind 是单条查询的判定类别。只有 ok 与 script_rejected 可判定；
// 其余类别都表示“没有得到可判定的结果”（即 incomplete）。
type a4cProbeKind string

const (
	a4cProbeNotRun a4cProbeKind = "not_run"
	a4cProbeOK     a4cProbeKind = "ok"
	// a4cProbeScriptRejected 是唯一允许记录的 4xx：必须同时命中状态码、
	// error.type 与 root_cause.type 的声明组合（见 a4cClassifyError）。
	// 它只记录“脚本被拒”这一观察，不得当成缺字段的证据。
	a4cProbeScriptRejected a4cProbeKind = "script_rejected"
	a4cProbeTargetMissing  a4cProbeKind = "target_missing"
	a4cProbeUndeclared4xx  a4cProbeKind = "undeclared_4xx"
	a4cProbeAuth           a4cProbeKind = "auth_or_throttle"
	a4cProbeServer         a4cProbeKind = "server_5xx"
	a4cProbeUnparsable     a4cProbeKind = "unparsable"
	a4cProbeTransport      a4cProbeKind = "transport"
)

// 预先声明的错误分类常量：只承认 A-4 关心的那一组脚本错误形态。
const (
	a4cErrTypeIndexMissing = "index_not_found_exception"
	a4cErrTypeSearchPhase  = "search_phase_execution_exception"
	a4cErrTypeScript       = "script_exception"
	// a4cNoteCauseNotAttributed 标明“只记录了状态与安全类型”，不归因原因。
	a4cNoteCauseNotAttributed = "cause_not_attributed"
)

// a4cClassifyError 按预先声明的表分类（顺序即优先级；未命中即不可判定）：
//
//  1. 缺状态码或缺 error.type -> unparsable
//  2. 404 或 index_not_found_exception -> target_missing
//  3. 401 / 403 / 408 / 429 -> auth_or_throttle
//  4. status>=500 -> server_5xx
//  5. 400 + error.type=search_phase_execution_exception + root_cause.type=script_exception
//     -> script_rejected（唯一可判定 4xx）
//  6. 其余 4xx（未知 error.type、422 等） -> undeclared_4xx
//  7. 其他 -> transport
//
// 关键约束：不把“任意非认证 4xx”归为可判定；只看状态码不做缺字段归因。
func a4cClassifyError(status int, errType, rootCause string) a4cProbeKind {
	switch {
	case status == 0 || errType == "":
		return a4cProbeUnparsable
	case status == http.StatusNotFound || errType == a4cErrTypeIndexMissing:
		return a4cProbeTargetMissing
	case status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status == http.StatusRequestTimeout || status == http.StatusTooManyRequests:
		return a4cProbeAuth
	case status >= 500:
		return a4cProbeServer
	case status == http.StatusBadRequest && errType == a4cErrTypeSearchPhase && rootCause == a4cErrTypeScript:
		return a4cProbeScriptRejected
	case status >= 400 && status < 500:
		return a4cProbeUndeclared4xx
	}
	return a4cProbeTransport
}

// a4cProbe 是一条查询的脱敏观察：不含正文、向量与原始错误文本。
type a4cProbe struct {
	Kind      a4cProbeKind
	Status    int
	Hits      int
	ErrType   string
	RootCause string
	Note      string
}

// Complete 表示“该查询得到了可判定的观察”：2xx 可解析，或命中声明表的脚本错误。
func (p a4cProbe) Complete() bool {
	return p.Kind == a4cProbeOK || p.Kind == a4cProbeScriptRejected
}

func (p a4cProbe) Summary() string {
	out := fmt.Sprintf("kind=%s status=%d hits=%d", p.Kind, p.Status, p.Hits)
	if p.ErrType != "" {
		out += " error.type=" + a4cSafeErrorType(p.ErrType)
	}
	if p.RootCause != "" {
		out += " root_cause.type=" + a4cSafeErrorType(p.RootCause)
	}
	if p.Note != "" {
		out += " note=" + p.Note
	}
	return out
}

func a4cSafeErrorType(s string) string {
	switch s {
	case "":
		return ""
	case a4cErrTypeIndexMissing, a4cErrTypeSearchPhase, a4cErrTypeScript:
		return s
	default:
		return "unknown"
	}
}

// a4cVerdict 是一次真实分支运行的完整判定；Summary 只写白名单字段。
type a4cVerdict struct {
	QueryDims    int
	Requests     int
	Refused      int
	IndexOK      bool
	AliasesEmpty bool
	UUIDMatch    bool
	ContentOK    bool
	ContentDims  int
	QAExists     bool
	Control      a4cProbe
	QA           a4cProbe
	EmbCalls     int
	// Observed 仅在“预检通过 + 对照 200 且有命中 + QA 可判定”时置真；
	// 它等价于 !Incomplete，单独写出是为了让证据字符串自证“是否完成观察”。
	Observed    bool
	Incomplete  bool
	Reason      string
	Observation string
}

// Capture is scoped to one Retrieve context, never a process-wide last response.
const a4cMaxResponseBody = 1 << 20

type a4cCaptureKey struct{}
type a4cCapture struct {
	status                      int
	invalid, conflict, complete bool
}

func (c *a4cCapture) observe(raw []byte) {
	var m struct {
		Status   *int  `json:"status"`
		TimedOut *bool `json:"timed_out"`
		Shards   *struct {
			Failed *int `json:"failed"`
		} `json:"_shards"`
	}
	if json.Unmarshal(raw, &m) != nil {
		c.invalid = true
		return
	}
	if m.Status != nil && *m.Status != c.status {
		c.conflict = true
	}
	if c.status >= 200 && c.status < 300 {
		c.complete = m.TimedOut != nil && !*m.TimedOut && m.Shards != nil && m.Shards.Failed != nil && *m.Shards.Failed == 0
	}
}

// Summary 是唯一允许写进证据的形态：计数、状态、类型、存在性。
func (v a4cVerdict) Summary() string {
	queryVec := "not_generated"
	if v.EmbCalls > 0 && v.QueryDims > 0 {
		queryVec = fmt.Sprintf("e0/%d", v.QueryDims)
	}
	return fmt.Sprintf(
		"requests=%d refused=%d index_ok=%t aliases_empty=%t uuid_match=%t content_exist=%t content_dims=%d qa_exist=%t emb_calls=%d query_vec=%s observed=%t incomplete=%t reason=%q control=[%s] qa=[%s] observation=%q",
		v.Requests, v.Refused, v.IndexOK, v.AliasesEmpty, v.UUIDMatch, v.ContentOK, v.ContentDims, v.QAExists, v.EmbCalls, queryVec, v.Observed, v.Incomplete, v.Reason, v.Control.Summary(), v.QA.Summary(), v.Observation,
	)
}

// a4cRunRealProbe 执行一次组件级真实分支：只读预检 → 对照查询 → QA 查询 → 分类。
// 所有请求都由 a4cSender 记账并受 max 限制；任何不可判读都返回 Incomplete。
func a4cRunRealProbe(t *testing.T, ctx context.Context, tgt a4RealTarget, obs *a4Obs, max int) (v a4cVerdict) {
	t.Helper()
	c := a4cBuild(t, tgt.URL, tgt.Index, tgt.Dims, tgt.User, tgt.Pass, true, obs, max)
	defer func() {
		v.Requests = obs.count()
		v.Refused = obs.refusedCount()
		var dims []int
		v.EmbCalls, dims = c.emb.stats()
		if len(dims) > 0 {
			v.QueryDims = dims[len(dims)-1]
		}
	}()

	if err := a4cPrecheckIndex(ctx, c, tgt, &v); err != nil {
		v.Incomplete, v.Reason = true, "index:"+err.Error()
		return
	}
	if err := a4cPrecheckMapping(ctx, c, tgt, &v); err != nil {
		v.Incomplete, v.Reason = true, "mapping:"+err.Error()
		return
	}
	// 对照查询必须是 200 且有命中：任何 4xx/5xx/0 命中都算对照失败（incomplete）。
	v.Control = a4cRunSearch(ctx, c, c.Content, tgt.KB)
	if v.Control.Kind != a4cProbeOK {
		v.Incomplete, v.Reason = true, "control:"+v.Control.Summary()
		return
	}
	if v.Control.Hits < 1 {
		v.Incomplete, v.Reason = true, "control:no_hits (filter or corpus mismatch)"
		return
	}
	v.QA = a4cRunSearch(ctx, c, c.QA, tgt.KB)
	if !v.QA.Complete() {
		v.Incomplete, v.Reason = true, "qa:"+v.QA.Summary()
		return
	}
	v.Observed = true
	v.Observation = "control: " + v.Control.Summary() + " | qa: " + v.QA.Summary()
	if v.QA.Kind == a4cProbeScriptRejected {
		// 只声明“脚本被拒”这一事实；状态码不是缺字段的证据。
		v.Observation += " | qa_attribution=none (status+types only; missing-field NOT proven)"
	}
	return
}

// a4cRunSearch 用生产检索器发一条只读查询（TopK=1，减少 _source 暴露面）。
func a4cRunSearch(ctx context.Context, c *a4Component, r er.Retriever, kb string) a4cProbe {
	cap := &a4cCapture{}
	ctx = context.WithValue(ctx, a4cCaptureKey{}, cap)
	docs, err := r.Retrieve(ctx, "a4-component-query", er.WithTopK(1), es8.WithFilters(a4cFilter(kb)))
	if cap.status == 0 && err != nil {
		return a4cProbe{Kind: a4cProbeTransport, Note: a4cErrorKind(err)}
	}
	if cap.invalid || cap.status == 0 {
		return a4cProbe{Kind: a4cProbeUnparsable, Status: cap.status}
	}
	if cap.conflict {
		return a4cProbe{Kind: a4cProbeUnparsable, Status: cap.status, Note: "status_mismatch"}
	}
	if cap.status >= 500 {
		return a4cProbe{Kind: a4cProbeServer, Status: cap.status}
	}
	if cap.status == 401 || cap.status == 403 || cap.status == 408 || cap.status == 429 {
		return a4cProbe{Kind: a4cProbeAuth, Status: cap.status}
	}
	if cap.status >= 200 && cap.status < 300 && !cap.complete {
		return a4cProbe{Kind: a4cProbeUnparsable, Status: cap.status, Note: "incomplete_response"}
	}
	if err == nil {
		if cap.status < 200 || cap.status >= 300 {
			return a4cProbe{Kind: a4cProbeUnparsable, Status: cap.status}
		}
		return a4cProbe{Kind: a4cProbeOK, Status: cap.status, Hits: len(docs)}
	}
	var esErr *types.ElasticsearchError
	if !errors.As(err, &esErr) {
		// 网络/取消/响应不可解析：不构成可判定观察。
		return a4cProbe{Kind: a4cProbeTransport, Note: a4cErrorKind(err)}
	}
	p := a4cProbe{Status: cap.status, ErrType: a4cSafeErrorType(esErr.ErrorCause.Type)}
	if len(esErr.ErrorCause.RootCause) > 0 {
		p.RootCause = a4cSafeErrorType(esErr.ErrorCause.RootCause[0].Type)
	}
	p.Kind = a4cClassifyError(p.Status, p.ErrType, p.RootCause)
	if p.Kind == a4cProbeScriptRejected {
		// 只报告实际状态与安全类型，不把 400 当作缺字段的原因。
		p.Note = a4cNoteCauseNotAttributed
	}
	return p
}

// a4cGet 通过生产 ES 客户端发只读 GET（经 a4cSender 记账，受上限约束）。
func a4cGet(ctx context.Context, c *a4Component, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Target, "/")+path, nil)
	if err != nil {
		return 0, nil, err
	}
	res, err := c.Client.Perform(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, a4cMaxRequestBody))
	return res.StatusCode, raw, readErr
}

// a4cPrecheckIndex 只读确认“单一具体物理索引 + 预期 uuid + 无别名”。
func a4cPrecheckIndex(ctx context.Context, c *a4Component, tgt a4RealTarget, v *a4cVerdict) error {
	status, raw, err := a4cGet(ctx, c, "/"+tgt.Index+"?filter_path=*.settings.index.uuid,*.aliases")
	if err != nil {
		return fmt.Errorf("transport:%s", a4cErrorKind(err))
	}
	if status != http.StatusOK {
		return fmt.Errorf("status=%d", status)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return errors.New("unparsable")
	}
	if len(doc) != 1 {
		return fmt.Errorf("targets=%d", len(doc))
	}
	entry, ok := doc[tgt.Index]
	if !ok {
		return errors.New("name_mismatch")
	}
	var meta struct {
		Settings struct {
			Index struct {
				UUID string `json:"uuid"`
			} `json:"index"`
		} `json:"settings"`
		Aliases map[string]json.RawMessage `json:"aliases"`
	}
	if err := json.Unmarshal(entry, &meta); err != nil {
		return errors.New("unparsable")
	}
	v.AliasesEmpty = len(meta.Aliases) == 0
	if !v.AliasesEmpty {
		return fmt.Errorf("alias_present=%d", len(meta.Aliases))
	}
	if meta.Settings.Index.UUID == "" {
		return errors.New("uuid_missing")
	}
	// UUID 必须完整、逐字节精确相等：大小写不同即视为不同目标，
	// 不做 EqualFold/前缀/后缀/空白归一，避免把两个字节不同的标识
	// 绑定成同一物理索引。比较结果只输出布尔值，不写 UUID 本身。
	v.UUIDMatch = meta.Settings.Index.UUID == tgt.UUID
	if !v.UUIDMatch {
		return errors.New("uuid_mismatch")
	}
	v.IndexOK = true
	return nil
}

// a4cPrecheckMapping 只读核验 content_vector 维度与 qa_content_vector 存在性。
// 两者都不可判读时 fail closed；qa 缺失是 A-4 观察前提，不算失败。
func a4cPrecheckMapping(ctx context.Context, c *a4Component, tgt a4RealTarget, v *a4cVerdict) error {
	status, raw, err := a4cGet(ctx, c, "/"+tgt.Index+"/_mapping/field/"+a4cMappingFields)
	if err != nil {
		return fmt.Errorf("transport:%s", a4cErrorKind(err))
	}
	if status != http.StatusOK {
		return fmt.Errorf("status=%d", status)
	}
	type fieldEntry struct {
		Mapping map[string]struct {
			Type string `json:"type"`
			Dims int    `json:"dims"`
		} `json:"mapping"`
	}
	var doc map[string]struct {
		Mappings map[string]fieldEntry `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return errors.New("unparsable")
	}
	entry, ok := doc[tgt.Index]
	if !ok {
		return errors.New("name_mismatch")
	}
	content, ok := entry.Mappings[a4cContentFld]
	if !ok {
		return errors.New("content_vector_missing")
	}
	spec, ok := content.Mapping[a4cContentFld]
	if !ok || spec.Dims == 0 {
		return errors.New("content_vector_dims_unreadable")
	}
	if spec.Dims != tgt.Dims {
		return fmt.Errorf("content_vector_dims=%d want=%d", spec.Dims, tgt.Dims)
	}
	v.ContentOK, v.ContentDims = true, spec.Dims
	if qa, ok := entry.Mappings[coretypes.FieldQAContentVector]; ok && len(qa.Mapping) > 0 {
		v.QAExists = true
	}
	return nil
}

// TestA4Component_RealESTarget 真实 ES 组件级入口：默认 skip，只有显式授权才运行。
// 本轮不设置 A4_COMPONENT_* 环境变量，因此不会连接任何真实服务。
func TestA4Component_RealESTarget(t *testing.T) {
	tgt, enabled, err := a4cResolveRealTarget(os.Getenv)
	if err != nil {
		t.Fatalf("real target configuration refused: %v", err)
	}
	if !enabled {
		t.Skip("real ES target not enabled; see design/stage2d-a4-l2-component.md §7")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v := a4cRunRealProbe(t, ctx, tgt, &a4Obs{}, a4cMaxRequests)
	t.Logf("component-level observation: %s", v.Summary())
	if v.Incomplete {
		t.Fatalf("INCOMPLETE: %s (not a success; no A-4 conclusion)", v.Reason)
	}
	if len(v.Control.Summary()) == 0 || v.Control.Hits < 1 {
		t.Fatalf("control query did not succeed: %s", v.Control.Summary())
	}
	// PASS 只表示“本入口完成了一次可判定的观察”，不是 A-4/2D 结论。
	t.Logf("OBSERVED (component-level only; NOT an A-4 or Stage 2D conclusion): %s", v.Observation)
}

// TestA4Component_TargetGate 只测开关逻辑，不连接任何目标。
func TestA4Component_TargetGate(t *testing.T) {
	base := map[string]string{
		a4cAllowEnv: "1", a4cAuthzEnv: a4cAuthzPhrase,
		a4cURLEnv: "http://127.0.0.1:9200", a4cIndexEnv: "a4-phys",
		a4cIndexUUIDEnv: "abcdefghijklmnop", a4cKBEnv: "kb-1",
	}
	with := func(over map[string]string) func(string) string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range over {
			env[k] = v
		}
		return func(k string) string { return env[k] }
	}
	if _, ok, err := a4cResolveRealTarget(func(string) string { return "" }); err != nil || ok {
		t.Fatalf("empty env: ok=%v err=%v, want disabled", ok, err)
	}
	refused := []struct {
		name string
		env  func(string) string
	}{
		{"url only", with(map[string]string{a4cAllowEnv: ""})},
		{"bad authz", with(map[string]string{a4cAuthzEnv: "yes"})},
		{"missing index", with(map[string]string{a4cIndexEnv: ""})},
		{"wildcard index", with(map[string]string{a4cIndexEnv: "logs-*"})},
		{"alias expression index", with(map[string]string{a4cIndexEnv: "logs-000001,logs-000002"})},
		{"missing uuid", with(map[string]string{a4cIndexUUIDEnv: ""})},
		{"uuid leading space", with(map[string]string{a4cIndexUUIDEnv: "  " + a4cFakeUUID})},
		{"uuid trailing space", with(map[string]string{a4cIndexUUIDEnv: a4cFakeUUID + " "})},
		{"uuid internal whitespace", with(map[string]string{a4cIndexUUIDEnv: a4cFakeUUID[:4] + " " + a4cFakeUUID[4:]})},
		{"missing kb", with(map[string]string{a4cKBEnv: ""})},
		{"credentials in url", with(map[string]string{a4cURLEnv: "http://u:p@127.0.0.1:9200"})},
		{"remote without flag", with(map[string]string{a4cURLEnv: "http://es.example.com:9200"})},
		{"bad scheme", with(map[string]string{a4cURLEnv: "ftp://127.0.0.1:9200"})},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok, err := a4cResolveRealTarget(tc.env); err == nil || ok {
				t.Fatalf("want refusal, got ok=%v err=%v", ok, err)
			}
		})
	}
	got, ok, err := a4cResolveRealTarget(with(nil))
	if !ok || err != nil {
		t.Fatalf("want enabled, got ok=%v err=%v", ok, err)
	}
	if got.Index != "a4-phys" || got.KB != "kb-1" || got.Dims != a4cDims || got.UUID != "abcdefghijklmnop" {
		t.Fatalf("unexpected target: %+v", got)
	}
}

// ---- 离线（httptest）真实分支覆盖：不连接任何真实 ES ----

const (
	a4cFakeUUID    = "a4c-fake-uuid-000000000"
	a4cESErrorBody = `{"error":{"root_cause":[{"type":"script_exception","reason":"hidden"}],` +
		`"type":"search_phase_execution_exception","reason":"hidden"},"status":400}`
	a4cSearchHitBody = `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},` +
		`"hits":{"total":{"value":1,"relation":"eq"},"max_score":2.0,"hits":[{"_index":"a4-component-index",` +
		`"_id":"doc-1","_score":2.0,"_source":{"content":"hello","content_vector":[0.1,0.2],` +
		`"_knowledge_name":"a4-kb","ext":""}}]}}`
	a4cSearchEmptyBody = `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},` +
		`"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`
)

type a4cRealFakeOpt struct {
	UUID          string
	MetaStatus    int
	MetaBody      string
	MetaLocation  string
	MappingStatus int
	MappingBody   string
	ContentStatus int
	ContentBody   string
	QAStatus      int
	QABody        string
	BlockFirst    bool
}

// a4cRealFake 是本机假 ES：同时承接只读预检与两条查询，按路径分流并记录服务端实际收到的请求。
type a4cRealFake struct {
	mu     sync.Mutex
	served []string
	srv    *httptest.Server
	arrive chan struct{}
	gate   chan struct{}
}

func a4cNewRealFake(t *testing.T, opt a4cRealFakeOpt) *a4cRealFake {
	t.Helper()
	if opt.UUID == "" {
		opt.UUID = a4cFakeUUID
	}
	if opt.MetaStatus == 0 {
		opt.MetaStatus = http.StatusOK
	}
	if opt.MetaBody == "" {
		opt.MetaBody = a4cMetaJSON(a4cIndexName, opt.UUID, nil)
	}
	if opt.MappingStatus == 0 {
		opt.MappingStatus = http.StatusOK
	}
	if opt.MappingBody == "" {
		opt.MappingBody = a4cMappingJSON(a4cIndexName, map[string]int{
			a4cContentFld: a4cDims, coretypes.FieldQAContentVector: a4cDims,
		})
	}
	if opt.ContentStatus == 0 {
		opt.ContentStatus = http.StatusOK
	}
	if opt.ContentBody == "" {
		opt.ContentBody = a4cSearchHitBody
	}
	if opt.QAStatus == 0 {
		opt.QAStatus = http.StatusBadRequest
	}
	if opt.QABody == "" {
		opt.QABody = a4cESErrorBody
	}
	f := &a4cRealFake{arrive: make(chan struct{}, 1), gate: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r.Method + " " + r.URL.Path)
		if opt.BlockFirst {
			select {
			case f.arrive <- struct{}{}:
			default:
			}
			<-f.blockCh() // 由 t.Cleanup(f.release) 释放，避免服务关闭时永久阻塞
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/"+a4cIndexName:
			if opt.MetaLocation != "" {
				w.Header().Set("Location", opt.MetaLocation)
			}
			a4cWriteJSON(w, opt.MetaStatus, opt.MetaBody)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/"+a4cIndexName+"/_mapping/field/"):
			a4cWriteJSON(w, opt.MappingStatus, opt.MappingBody)
		case r.Method == http.MethodPost && r.URL.Path == "/"+a4cIndexName+"/_search":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if a4cBodyUsesQAField(body) {
				a4cWriteJSON(w, opt.QAStatus, opt.QABody)
				return
			}
			a4cWriteJSON(w, opt.ContentStatus, opt.ContentBody)
		default:
			a4cWriteJSON(w, http.StatusNotFound, `{"error":{"type":"unexpected_path"},"status":404}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(f.release) // LIFO：先释放阻塞，再关闭服务
	return f
}

func (f *a4cRealFake) blockCh() <-chan struct{} { return f.gate }

func (f *a4cRealFake) release() {
	select {
	case <-f.gate:
	default:
		close(f.gate)
	}
}

func (f *a4cRealFake) record(item string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served = append(f.served, item)
}

func (f *a4cRealFake) servedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.served)
}

func (f *a4cRealFake) servedCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.served...)
}

func (f *a4cRealFake) url() string { return f.srv.URL }

func a4cMetaJSON(index, uuid string, aliases map[string]any) string {
	if aliases == nil {
		aliases = map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{index: map[string]any{
		"settings": map[string]any{"index": map[string]any{"uuid": uuid}},
		"aliases":  aliases,
	}})
	return string(b)
}

func a4cMappingJSON(index string, fields map[string]int) string {
	mappings := map[string]any{}
	for name, dims := range fields {
		mappings[name] = map[string]any{
			"full_name": name,
			"mapping": map[string]any{name: map[string]any{
				"type": "dense_vector", "dims": dims, "index": true, "similarity": "cosine",
			}},
		}
	}
	b, _ := json.Marshal(map[string]any{index: map[string]any{"mappings": mappings}})
	return string(b)
}

func a4cBodyUsesQAField(body map[string]any) bool {
	q, ok := body["query"].(map[string]any)
	if !ok {
		return false
	}
	ss, ok := q["script_score"].(map[string]any)
	if !ok {
		return false
	}
	script, ok := ss["script"].(map[string]any)
	if !ok {
		return false
	}
	src, _ := script["source"].(string)
	return strings.Contains(src, "'"+coretypes.FieldQAContentVector+"'")
}

func a4cOfflineTarget(target string) a4RealTarget {
	return a4RealTarget{
		URL: target, Index: a4cIndexName, UUID: a4cFakeUUID, KB: "a4-kb", Dims: a4cDims,
	}
}

// a4cRequireIncomplete 断言一次运行“不完整”，且绝不会同时标成完成观察。
func a4cRequireIncomplete(t *testing.T, v a4cVerdict, want string) {
	t.Helper()
	if !v.Incomplete || v.Observed {
		t.Fatalf("observed=%t incomplete=%t reason=%q, want incomplete and not observed", v.Observed, v.Incomplete, v.Reason)
	}
	if want != "" && !strings.Contains(v.Reason, want) {
		t.Fatalf("reason = %q, want substring %q", v.Reason, want)
	}
}

// TestA4ComponentRealBranch_RecordsRequestInTransport 是复核报告 overlay 复现的直接对应：
// 真实分支的请求必须由发送传输层记录（此前 obs 恒为 0，导致“no request”提前退出）。
func TestA4ComponentRealBranch_RecordsRequestInTransport(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if v.Incomplete {
		t.Fatalf("expected observable run, got incomplete: %s", v.Reason)
	}
	kinds := obs.kinds()
	want := []string{"meta", "mapping", "search", "search"}
	if len(kinds) != len(want) {
		t.Fatalf("recorded kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("recorded kinds = %v, want %v", kinds, want)
		}
	}
	if v.Requests != 4 || obs.count() != 4 || f.servedCount() != 4 {
		t.Fatalf("requests: verdict=%d obs=%d served=%d, want 4/4/4", v.Requests, obs.count(), f.servedCount())
	}
	if !v.IndexOK || !v.AliasesEmpty || !v.UUIDMatch || !v.ContentOK || v.ContentDims != a4cDims || !v.QAExists {
		t.Fatalf("precheck结果不完整: %s", v.Summary())
	}
	if !strings.Contains(v.Observation, "status=400") || !strings.Contains(v.Observation, "search_phase_execution_exception") {
		t.Fatalf("qa observation = %q, want status=400 + search_phase_execution_exception", v.Observation)
	}
	// 这只是组件层观察，不是 A-4 结论：不写“字段缺失已证明”、不写“A-4 通过”。
	t.Logf("component-level observation (NOT an A-4 conclusion): %s", v.Summary())
}

// TestA4ComponentRealBranch_ReviewReproRecordsRequest 保留复核报告 repro_test.go 的断言原样：
// 真实分支（real=true）对裸 httptest 发 1 次检索后，传输层必须记录 1 条请求。
// 复核时该断言 exit 1（real branch recorded 0 requests, want 1）；修复后必须 exit 0。
func TestA4ComponentRealBranch_ReviewReproRecordsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"took":1,"timed_out":false,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0},"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
	}))
	defer srv.Close()

	obs := &a4Obs{}
	c := a4cBuild(t, srv.URL, "fake", a4cDims, "", "", true, obs, a4cMaxRequests)
	if _, err := c.retrieve(context.Background(), c.Content); err != nil {
		t.Fatalf("offline fake failed: %v", err)
	}
	if n := obs.count(); n != 1 {
		t.Fatalf("real branch recorded %d requests, want 1", n)
	}
}

// TestA4ComponentRealBranch_BudgetRefusedBeforeSend 上限在发送前生效：超限请求不会到达服务端。
func TestA4ComponentRealBranch_BudgetRefusedBeforeSend(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, 2)
	if !v.Incomplete {
		t.Fatalf("expected incomplete when budget=2, got %s", v.Summary())
	}
	if v.Requests != 2 || f.servedCount() != 2 {
		t.Fatalf("requests: verdict=%d served=%d, want 2/2", v.Requests, f.servedCount())
	}
	if obs.refusedCount() == 0 {
		t.Fatal("budget refusal was not recorded")
	}
	if !strings.Contains(v.Reason, "budget") && !strings.Contains(v.Reason, "mapping") {
		t.Fatalf("reason = %q, want budget-class truncation", v.Reason)
	}
}

// TestA4ComponentRealBranch_NoRetryOn503 关闭 SDK 自动重试：503 只发一次。
func TestA4ComponentRealBranch_NoRetryOn503(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{
		MetaStatus: http.StatusServiceUnavailable,
		MetaBody:   `{"error":{"type":"unavailable_shards_exception"},"status":503}`,
	})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if !v.Incomplete {
		t.Fatalf("5xx must be incomplete, got %s", v.Summary())
	}
	if got := f.servedCount(); got != 1 {
		t.Fatalf("503 was retried: served=%d, want 1", got)
	}
	if obs.count() != 1 || !strings.Contains(v.Reason, "status=503") {
		t.Fatalf("reason = %q (requests=%d), want status=503", v.Reason, obs.count())
	}
}

// TestA4ComponentRealBranch_NoRedirectFollow 3xx 在传输层被拒绝，且不跟随重定向。
func TestA4ComponentRealBranch_NoRedirectFollow(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{
		MetaStatus:   http.StatusFound,
		MetaBody:     `{"redirected":true}`,
		MetaLocation: "/" + a4cIndexName + "?redirected=1",
	})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if !v.Incomplete {
		t.Fatalf("redirect must be incomplete, got %s", v.Summary())
	}
	if got := f.servedCount(); got != 1 {
		t.Fatalf("redirect was followed: served=%d (%v), want 1", got, f.servedCopy())
	}
	recs := obs.records()
	if len(recs) != 1 || recs[0].Status != http.StatusFound || recs[0].Err != "redirect" {
		t.Fatalf("records = %+v, want one 302 marked redirect", recs)
	}
	if !strings.Contains(v.Reason, "redirect") {
		t.Fatalf("reason = %q, want redirect", v.Reason)
	}
}

// TestA4ComponentRealBranch_NonZeroOneHotVector 真实分支实际发送 1024 维非零 one-hot 向量。
func TestA4ComponentRealBranch_NonZeroOneHotVector(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if v.Incomplete {
		t.Fatalf("expected observable run, got incomplete: %s", v.Reason)
	}
	_, _, bodies := obs.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("recorded search bodies = %d, want 2", len(bodies))
	}
	for i, body := range bodies {
		a4cAssertOneHot(t, a4cSentVector(t, body))
		_ = i
	}
	if v.EmbCalls != 2 {
		t.Fatalf("fake embedding calls = %d, want 2 (one per query)", v.EmbCalls)
	}
}

// TestA4ComponentRealBranch_ControlQueryMustSucceed 对照查询必须 200 且有命中，否则不完整且不发 QA 查询。
func TestA4ComponentRealBranch_ControlQueryMustSucceed(t *testing.T) {
	cases := []struct {
		name string
		opt  a4cRealFakeOpt
		want string
	}{
		{"no hits", a4cRealFakeOpt{ContentBody: a4cSearchEmptyBody}, "no_hits"},
		{"content 400", a4cRealFakeOpt{ContentStatus: http.StatusBadRequest}, "status=400"},
		{"content 500", a4cRealFakeOpt{ContentStatus: http.StatusInternalServerError,
			ContentBody: `{"error":{"type":"internal"},"status":500}`}, "server_5xx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, tc.want)
			if obs.kinds()[len(obs.kinds())-1] != "search" || obs.count() != 3 {
				t.Fatalf("kinds=%v count=%d, want stop right after control search", obs.kinds(), obs.count())
			}
			if f.servedCount() != 3 {
				t.Fatalf("served=%d, want 3 (no qa request)", f.servedCount())
			}
		})
	}
}

// TestA4ComponentRealBranch_IndexPrecheckFailClosed 物理索引预检 fail closed：不满足即不发查询。
func TestA4ComponentRealBranch_IndexPrecheckFailClosed(t *testing.T) {
	cases := []struct {
		name string
		opt  a4cRealFakeOpt
		want string
	}{
		{"uuid mismatch", a4cRealFakeOpt{UUID: "other-uuid-999999"}, "uuid_mismatch"},
		{"uuid case mismatch", a4cRealFakeOpt{UUID: strings.ToUpper(a4cFakeUUID)}, "uuid_mismatch"},
		{"alias present", a4cRealFakeOpt{MetaBody: a4cMetaJSON(a4cIndexName, a4cFakeUUID, map[string]any{"a4-alias": map[string]any{}})}, "alias_present"},
		{"two targets", a4cRealFakeOpt{MetaBody: `{"a4-component-index":{"settings":{"index":{"uuid":"` + a4cFakeUUID + `"}},"aliases":{}},"other":{"aliases":{}}}`}, "targets=2"},
		{"404", a4cRealFakeOpt{MetaStatus: http.StatusNotFound,
			MetaBody: `{"error":{"type":"index_not_found_exception"},"status":404}`}, "status=404"},
		{"unparsable", a4cRealFakeOpt{MetaBody: `not-json`}, "unparsable"},
		{"uuid missing", a4cRealFakeOpt{MetaBody: `{"a4-component-index":{"settings":{"index":{}},"aliases":{}}}`}, "uuid_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, tc.want)
			if kinds := obs.kinds(); len(kinds) != 1 || kinds[0] != "meta" {
				t.Fatalf("kinds=%v, want only meta precheck", kinds)
			}
			if f.servedCount() != 1 {
				t.Fatalf("served=%d, want 1 (no mapping/search)", f.servedCount())
			}
		})
	}
}

// TestA4ComponentRealBranch_IndexPrecheckUUIDExactComparison 目标 UUID 必须完整精确
// 相等：只有逐字节相同才继续 mapping/search；大小写、前缀、后缀、其他值一律
// fail closed，只发 1 条只读 GET，且证据不得泄露 UUID 本身。
func TestA4ComponentRealBranch_IndexPrecheckUUIDExactComparison(t *testing.T) {
	t.Run("same uuid continues", func(t *testing.T) {
		f := a4cNewRealFake(t, a4cRealFakeOpt{})
		obs := &a4Obs{}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
		if !v.UUIDMatch || !v.IndexOK {
			t.Fatalf("uuid_match=%t index_ok=%t reason=%q, want exact uuid accepted", v.UUIDMatch, v.IndexOK, v.Reason)
		}
		if kinds := obs.kinds(); len(kinds) < 2 || kinds[0] != "meta" || kinds[1] != "mapping" {
			t.Fatalf("kinds=%v, want meta then mapping", kinds)
		}
		if v.Incomplete || !v.Observed {
			t.Fatalf("observed=%t incomplete=%t reason=%q, want the run to reach a decision", v.Observed, v.Incomplete, v.Reason)
		}
	})

	cases := []struct {
		name string
		uuid string
	}{
		{"case only", strings.ToUpper(a4cFakeUUID)},
		{"prefix", a4cFakeUUID[:len(a4cFakeUUID)-1]},
		{"suffix", a4cFakeUUID + "0"},
		{"other uuid", "other-uuid-999999"},
		{"upper with trailing space", strings.ToUpper(a4cFakeUUID) + " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, a4cRealFakeOpt{UUID: tc.uuid})
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, "uuid_mismatch")
			if v.UUIDMatch || v.IndexOK {
				t.Fatalf("uuid_match=%t index_ok=%t, want both false", v.UUIDMatch, v.IndexOK)
			}
			if kinds := obs.kinds(); len(kinds) != 1 || kinds[0] != "meta" {
				t.Fatalf("kinds=%v, want only meta precheck", kinds)
			}
			if obs.count() != 1 || f.servedCount() != 1 {
				t.Fatalf("requests=%d served=%d, want 1/1 (no mapping/search)", obs.count(), f.servedCount())
			}
			if leaked := strings.ToLower(v.Summary()); strings.Contains(leaked, strings.ToLower(a4cFakeUUID)) {
				t.Fatalf("summary leaks uuid: %s", v.Summary())
			}
		})
	}
}

// TestA4ComponentRealBranch_MappingPrecheckFailClosed 向量字段维度必须与配置一致。
func TestA4ComponentRealBranch_MappingPrecheckFailClosed(t *testing.T) {
	cases := []struct {
		name string
		opt  a4cRealFakeOpt
		want string
	}{
		{"content dims mismatch", a4cRealFakeOpt{MappingBody: a4cMappingJSON(a4cIndexName, map[string]int{
			a4cContentFld: 512, coretypes.FieldQAContentVector: 512})}, "content_vector_dims=512"},
		{"content missing", a4cRealFakeOpt{MappingBody: a4cMappingJSON(a4cIndexName, map[string]int{
			coretypes.FieldQAContentVector: a4cDims})}, "content_vector_missing"},
		{"mapping 500", a4cRealFakeOpt{MappingStatus: http.StatusInternalServerError,
			MappingBody: `{"error":{"type":"internal"},"status":500}`}, "status=500"},
		{"mapping unparsable", a4cRealFakeOpt{MappingBody: `[]`}, "unparsable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, tc.want)
			if f.servedCount() != 2 {
				t.Fatalf("served=%d, want 2 (meta+mapping only)", f.servedCount())
			}
		})
	}
}

// TestA4ComponentRealBranch_Unknown404IsIncomplete 复现复核报告 overlay 的断言：
// QA 返回 404 + index_not_found_exception 必须不完整，不得当作“预期 4xx 已观察”。
func TestA4ComponentRealBranch_Unknown404IsIncomplete(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{
		QAStatus: http.StatusNotFound,
		QABody:   `{"error":{"type":"index_not_found_exception","reason":"missing"},"status":404}`,
	})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	a4cRequireIncomplete(t, v, "target_missing")
	if v.QA.Kind != a4cProbeTargetMissing || v.QA.Status != http.StatusNotFound {
		t.Fatalf("qa probe = %s, want kind=target_missing status=404", v.QA.Summary())
	}
	if v.QA.Complete() {
		t.Fatal("target_missing must not be Complete()")
	}
	if v.Requests != 4 {
		t.Fatalf("requests = %d, want 4 (all sent, none retried)", v.Requests)
	}
}

// TestA4ComponentRealBranch_UndeclaredQAFailureIsIncomplete 只有声明表里那一组
// (400, search_phase_execution_exception, script_exception) 可判定，其余 4xx 一律不完整。
func TestA4ComponentRealBranch_UndeclaredQAFailureIsIncomplete(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantKind a4cProbeKind
	}{
		{"unknown error type", http.StatusBadRequest,
			`{"error":{"type":"illegal_argument_exception"},"status":400}`, a4cProbeUndeclared4xx},
		{"search_phase without root cause", http.StatusBadRequest,
			`{"error":{"type":"search_phase_execution_exception"},"status":400}`, a4cProbeUndeclared4xx},
		{"wrong root cause", http.StatusBadRequest,
			`{"error":{"root_cause":[{"type":"illegal_argument_exception"}],` +
				`"type":"search_phase_execution_exception"},"status":400}`, a4cProbeUndeclared4xx},
		{"unprocessable entity", http.StatusUnprocessableEntity,
			`{"error":{"type":"document_parsing_exception"},"status":422}`, a4cProbeUndeclared4xx},
		{"missing error type", http.StatusBadRequest,
			`{"error":{"reason":"x"},"status":400}`, a4cProbeUnparsable},
		{"404 with other type", http.StatusNotFound,
			`{"error":{"type":"resource_not_found_exception"},"status":404}`, a4cProbeTargetMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, a4cRealFakeOpt{QAStatus: tc.status, QABody: tc.body})
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, "qa:")
			if v.QA.Kind != tc.wantKind {
				t.Fatalf("qa kind = %s, want %s (%s)", v.QA.Kind, tc.wantKind, v.QA.Summary())
			}
			if v.QA.Complete() {
				t.Fatalf("kind %s must not be Complete()", v.QA.Kind)
			}
			if v.Requests != 4 || f.servedCount() != 4 {
				t.Fatalf("requests=%d served=%d, want 4/4", v.Requests, f.servedCount())
			}
		})
	}
}

// TestA4ComponentRealBranch_QAMissingFieldShapeIsObservable 与 L0 形状一致的诊断观察：
// 索引缺 qa_content_vector 且 QA 查询被脚本拒绝。只记录状态与安全类型，不归因缺字段。
func TestA4ComponentRealBranch_QAMissingFieldShapeIsObservable(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{
		MappingBody: a4cMappingJSON(a4cIndexName, map[string]int{a4cContentFld: a4cDims}),
	})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if !v.Observed || v.Incomplete {
		t.Fatalf("expected a complete observation, got %s", v.Summary())
	}
	if v.QAExists {
		t.Fatal("qa_exist = true, want false for this fixture")
	}
	if v.QA.Kind != a4cProbeScriptRejected || v.QA.Status != http.StatusBadRequest ||
		v.QA.ErrType != a4cErrTypeSearchPhase || v.QA.RootCause != a4cErrTypeScript {
		t.Fatalf("qa probe = %s, want script_rejected 400 search_phase_execution_exception/script_exception", v.QA.Summary())
	}
	if v.QA.Note != a4cNoteCauseNotAttributed {
		t.Fatalf("qa note = %q, want %q", v.QA.Note, a4cNoteCauseNotAttributed)
	}
	if !strings.Contains(v.Observation, "qa_attribution=none") {
		t.Fatalf("observation = %q, want explicit no-attribution marker", v.Observation)
	}
	if !strings.Contains(v.Summary(), "qa_exist=false") {
		t.Fatalf("summary = %q, want qa_exist=false (mapping evidence)", v.Summary())
	}
	if v.Requests != 4 {
		t.Fatalf("requests = %d, want 4", v.Requests)
	}
}

// TestA4ComponentRealBranch_QASuccessIsCompleteObservation 正常例：两条查询都 200 且有命中。
func TestA4ComponentRealBranch_QASuccessIsCompleteObservation(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{QAStatus: http.StatusOK, QABody: a4cSearchHitBody})
	obs := &a4Obs{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
	if !v.Observed || v.Incomplete {
		t.Fatalf("expected complete observation, got %s", v.Summary())
	}
	if v.Control.Kind != a4cProbeOK || v.Control.Hits != 1 || v.QA.Kind != a4cProbeOK || v.QA.Hits != 1 {
		t.Fatalf("probes = control[%s] qa[%s], want ok/1hit for both", v.Control.Summary(), v.QA.Summary())
	}
	if v.Reason != "" {
		t.Fatalf("reason = %q, want empty on complete observation", v.Reason)
	}
	if !strings.Contains(v.Summary(), "observed=true") {
		t.Fatalf("summary = %q, want observed=true", v.Summary())
	}
}

// TestA4ComponentRealBranch_IncompleteNeverObserved 不完整永远不得被报告为完成观察。
func TestA4ComponentRealBranch_IncompleteNeverObserved(t *testing.T) {
	cases := []struct {
		name string
		opt  a4cRealFakeOpt
	}{
		{"index 404", a4cRealFakeOpt{MetaStatus: http.StatusNotFound,
			MetaBody: `{"error":{"type":"index_not_found_exception"},"status":404}`}},
		{"mapping 500", a4cRealFakeOpt{MappingStatus: http.StatusInternalServerError,
			MappingBody: `{"error":{"type":"internal"},"status":500}`}},
		{"control 500", a4cRealFakeOpt{ContentStatus: http.StatusInternalServerError,
			ContentBody: `{"error":{"type":"internal"},"status":500}`}},
		{"control no hits", a4cRealFakeOpt{ContentBody: a4cSearchEmptyBody}},
		{"qa 401", a4cRealFakeOpt{QAStatus: http.StatusUnauthorized,
			QABody: `{"error":{"type":"security_exception"},"status":401}`}},
		{"qa 503", a4cRealFakeOpt{QAStatus: http.StatusServiceUnavailable,
			QABody: `{"error":{"type":"unavailable"},"status":503}`}},
		{"qa unknown 404", a4cRealFakeOpt{QAStatus: http.StatusNotFound,
			QABody: `{"error":{"type":"index_not_found_exception"},"status":404}`}},
		{"qa unparsable", a4cRealFakeOpt{QAStatus: http.StatusBadRequest, QABody: `not-json`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := a4cNewRealFake(t, tc.opt)
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, "")
			if strings.Contains(v.Summary(), "observed=true") {
				t.Fatalf("incomplete run reported as observed: %s", v.Summary())
			}
		})
	}
}

// TestA4ComponentRealBranch_AuthIsIncomplete 认证/限流错误必须是不完整，不得当作预期 4xx。
func TestA4ComponentRealBranch_AuthIsIncomplete(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			f := a4cNewRealFake(t, a4cRealFakeOpt{
				ContentStatus: status,
				ContentBody:   `{"error":{"type":"security_exception"},"status":` + fmt.Sprint(status) + `}`,
			})
			obs := &a4Obs{}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), obs, a4cMaxRequests)
			a4cRequireIncomplete(t, v, "auth_or_throttle")
		})
	}
}

// TestA4ComponentRealBranch_CancelIsIncomplete 取消：请求已发但观察不完整，测试不得当成功。
func TestA4ComponentRealBranch_CancelIsIncomplete(t *testing.T) {
	f := a4cNewRealFake(t, a4cRealFakeOpt{BlockFirst: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-f.arrive:
			cancel()
		case <-time.After(3 * time.Second):
		}
	}()
	start := time.Now()
	v := a4cRunRealProbe(t, ctx, a4cOfflineTarget(f.url()), &a4Obs{}, a4cMaxRequests)
	a4cRequireIncomplete(t, v, "")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancel took %v, want bounded return", elapsed)
	}
	if v.Requests > 1 {
		t.Fatalf("requests after cancel = %d, want <=1", v.Requests)
	}
}
