package bailian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 百炼 Embedding 适配层错误。
var (
	ErrEmbedCountMismatch   = errors.New("bailian embed: response count mismatch")
	ErrEmbedDimMismatch     = errors.New("bailian embed: dimension mismatch")
	ErrEmbedNaN             = errors.New("bailian embed: NaN value in embedding vector")
	ErrEmbedInf             = errors.New("bailian embed: Inf value in embedding vector")
	ErrEmbedUsageMissing    = errors.New("bailian embed: usage info missing from response")
	ErrEmbedNilProfile      = errors.New("bailian embed: nil ProviderProfile")
	ErrEmbedNilTransport    = errors.New("bailian embed: nil Transport")
	ErrEmbedIndexMissing    = errors.New("bailian embed: index field missing in response data item")
	ErrEmbedIndexDuplicate  = errors.New("bailian embed: duplicate index in response")
	ErrEmbedIndexOutOfRange = errors.New("bailian embed: index out of range")
	ErrEmbedIndexDisorder   = errors.New("bailian embed: indices not in request order")
)

// validatedEmbedRole 包装 EmbedRole，附加响应验证与预算接入（B1）。
// Fix 2d: 通过 lastUsage 字段从 validationTransport 回传真实 usage。
type validatedEmbedRole struct {
	inner       providers.EmbedRole
	profile     *providers.ProviderProfile
	expectedDim int
	budget      *providers.ProcessBudget

	// Fix 2d: usage 回传通道（validationTransport 写入，EmbedStrings 读取）
	usageMu   sync.Mutex
	lastUsage *embeddingUsage
}

// embeddingUsage 是从原始响应解析的 usage 信息。
type embeddingUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// 编译期断言。
var _ providers.EmbedRole = (*validatedEmbedRole)(nil)

// BuildEmbed 构造百炼 Embedding 角色适配器。
//
// 走 OpenAI 兼容协议 /compatible-mode/v1/embeddings；
// 候选模型 text-embedding-v4 默认 1024 维。
// B3: 在 SDK 丢失元数据前验证原始 index。
// 通过 validationTransport 拦截原始 HTTP 响应，验证 index presence/唯一性/范围/顺序。
func BuildEmbed(ctx context.Context, cfg providers.BuildConfig) (providers.EmbedRole, error) {
	if cfg.Profile == nil {
		return nil, ErrEmbedNilProfile
	}
	if cfg.Transport == nil {
		return nil, ErrEmbedNilTransport
	}

	// B3: 包装 Transport 以拦截原始响应并验证 index
	// Fix 2d: 传入 owner 以便回传 usage
	role := &validatedEmbedRole{
		profile:     cfg.Profile.Clone(),
		expectedDim: cfg.Profile.Dimensions,
		budget:      cfg.Budget,
	}
	validatedCfg := cfg
	validatedCfg.Transport = wrapTransportWithValidation(cfg.Transport, role)

	inner, err := providers.BuildEmbedRole(ctx, validatedCfg)
	if err != nil {
		return nil, fmt.Errorf("bailian embed: %w", err)
	}
	role.inner = inner
	return role, nil
}

// EmbedStrings 调用底层 Embedder 并验证响应，附加预算结算（B1）。
// Fix 2d: 从 validationTransport 回传的 usage 结算，而非始终 Finish(nil)。
func (v *validatedEmbedRole) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error) {
	// Fix 2e: 本地校验前置（在 Reserve 之前）
	if len(texts) == 0 {
		return nil, fmt.Errorf("bailian embed: empty texts")
	}

	// 清除上次 usage
	v.usageMu.Lock()
	v.lastUsage = nil
	v.usageMu.Unlock()

	// B1: 预算接入
	lease, err := v.reserveBudget(ctx, estimateEmbedTokens(texts))
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	result, err := v.inner.EmbedStrings(ctx, texts, opts...)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	if err := validateEmbeddingResponse(result, texts, v.expectedDim); err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	// Fix 2d: 从 validationTransport 回传的 usage 结算
	if lease != nil {
		v.usageMu.Lock()
		u := v.lastUsage
		v.usageMu.Unlock()
		if u != nil && u.PromptTokens > 0 {
			lease.Finish(&model.TokenUsage{
				PromptTokens:     u.PromptTokens,
				CompletionTokens: u.CompletionTokens,
				TotalTokens:      u.TotalTokens,
			})
		} else {
			lease.Finish(nil) // usage unknown → 保守保留预留
		}
	}
	return result, nil
}

// Dimensions 返回 Profile 声明的输出维度。
func (v *validatedEmbedRole) Dimensions() int { return v.expectedDim }

// ModelID 返回 Profile 声明的模型身份。
func (v *validatedEmbedRole) ModelID() string { return v.profile.ModelID() }

// reserveBudget 从 ctx 复用已有 RequestBudget，或创建新的。Fix 2a: owner 复用。
func (v *validatedEmbedRole) reserveBudget(ctx context.Context, est *providers.TokenEstimate) (*providers.AttemptLease, error) {
	if v.budget == nil {
		return nil, nil
	}
	// Fix 2a: 优先从 ctx 复用入口 owner 构造的 RequestBudget
	rb := providers.RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = v.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("bailian embed: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("bailian embed: budget reserve: %w", err)
	}
	return lease, nil
}

// estimateEmbedTokens 从文本估算 token 数（保守估算：4 字符 ≈ 1 token）。
func estimateEmbedTokens(texts []string) *providers.TokenEstimate {
	var inputChars int64
	for _, t := range texts {
		inputChars += int64(len(t))
	}
	return &providers.TokenEstimate{
		InputTokens: inputChars/4 + 1,
		MaxOutput:   0, // Embedding 无输出 token
	}
}

// validateEmbeddingResponse 验证 Embedding 响应的完整性与有限性。
//
// 验证链：
//  1. 数量匹配：len(result) == len(texts)
//  2. 维度匹配：每个向量的 len == expectedDim（expectedDim > 0 时）
//  3. 有限值：每个元素不是 NaN 或 Inf
func validateEmbeddingResponse(result [][]float64, texts []string, expectedDim int) error {
	// 数量匹配
	if len(result) != len(texts) {
		return fmt.Errorf("%w: expected %d vectors, got %d", ErrEmbedCountMismatch, len(texts), len(result))
	}
	for i, vec := range result {
		// 维度匹配
		if expectedDim > 0 && len(vec) != expectedDim {
			return fmt.Errorf("%w: vector[%d] expected dim %d, got %d", ErrEmbedDimMismatch, i, expectedDim, len(vec))
		}
		// 有限值检查
		for j, val := range vec {
			if isNaN(val) {
				return fmt.Errorf("%w: vector[%d][%d]", ErrEmbedNaN, i, j)
			}
			if isInf(val) {
				return fmt.Errorf("%w: vector[%d][%d]", ErrEmbedInf, i, j)
			}
		}
	}
	return nil
}

func isNaN(v float64) bool { return v != v }
func isInf(v float64) bool { return v == v && v-v != 0 }

// ---------------------------------------------------------------------------
// B3: 原始响应 index 验证传输层
// ---------------------------------------------------------------------------

// validationTransport 包装 http.RoundTripper，拦截 Embedding 响应并验证 index。
// Fix 2d: 同时回传 usage 给 owner。
type validationTransport struct {
	base  http.RoundTripper
	owner *validatedEmbedRole // Fix 2d: 回传 usage
}

// wrapTransportWithValidation 包装 Transport 以拦截原始响应。
// Fix 7: nil Transport 安全处理（回退到 http.DefaultTransport），浅拷贝 Client 保留策略。
func wrapTransportWithValidation(t *providers.Transport, owner *validatedEmbedRole) *providers.Transport {
	client := t.Client()
	if client == nil {
		client = &http.Client{}
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	// Fix 7: 浅拷贝 Client 保留策略（Timeout, CheckRedirect 等）
	wrapped := &http.Client{
		Transport:     &validationTransport{base: base, owner: owner},
		Timeout:       client.Timeout,
		CheckRedirect: client.CheckRedirect,
		Jar:           client.Jar,
	}
	return providers.NewTransport(providers.WithHTTPClient(wrapped))
}

// RoundTrip 拦截 HTTP 响应，对 Embedding 响应验证 index 后传递给上层。
// Fix 2d: 同时提取 usage 并回传给 owner。
func (vt *validationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := vt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// 只拦截成功的 Embedding 响应
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	// 尝试解析并验证 index，同时提取 usage
	validationErr, usage := validateAndExtractUsage(body)
	if validationErr != nil {
		// 验证失败：返回错误响应（上层 SDK 会解析到错误）
		return nil, validationErr
	}

	// Fix 2d: 回传 usage 给 owner
	if usage != nil && vt.owner != nil {
		vt.owner.usageMu.Lock()
		vt.owner.lastUsage = usage
		vt.owner.usageMu.Unlock()
	}

	// 验证通过：重建响应体
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// rawEmbeddingResponse 是原始 Embedding API 响应的简化结构。
type rawEmbeddingResponse struct {
	Data  []rawEmbeddingData `json:"data"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type rawEmbeddingData struct {
	Index     *int      `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// validateRawEmbeddingIndices 验证原始 Embedding 响应的 index 字段。
//
// 验证链（B3）：
//  1. index presence：每个 data item 必须有 index 字段
//  2. index 唯一性：不允许重复 index
//  3. index 范围：0 <= index < len(data)
//  4. index 顺序：乱序可明确拒绝或可靠重排（此处选择拒绝）
//  5. usage 缺失允许保守 unknown，但不得退款为 0（由预算层处理）
func validateRawEmbeddingIndices(body []byte) error {
	err, _ := validateAndExtractUsage(body)
	return err
}

// validateAndExtractUsage 验证 index 并提取 usage（Fix 2d）。
func validateAndExtractUsage(body []byte) (error, *embeddingUsage) {
	var resp rawEmbeddingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// 解析失败：不拦截，让上层 SDK 处理
		return nil, nil
	}

	// Fix 2d: 提取 usage
	var usage *embeddingUsage
	if resp.Usage != nil && resp.Usage.PromptTokens > 0 {
		usage = &embeddingUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}

	if len(resp.Data) == 0 {
		return nil, usage // 空数据：不拦截
	}

	n := len(resp.Data)
	seen := make(map[int]bool, n)
	indices := make([]int, 0, n)

	for i, d := range resp.Data {
		// 1. index presence
		if d.Index == nil {
			return fmt.Errorf("%w: data[%d] has no index field", ErrEmbedIndexMissing, i), usage
		}
		idx := *d.Index
		// 2. index 唯一性
		if seen[idx] {
			return fmt.Errorf("%w: index %d appears multiple times", ErrEmbedIndexDuplicate, idx), usage
		}
		seen[idx] = true
		// 3. index 范围
		if idx < 0 || idx >= n {
			return fmt.Errorf("%w: index %d out of range [0, %d)", ErrEmbedIndexOutOfRange, idx, n), usage
		}
		indices = append(indices, idx)
	}

	// 4. index 顺序检查：如果乱序则拒绝（明确拒绝策略）
	for i := 1; i < len(indices); i++ {
		if indices[i] <= indices[i-1] {
			return fmt.Errorf("%w: index[%d]=%d <= index[%d]=%d (expected strictly ascending)",
				ErrEmbedIndexDisorder, i, indices[i], i-1, indices[i-1]), usage
		}
	}

	return nil, usage
}

// sortEmbeddingByIndex 按 index 重排 Embedding 结果（可选策略：可靠重排）。
// 当前未使用（选择拒绝策略），但保留供未来切换。
func sortEmbeddingByIndex(data []rawEmbeddingData) []rawEmbeddingData {
	sorted := make([]rawEmbeddingData, len(data))
	copy(sorted, data)
	sort.Slice(sorted, func(i, j int) bool {
		return *sorted[i].Index < *sorted[j].Index
	})
	return sorted
}
