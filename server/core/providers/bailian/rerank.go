package bailian

import (
	"context"
	"fmt"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// BuildRerank 构造百炼 Rerank 角色适配器。
//
// 走 OpenAI 兼容路径 /compatible-api/v1/reranks，模型 qwen3-rerank。
// 请求体为扁平结构 {model, query, documents, top_n}，与既有 rerank.Rerank 格式一致。
// 全部已验收校验链（index/score/NaN/Inf/重复/越界/10MB/30s）由 core/rerank.Rerank 保留，
// 本适配器仅附加 Profile 身份声明。
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
	return rr, nil
}
