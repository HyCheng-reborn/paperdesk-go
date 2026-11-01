//go:build online

// 在线执行骨架（`//go:build online`）：默认构建不编译本文件，
// 因此默认构建/默认测试不可能意外发出真实在线请求。
//
// 本批（Stage 2D 离线准备）**未执行**本文件任何路径。要真正运行，必须同时满足：
//  1. 用户对 design/stage2d-plan.md 的 S-1 给出明确在线授权（范围+预算数值）；
//  2. Plan 中目标/参数/预算/重试全部按授权文本填写，Readiness 全项 OK；
//  3. 凭据仅经环境变量注入（本文件不落盘、不打印、不写入证据）。
//
// 注意：本骨架的 ES 侧仍是本机 fake ES（方案 A），只证明绑定/guard/身份链被真实执行，
// 不产生任何真实 ES 事实；真实 ES 验证属 S-2，需另行授权。
//
// 远端模型端点在 online 构建中允许，但必须是 HTTPS（明文被 ErrPlaintextRemote 拒绝）；
// 本文件在本批（离线准备）**未被运行**，只经 `go vet -tags online` 静态检查。
package probe

import (
	"context"
	"fmt"
)

// RunOnline 执行 S-1 方案 A 在线探针（本机 fake ES 绑定 + 真实模型端点）。
// 任一前置条件不满足即 fail closed，不发起任何请求。
//
// 授权与凭据边界与 NewChain 同源（remoteSendAuthorizationError + Credentials.Validate）：
// 即使调用方绕过本函数直接使用 NewChain/Run/RunStep，online 构建的远端发送
// 仍受同一 S-1 授权与环境变量凭据约束。
func RunOnline(ctx context.Context, cfg OnlineConfig) (Report, error) {
	// 一次性完整预检（计划 + 端点策略 + 角色身份 + **全部**步骤），发生在任何构造与发送之前：
	// 只要后一步非法（含不可表达的模型身份），本次运行零发送
	// （与 Readiness 共用同一实现，不会给出相反结论）。
	if err := Preflight(cfg.Plan, cfg.Steps); err != nil {
		return Report{}, fmt.Errorf("probe: online preflight: %w", err)
	}
	auth := cfg.Plan.Authorization
	if !auth.OnlineApproved {
		return Report{}, ErrNotAuthorized
	}
	if auth.Scope != ScopeS1 {
		return Report{}, fmt.Errorf("%w: authorization scope %q", ErrScopeNotSupported, auth.Scope)
	}
	// 凭据边界与 Readiness 共用 Credentials.Validate：
	// 未声明/空白变量名/多声明/不存在/空值/纯空白一律 fail closed，错误只含变量名。
	if err := cfg.Credentials.Validate(); err != nil {
		return Report{}, err
	}

	target, err := NewLocalFakeESTarget(cfg.Plan.Target, cfg.Plan.Embed.Model, cfg.Plan.Embed.Dimensions)
	if err != nil {
		return Report{}, err
	}
	defer target.Close()

	// 在线执行仍受端点策略约束：远端必须是 HTTPS，不允许 userinfo；
	// 本构建已允许非回环端点（remoteEndpointsAllowed=true，见 endpoint_policy_online.go）。
	// 凭据只以环境变量声明的形式传递，值在 NewChain 内读取一次。
	chain, err := NewChain(ctx, cfg.Plan, target, ChainOptions{Credentials: cfg.Credentials})
	if err != nil {
		return Report{}, err
	}
	defer chain.Close()

	return chain.Run(ctx, cfg.Steps)
}
