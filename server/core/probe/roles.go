package probe

import (
	"fmt"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 本文件把"实际启用角色的模型身份是否可表达"纳入预检（复核 P2）。
//
// 为什么必须复用生产 builder：生产的三条构造入口（BuildChatRole /
// BuildEmbedRole / BuildRerankRole）在装配 adapter 之前都会调用
// (*ProviderProfile).RequestModel()，把"身份里带 @version 但协议无法在请求中
// 表达版本"的情况 fail closed（providers.ErrModelVersionNotExpressible）。
// 如果预检只检查字段非空与步骤形状，前一步会先被真实发送，后一步才在构造
// adapter 时失败——这正是复核复现的"先发 embedding 再拒绝 chat@v1"。

// roleProfiles 构造三角色的 ProviderProfile（纯本地、无网络）。
// NewChain 与 ValidateRoles 共用本函数，保证"预检认为可执行"与
// "真实构造会成功"来自同一份 Profile 构造与同一组校验规则。
func roleProfiles(plan Plan) (chat, embed, rerank *providers.ProviderProfile, err error) {
	chat, err = providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: plan.Target.ProviderName,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     plan.Chat.Endpoint,
		Model:        plan.Chat.Model,
		Capabilities: providers.Capabilities{Stream: true, Usage: true},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("probe: chat profile: %w", err)
	}
	embed, err = providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: plan.Target.ProviderName,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     plan.Embed.Endpoint,
		Model:        plan.Embed.Model,
		Dimensions:   plan.Embed.Dimensions,
		Capabilities: providers.Capabilities{
			Usage:                     true,
			SupportsDimensionsRequest: plan.Embed.SupportsDimensionsRequest,
		},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("probe: embed profile: %w", err)
	}
	rerank, err = providers.NewProviderProfile(providers.ProviderProfile{
		ProviderName: plan.Target.ProviderName,
		Protocol:     providers.ProtocolOpenAICompat,
		Endpoint:     plan.Rerank.URL,
		Model:        plan.Rerank.Model,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("probe: rerank profile: %w", err)
	}
	return chat, embed, rerank, nil
}

// ValidateRoles 在发送前复用生产 builder 的**纯校验规则**：
//   - NewProviderProfile：必填项缺失、模型身份语法（空模型/空版本/多个 '@'）、
//     embedding 维度范围等可在构造期确定的非法配置；
//   - (*ProviderProfile).RequestModel：请求模型必须能表达已声明身份
//     （带 "@version" 的身份当前两种协议都无法在 wire 上表达）。
//
// 覆盖范围：Plan 要求三角色都必须声明（不存在"未启用角色"这一状态），
// 因此这里校验全部声明角色；步骤矩阵只决定本次实际发送哪几个角色。
// 任何一项非法都在第一次发送之前失败，错误链保留
// providers.ErrModelVersionNotExpressible / ErrModelIDInvalid 等 sentinel。
//
// Readiness 的 "plan" 项与 Preflight（Run / RunOnline 入口）都调用本函数，
// 因此"待配置入口"与"实际执行入口"不会给出相反结论。
func ValidateRoles(plan Plan) error {
	chat, embed, rerank, err := roleProfiles(plan)
	if err != nil {
		return err
	}
	roles := []struct {
		name    string
		profile *providers.ProviderProfile
	}{
		{"chat", chat},
		{"embed", embed},
		{"rerank", rerank},
	}
	for _, role := range roles {
		if _, err := role.profile.RequestModel(); err != nil {
			return fmt.Errorf("probe: %s role: %w", role.name, err)
		}
	}
	return nil
}
