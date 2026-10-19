package bailian

import (
	"github.com/wangle201210/go-rag/server/core/providers"
)

// init 注册百炼 Provider 的构造器到 providers 包的全局钩子。
// 当 Registry 构建角色时遇到 ProviderName=="bailian"，会调用这些钩子。
func init() {
	providers.RegisterChatBuilder(ProviderBailian, BuildChat)
	providers.RegisterEmbedBuilder(ProviderBailian, BuildEmbed)
	providers.RegisterRerankBuilder(ProviderBailian, BuildRerank)
}

// ProviderBailian 是百炼 Provider 名称常量。
const ProviderBailian = "bailian"
