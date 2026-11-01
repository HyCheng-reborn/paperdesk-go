//go:build !online

// 默认构建的端点能力声明。
//
// 远端能力不是调用方可覆盖的选项，而是**构建期**事实：默认构建里根本不存在
// 打开远端的路径（ChainOptions 只有 APIKey），因此"调用者忘了关掉开关"这类
// 依赖不成立。远端能力只存在于 online 构建（endpoint_policy_online.go），
// 且在那里仍受 HTTPS 与授权/凭据边界约束。
package probe

import (
	"fmt"
	"strings"
)

// remoteEndpointsAllowed 在默认构建中恒为 false。
const remoteEndpointsAllowed = false

// ChainOptions 是构造期凭据，**不包含任何远端开关**。
//
// 远端能力由构建标签决定（本文件 / endpoint_policy_online.go），
// 而不是由调用方参数决定：默认构建里根本不存在打开远端的字段，
// 因此"调用者忘了关开关"这类风险面不成立。
type ChainOptions struct {
	// APIKey 只在构造期临时传递给 adapter，不进入 Profile，也不写入证据。
	APIKey string
}

// resolveConstructionCredential 解析构造期凭据。
//
// 默认构建没有远端能力（NewChain 先执行 CheckEndpoints，任何非回环端点已被拒绝），
// 因此这里只保留既有的"必须提供构造期密钥"边界，不存在可绕过的远端发送路径。
func resolveConstructionCredential(_ Plan, opts ChainOptions) (string, error) {
	if strings.TrimSpace(opts.APIKey) == "" {
		return "", fmt.Errorf("%w: an api key is required to construct roles", ErrNotConfigured)
	}
	return opts.APIKey, nil
}

// remoteSendAuthorizationError 在默认构建中不存在远端发送路径，因此恒为 nil。
// 保留同一符号是为了让 Chain.Run/RunStep 的防御性复述在两个构建里都成立。
func remoteSendAuthorizationError(Plan) error { return nil }
