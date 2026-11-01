package probe

import (
	"fmt"
)

// OnlineConfig 是 S-1 方案 A 在线执行所需的全部输入。
//
// 本批**不执行**任何在线探针：该结构只用于表达"未来在线运行需要用户补齐什么"，
// 其中所有目标/参数/预算数值均由授权文本给出（U-1/U-2/U-3/U-6），本包不填默认值，
// 也不持有真实凭据（Credentials 只保存环境变量名）。
type OnlineConfig struct {
	Plan        Plan
	Credentials Credentials
	// Steps 是本次授权覆盖的最小调用矩阵（空表示未声明，不静默使用默认矩阵）。
	Steps []Step
}

// ReadinessItem 是在线执行前某一项的待配置状态。
// Detail 只包含字段名/环境变量名与错误文本，绝不包含凭据值或模型输出。
type ReadinessItem struct {
	Name   string
	OK     bool
	Detail string
}

// Readiness 返回在线执行前必须补齐的项（离线可调用，不发起任何网络请求）。
//
// 与执行入口的关系：
//   - "plan" 项复用 Plan.Validate + CheckEndpoints + ValidateRoles（角色身份可表达性），
//   - "steps" 项复用 ValidateSteps，
//   - "credentials" 项复用 Credentials.Validate（存在、非空、变量名非空白、恰好一个声明）。
//
// 三者与 RunOnline/NewChain 的预检及凭据判定同源，因此 Ready() 为 false 时
// 执行入口也必然拒绝，不会出现"待配置入口说没就绪、实际入口却先发送一步"或
// "就绪说可以、执行时才发现凭据为空"的矛盾。
func Readiness(cfg OnlineConfig) []ReadinessItem {
	items := make([]ReadinessItem, 0, 4)

	if err := cfg.Plan.Validate(); err != nil {
		items = append(items, ReadinessItem{Name: "plan", Detail: err.Error()})
	} else if err := CheckEndpoints(cfg.Plan); err != nil {
		items = append(items, ReadinessItem{Name: "plan", Detail: err.Error()})
	} else if err := ValidateRoles(cfg.Plan); err != nil {
		items = append(items, ReadinessItem{Name: "plan", Detail: err.Error()})
	} else {
		items = append(items, ReadinessItem{Name: "plan", OK: true, Detail: "fixed target/parameters/budget/retry, endpoint policy and role identities are set"})
	}

	auth := cfg.Plan.Authorization
	switch {
	case !auth.OnlineApproved:
		items = append(items, ReadinessItem{Name: "authorization", Detail: "online execution is not approved"})
	case auth.Scope != ScopeS1:
		items = append(items, ReadinessItem{
			Name:   "authorization",
			Detail: fmt.Sprintf("skeleton only supports %s, authorization scope is %q", ScopeS1, auth.Scope),
		})
	default:
		items = append(items, ReadinessItem{Name: "authorization", OK: true, Detail: "approved for " + ScopeS1})
	}

	if err := cfg.Credentials.Validate(); err != nil {
		items = append(items, ReadinessItem{Name: "credentials", Detail: err.Error()})
	} else {
		items = append(items, ReadinessItem{
			Name:   "credentials",
			OK:     true,
			Detail: "exactly one credential environment variable is declared, present and non-empty (its value is never recorded)",
		})
	}

	items = append(items, stepsReadiness(cfg.Plan, cfg.Steps))
	return items
}

// stepsReadiness 校验本次声明的最小调用矩阵（与执行入口共用 ValidateSteps）。
func stepsReadiness(plan Plan, steps []Step) ReadinessItem {
	if err := ValidateSteps(plan, steps); err != nil {
		return ReadinessItem{Name: "steps", Detail: err.Error()}
	}
	return ReadinessItem{Name: "steps", OK: true, Detail: fmt.Sprintf("%d step(s) declared", len(steps))}
}

// Ready 汇总 Readiness：全部项 OK 才表示"可以申请在线执行"。
// 注意：Ready 为 true 也不等于已获授权，执行入口仍会再次校验授权文本。
func (cfg OnlineConfig) Ready() bool {
	for _, item := range Readiness(cfg) {
		if !item.OK {
			return false
		}
	}
	return true
}
