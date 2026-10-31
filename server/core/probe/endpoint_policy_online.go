//go:build online

// online 构建的端点能力声明与**构造期授权/凭据边界**。
//
// 关键约束（复核 P1：online 构建可绕过 RunOnline 授权）：
// 远端发送能力不能只由 RunOnline 一个入口保护。Chain 的唯一构造入口 NewChain
// 在 online 构建中必须施加与 RunOnline 相同的 S-1 授权与凭据边界，
// 且凭据只能来自环境变量声明——否则调用方直接 NewChain → RunStep 就能把
// 字面量密钥发往任意 HTTPS 端点。回环端点（受控本机 fake）不受此限制，
// 以免破坏未授权的本机离线测试。
package probe

import (
	"fmt"
	"strings"
)

// remoteEndpointsAllowed 在 online 构建中为 true：允许非回环端点，且必须 HTTPS。
const remoteEndpointsAllowed = true

// ChainOptions 是 online 构建的构造期凭据，**不包含任何远端开关**：
// 远端发送的准入由"S-1 授权声明 + 环境变量凭据"决定，而不是由布尔开关打开。
type ChainOptions struct {
	// APIKey 仅用于受控本机 fake（回环端点）的构造期凭据。
	APIKey string
	// Credentials 声明非回环发送所需的凭据环境变量名；
	// 值在这里读取一次并只交给构造期 adapter，不写日志、不写证据。
	Credentials Credentials
}

// resolveConstructionCredential 是 online 构建的构造期边界，与 RunOnline 完全同源：
//   - 回环端点（受控本机 fake）：可用字面量密钥，也可用环境变量声明；
//     未授权不影响本机离线测试（复核要求：不要破坏未授权的本机 fake 测试）；
//   - 非回环端点（真实远端发送能力）：必须同时满足 S-1 授权声明完整、已批准、
//     作用域为 S-1，且凭据只来自环境变量声明（字面量密钥一律拒绝）。
//
// 直接调用 NewChain（以及其后的 Run/RunStep）都走这里，无法绕过 RunOnline。
func resolveConstructionCredential(plan Plan, opts ChainOptions) (string, error) {
	if !plan.HasRemoteEndpoints() {
		if strings.TrimSpace(opts.APIKey) != "" {
			return opts.APIKey, nil
		}
		return opts.Credentials.Resolve()
	}
	if err := remoteSendAuthorizationError(plan); err != nil {
		return "", err
	}
	if strings.TrimSpace(opts.APIKey) != "" {
		return "", fmt.Errorf("%w: non-loopback endpoints require credentials declared via environment variables (a literal api key is not accepted)", ErrNotConfigured)
	}
	return opts.Credentials.Resolve()
}

// remoteSendAuthorizationError 是 S-1 授权边界的唯一实现（与 RunOnline 共用）：
// 未批准 → ErrNotAuthorized；作用域不是 S-1 → ErrScopeNotSupported；
// 已批准但声明不完整 → ErrNotConfigured（由 Authorization.Validate 给出）。
func remoteSendAuthorizationError(plan Plan) error {
	if !plan.HasRemoteEndpoints() {
		return nil
	}
	if err := plan.Authorization.Validate(); err != nil {
		return err
	}
	if !plan.Authorization.OnlineApproved {
		return ErrNotAuthorized
	}
	if plan.Authorization.Scope != ScopeS1 {
		return fmt.Errorf("%w: authorization scope %q", ErrScopeNotSupported, plan.Authorization.Scope)
	}
	return nil
}
