package probe

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// "显式不限"维度名（BudgetPlan.Unlimited 的合法取值）。
const (
	unlimitedRequests     = "requests"
	unlimitedInputTokens  = "input_tokens"
	unlimitedOutputTokens = "output_tokens"
	unlimitedConcurrency  = "concurrency"
)

// BudgetPlan 是探针预算的**待决定数值**容器（草案 §3.2/§3.2.1 的 U-3）。
//
// 语义约束（与 providers 组件一致，但在此显式收紧）：
//   - providers 里 <=0 表示该维度不限；因此"待定"绝不能静默落成 0。
//     本类型要求每个维度要么是正整数，要么在 Unlimited 中被**显式点名**。
//   - StepDeadline 必须为正：单步 deadline 是代码可执行的停止边界，不能缺省。
//   - 金额上限/账号侧配额不在此结构内（ProcessBudget 无金额维度，草案 §3.2）。
type BudgetPlan struct {
	MaxRequests     int64
	MaxInputTokens  int64
	MaxOutputTokens int64
	MaxConcurrency  int
	StepDeadline    time.Duration
	// Unlimited 显式声明"该维度不限"的维度名：requests/input_tokens/output_tokens/concurrency。
	// 未在此处点名的 0 值一律视为"待定"，直接报错。
	Unlimited []string
}

// Validate 校验预算计划：负值非法；0 必须显式声明为不限；deadline 必须为正。
func (p BudgetPlan) Validate() error {
	declared := make(map[string]bool, len(p.Unlimited))
	for _, name := range p.Unlimited {
		key := strings.ToLower(strings.TrimSpace(name))
		switch key {
		case unlimitedRequests, unlimitedInputTokens, unlimitedOutputTokens, unlimitedConcurrency:
			declared[key] = true
		default:
			return fmt.Errorf("%w: unknown unlimited dimension %q", ErrBudgetInvalid, name)
		}
	}
	dimensions := []struct {
		name  string
		value int64
	}{
		{unlimitedRequests, p.MaxRequests},
		{unlimitedInputTokens, p.MaxInputTokens},
		{unlimitedOutputTokens, p.MaxOutputTokens},
		{unlimitedConcurrency, int64(p.MaxConcurrency)},
	}
	for _, dim := range dimensions {
		switch {
		case dim.value < 0:
			return fmt.Errorf("%w: %s must not be negative", ErrBudgetInvalid, dim.name)
		case dim.value == 0 && !declared[dim.name]:
			// 关键契约：0 在组件语义里等于"不限"，待定参数不得静默变成不限。
			return fmt.Errorf("%w: %s is 0 (unlimited in providers); set a positive limit or declare it in unlimited", ErrBudgetUnset, dim.name)
		}
	}
	if p.StepDeadline <= 0 {
		return fmt.Errorf("%w: step_deadline must be positive", ErrBudgetUnset)
	}
	return nil
}

// NewProcessBudget 按计划构造进程级预算 owner（数值原样透传，不 sanitize）。
func (p BudgetPlan) NewProcessBudget() (*providers.ProcessBudget, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.MaxConcurrency > math.MaxInt {
		return nil, fmt.Errorf("%w: concurrency out of range", ErrBudgetInvalid)
	}
	budget, err := providers.NewProcessBudget(p.MaxRequests, p.MaxInputTokens, p.MaxOutputTokens, p.MaxConcurrency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBudgetInvalid, err)
	}
	return budget, nil
}

// StepOptions 返回单步请求级预算的上限与固定 deadline。
// 单步上限取进程级同值作为上界（真正约束仍是进程 owner），deadline 恒定设置。
// 声明为"不限"的维度不追加选项（组件语义即不限），但该声明已在 Validate 中显式留痕。
func (p BudgetPlan) StepOptions() []providers.BudgetOption {
	opts := []providers.BudgetOption{providers.WithDeadline(p.StepDeadline)}
	if p.MaxRequests > 0 {
		opts = append(opts, providers.WithMaxRequests(p.MaxRequests))
	}
	if p.MaxInputTokens > 0 {
		opts = append(opts, providers.WithMaxInputTokens(p.MaxInputTokens))
	}
	if p.MaxOutputTokens > 0 {
		opts = append(opts, providers.WithMaxOutputTokens(p.MaxOutputTokens))
	}
	if p.MaxConcurrency > 0 {
		opts = append(opts, providers.WithMaxConcurrency(p.MaxConcurrency))
	}
	return opts
}

// snapshotView 把组件快照转成证据视图。
func snapshotView(snap providers.BudgetSnapshot) BudgetSnapshotView {
	return BudgetSnapshotView{
		AttemptsUsed:      snap.AttemptsUsed,
		CommittedAttempts: snap.CommittedAttempts,
		InFlight:          snap.InFlight,
		InputReserved:     snap.InputReserved,
		OutputReserved:    snap.OutputReserved,
		InputUsed:         snap.InputUsed,
		OutputUsed:        snap.OutputUsed,
	}
}
