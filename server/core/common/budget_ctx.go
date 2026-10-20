package common

import (
	"context"
	"fmt"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// B5: 请求级预算注入辅助函数。
// 在请求/任务入口（Retrieve/Index/ChatStream）调用，创建 RequestBudget 并注入 ctx，
// 使下游所有适配器共享同一累计 owner。

// WithRequestBudget 从当前 Registry 创建 RequestBudget 并注入 ctx。
// Registry 未设置时返回原始 ctx（适配器会自行创建独立预算）。
// opts 可选地设定请求级子作用域上限。
// Fix 3: 检查 ctx 是否已有 RequestBudget，有则直接返回原 ctx，不覆盖。
func WithRequestBudget(ctx context.Context, opts ...providers.BudgetOption) context.Context {
	// Fix 3: 检查 ctx 是否已有 RequestBudget，有则直接返回原 ctx，不覆盖
	if existing := providers.RequestBudgetFromContext(ctx); existing != nil {
		return ctx
	}
	r := GetRegistry()
	if r == nil {
		return ctx
	}
	rb, err := r.NewRequestBudget(ctx, opts...)
	if err != nil {
		// 创建失败：返回原始 ctx，适配器会自行 fallback
		return ctx
	}
	return providers.ContextWithRequestBudget(ctx, rb)
}

// WithTaskBudget 从当前 Registry 创建 TaskBudget 并注入 ctx（供 Index 等同步+后台场景）。
// 返回注入后的 ctx 和 TaskBudget（供后台 TransferToBackground 使用）。
// Registry 未设置时返回原始 ctx 和 nil TaskBudget。
// Fix 3: 创建失败返回 error 而非 fmt.Printf + nil。
func WithTaskBudget(ctx context.Context, opts ...providers.BudgetOption) (context.Context, *providers.TaskBudget, error) {
	r := GetRegistry()
	if r == nil {
		return ctx, nil, nil
	}
	// TaskBudget 需要从 ProcessBudget 创建；通过 Registry 接口获取
	// ProviderRegistry 接口目前只暴露 NewRequestBudget，TaskBudget 通过类型断言获取
	type taskBudgetCreator interface {
		Budget() *providers.ProcessBudget
	}
	tbc, ok := r.(taskBudgetCreator)
	if !ok || tbc.Budget() == nil {
		return ctx, nil, nil
	}
	tb, err := tbc.Budget().NewTaskBudget(ctx, opts...)
	if err != nil {
		// Fix 3: 创建失败返回错误而非静默 fallback
		return ctx, nil, fmt.Errorf("create task budget: %w", err)
	}
	// Fix 3: 创建共享 TaskBudget scope 的 RequestBudget，供适配器消费
	// 不再创建独立的 RequestBudget，而是共享同一 scope
	rb, err := tb.NewSharedRequestBudget(ctx, opts...)
	if err != nil {
		return ctx, nil, fmt.Errorf("create shared request budget: %w", err)
	}
	return providers.ContextWithRequestBudget(ctx, rb), tb, nil
}
