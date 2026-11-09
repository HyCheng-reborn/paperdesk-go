package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"

	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// LocalFakeESTarget 是本机 fake ES：只服务"读取单个索引元数据"（GET /<index>），
// 承载一次性索引的 settings/mappings（含 go-rag-fingerprint），使
// vector.BindPhysicalTarget 能产出真实 TargetBinding，并让 guard 在每次
// embedding 前后重新读取同一目标。
//
// 它不连接、不模拟任何真实 ES 行为（写入、mapping 变更、kNN 查询均不支持），
// 因此由它产生的结论只覆盖"绑定/guard/身份比较链被真实执行"，
// **不覆盖任何真实 ES 事实**（草案 §2.1 方案 A 的"不可证明"列）。
type LocalFakeESTarget struct {
	Server      *httptest.Server
	Store       *vector.ESVectorStore
	Binding     *vector.TargetBinding
	Fingerprint *vector.Fingerprint

	indexName  string
	resourceID string
	dims       int
	similarity string
	reads      atomic.Int64
	otherCalls atomic.Int64

	// mu 保护 resourceID：handler 与 SetResourceID 可能并发。
	mu        sync.Mutex
	closeOnce sync.Once
}

// esDistance 把 fingerprint 相似度映射为 ES dense_vector 的 distance 名。
func esDistance(similarity string) string {
	switch similarity {
	case "dot":
		return "dot_product"
	case "euclidean":
		return "l2_norm"
	default:
		return similarity
	}
}

// NewLocalFakeESTarget 启动本机 fake ES，并对 (provider, model, dims, similarity)
// 执行一次真实的 BindPhysicalTarget。
//
// 参数与 Plan 共用同一组规范形式规则（复核第 3 轮 P2）：模型名不得带首尾空白，
// provider/similarity 必须是已小写的规范值。这样 Ready()、实际绑定与 fingerprint
// 摘要不会因为"某一层偷偷 TrimSpace/ToLower"而给出不同结论；非法值在这里直接
// 拒绝（属于本地可判定配置错误），而不是等到摘要/映射比较时失败。
func NewLocalFakeESTarget(spec TargetSpec, embeddingModel string, dims int) (*LocalFakeESTarget, error) {
	if spec.IndexName == "" {
		return nil, fmt.Errorf("%w: target.index_name is required", ErrNotConfigured)
	}
	if dims <= 0 {
		return nil, fmt.Errorf("%w: embed.dimensions must be a positive integer", ErrNotConfigured)
	}
	if err := requireCanonical("embed.model", embeddingModel); err != nil {
		return nil, err
	}
	if err := requireCanonicalLower("target.provider_name", spec.ProviderName); err != nil {
		return nil, err
	}
	if err := requireCanonicalLower("target.similarity", spec.Similarity); err != nil {
		return nil, err
	}
	fp, err := vector.NewFingerprint(vector.FingerprintVersion, embeddingModel, dims, spec.ProviderName, spec.Similarity)
	if err != nil {
		return nil, fmt.Errorf("probe: build local fingerprint: %w", err)
	}

	target := &LocalFakeESTarget{
		indexName:   spec.IndexName,
		resourceID:  "uuid-probe-local",
		dims:        dims,
		similarity:  spec.Similarity,
		Fingerprint: fp,
	}

	target.Server = httptest.NewServer(http.HandlerFunc(target.handle))
	store, err := vector.NewESVectorStore(&vector.ESConfig{Address: target.Server.URL}, dims)
	if err != nil {
		target.Close()
		return nil, fmt.Errorf("probe: new local es store: %w", err)
	}
	target.Store = store

	binding, err := vector.BindPhysicalTarget(context.Background(), store, spec.IndexName, fp)
	if err != nil {
		target.Close()
		return nil, fmt.Errorf("probe: bind local target: %w", err)
	}
	target.Binding = binding
	return target, nil
}

// handle 只回答 GET /<index>；其它请求计入 otherCalls 并返回 404/405，
// 便于离线证据证明"没有写入或建索引尝试"。
func (t *LocalFakeESTarget) handle(w http.ResponseWriter, r *http.Request) {
	// ES v8 客户端要求每个响应都带该头，否则拒绝解析。
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
	if r.Method != http.MethodGet {
		t.otherCalls.Add(1)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/"+t.indexName {
		t.otherCalls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	t.reads.Add(1)
	t.mu.Lock()
	resourceID := t.resourceID
	fingerprint := t.Fingerprint
	t.mu.Unlock()
	properties := map[string]any{}
	for _, field := range []string{"content_vector", "qa_content_vector"} {
		properties[field] = map[string]any{
			"type":       "dense_vector",
			"dims":       t.dims,
			"similarity": esDistance(t.similarity),
			"index":      true,
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		t.indexName: map[string]any{
			"settings": map[string]any{"index": map[string]any{"uuid": resourceID}},
			"mappings": map[string]any{
				"_meta":      map[string]any{"go-rag-fingerprint": fingerprint},
				"properties": properties,
			},
		},
	})
}

// IndexName 返回本机目标的物理名。
func (t *LocalFakeESTarget) IndexName() string {
	if t == nil {
		return ""
	}
	return t.indexName
}

// Reads 返回索引元数据读取次数（bind + 构建期校验 + 每次 embedding 前后 guard）。
func (t *LocalFakeESTarget) Reads() int64 {
	if t == nil {
		return 0
	}
	return t.reads.Load()
}

// OtherCalls 返回非"读取本索引"的请求次数（预期恒为 0）。
func (t *LocalFakeESTarget) OtherCalls() int64 {
	if t == nil {
		return 0
	}
	return t.otherCalls.Load()
}

// ApplyTo 把本机目标安装为 config.Config 的受控物理绑定输入：
// 同一 ES client、同一索引名、显式受控生命周期、真实 TargetBinding 与其 guard。
func (t *LocalFakeESTarget) ApplyTo(conf *config.Config) {
	if t == nil || conf == nil {
		return
	}
	conf.Client = t.Store.GetClient()
	conf.IndexName = t.indexName
	conf.ManagedResourceLifecycle = true
	conf.PhysicalBinding = t.Binding
	conf.VectorIOGuard = t.Binding.Guard()
}

// SetResourceID 改写后续读取到的索引 uuid（仅测试用）。
// 用于证明 guard 在探针入口每次发送前后都重新读取真实目标：
// 目标被替换后，下一次 embedding 必须在发送前被拒绝。
func (t *LocalFakeESTarget) SetResourceID(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.resourceID = id
	t.mu.Unlock()
}

// SetFingerprint 改写后续读取到的目标指纹（仅测试用）。
// 用于证明生产 embedding 入口的**完整**指纹比较真的在链上：
// 预检已经拒绝配置层面的 similarity 不一致后，这里从后端数据侧制造不一致。
func (t *LocalFakeESTarget) SetFingerprint(fp *vector.Fingerprint) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.Fingerprint = fp
	t.mu.Unlock()
}

// Close 关闭本机 HTTP 服务（幂等）。
func (t *LocalFakeESTarget) Close() {
	if t == nil || t.Server == nil {
		return
	}
	t.closeOnce.Do(func() { t.Server.Close() })
}
