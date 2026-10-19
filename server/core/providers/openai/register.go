// Package openai 注册 OpenAI Provider 的生产 builder hooks。
// Fix 6: 恢复实际支持的 OpenAI 生产 builder，使得 OpenAI Profile 不再返回 ErrUnknownProvider。
//
// 使用 OpenAI 兼容协议（与 bailian 相同底层），但注册独立的 provider name "openai"。
// 生产导入路径触发 init()。
package openai

import (
	"context"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// ProviderOpenAI 是 OpenAI Provider 名称常量。
const ProviderOpenAI = "openai"

// init 注册 OpenAI Provider 的构造器到 providers 包的全局钩子。
// 当 Registry 构建角色时遇到 ProviderName=="openai"，会调用这些钩子。
func init() {
	_ = providers.RegisterChatBuilder(ProviderOpenAI, BuildChat)
	_ = providers.RegisterEmbedBuilder(ProviderOpenAI, BuildEmbed)
	_ = providers.RegisterRerankBuilder(ProviderOpenAI, BuildRerank)
}

// BuildChat 构造 OpenAI Chat 角色适配器。
// 使用 providers.BuildChatRole（底层 chatopenai.NewChatModel），与百炼共享相同协议。
func BuildChat(ctx context.Context, cfg providers.BuildConfig) (providers.ChatRole, error) {
	return providers.BuildChatRole(ctx, cfg)
}

// BuildEmbed 构造 OpenAI Embedding 角色适配器。
// 使用 providers.BuildEmbedRole（底层 embopenai.NewEmbedder），与百炼共享相同协议。
func BuildEmbed(ctx context.Context, cfg providers.BuildConfig) (providers.EmbedRole, error) {
	return providers.BuildEmbedRole(ctx, cfg)
}

// BuildRerank 构造 OpenAI Rerank 角色适配器。
// 使用 providers.BuildRerankRole（底层 rerank.Rerank），复用已验收校验链。
func BuildRerank(ctx context.Context, cfg providers.BuildConfig) (providers.RerankRole, error) {
	return providers.BuildRerankRole(ctx, cfg)
}
