package bailian

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// rerankAdapter 包装 providers.RerankRole，附加预算接入（B1）。
type rerankAdapter struct {
	inner  providers.RerankRole
	budget *providers.ProcessBudget
}

// 编译期断言。
var _ providers.RerankRole = (*rerankAdapter)(nil)

// BuildRerank 构造百炼 Rerank 角色适配器。
//
// 走 OpenAI 兼容路径 /compatible-api/v1/reranks，模型 qwen3-rerank。
// 请求体为扁平结构 {model, query, documents, top_n}，与既有 rerank.Rerank 格式一致。
// 全部已验收校验链（index/score/NaN/Inf/重复/越界/10MB/30s）由 core/rerank.Rerank 保留，
// 本适配器仅附加 Profile 身份声明与预算接入。
func BuildRerank(ctx context.Context, cfg providers.BuildConfig) (providers.RerankRole, error) {
	if cfg.Profile == nil {
		return nil, fmt.Errorf("bailian rerank: nil ProviderProfile")
	}
	if cfg.Transport == nil {
		return nil, fmt.Errorf("bailian rerank: nil Transport")
	}
	// 直接使用 providers.BuildRerankRole：底层 rerank.Rerank 的 RerankConfig.URL
	// 指向 Profile.Endpoint（百炼 /compatible-api/v1/reranks），请求体结构兼容。
	rr, err := providers.BuildRerankRole(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("bailian rerank: %w", err)
	}
	return &rerankAdapter{inner: rr, budget: cfg.Budget}, nil
}

// Rerank 调用底层 Rerank 并附加预算结算（B1）。
// Fix 2e: 本地校验（topK、docs、query）在 Reserve 之前完成。
func (a *rerankAdapter) Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error) {
	// Fix 2e: 本地校验前置（在 Reserve 之前）
	if topK <= 0 {
		return nil, fmt.Errorf("bailian rerank: invalid topK: %d", topK)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("bailian rerank: empty docs")
	}
	if query == "" {
		return nil, fmt.Errorf("bailian rerank: empty query")
	}

	// B1: 预算接入
	lease, err := a.reserveBudget(ctx, estimateRerankTokens(query, docs))
	if err != nil {
		return nil, err
	}
	if lease != nil {
		lease.Commit()
	}

	result, err := a.inner.Rerank(ctx, query, docs, topK)
	if err != nil {
		if lease != nil {
			lease.Abort(err)
		}
		return nil, err
	}
	// Rerank 无 usage 返回：保守保留预留
	if lease != nil {
		lease.Finish(nil)
	}
	return result, nil
}

// reserveBudget 从 ctx 复用已有 RequestBudget，或创建新的。Fix 2a: owner 复用。
func (a *rerankAdapter) reserveBudget(ctx context.Context, est *providers.TokenEstimate) (*providers.AttemptLease, error) {
	if a.budget == nil {
		return nil, nil
	}
	// Fix 2a: 优先从 ctx 复用入口 owner 构造的 RequestBudget
	rb := providers.RequestBudgetFromContext(ctx)
	if rb == nil {
		var err error
		rb, err = a.budget.NewRequestBudget(ctx)
		if err != nil {
			return nil, fmt.Errorf("bailian rerank: create request budget: %w", err)
		}
	}
	lease, err := rb.ReserveCtx(ctx, est)
	if err != nil {
		return nil, fmt.Errorf("bailian rerank: budget reserve: %w", err)
	}
	return lease, nil
}

// estimateRerankTokens 从 query 和文档估算 token 数。
// Fix 7: 防御性处理 nil doc。
func estimateRerankTokens(query string, docs []*schema.Document) *providers.TokenEstimate {
	var inputChars int64
	inputChars += int64(len(query))
	for _, d := range docs {
		if d != nil {
			inputChars += int64(len(d.Content))
		}
	}
	return &providers.TokenEstimate{
		InputTokens: inputChars/4 + 1,
		MaxOutput:   0, // Rerank 无输出 token
	}
}
