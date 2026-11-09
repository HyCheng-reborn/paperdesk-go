package bailian

import (
	"github.com/wangle201210/go-rag/server/core/providers"
)

// init 注册百炼 Provider 的构造器到 providers 包的全局钩子。
// 当 Registry 构建角色时遇到 ProviderName=="bailian"，会调用这些钩子。
// B6: Register*Builder 现在返回 error，init 中必须处理。
func init() {
	// 忽略重复注册错误（init 只执行一次，且 provider 名称固定）
	_ = providers.RegisterChatBuilder(ProviderBailian, BuildChat)
	_ = providers.RegisterEmbedBuilder(ProviderBailian, BuildEmbed)
	_ = providers.RegisterRerankBuilder(ProviderBailian, BuildRerank)
}

// ProviderBailian 是百炼 Provider 名称常量。
const ProviderBailian = "bailian"
