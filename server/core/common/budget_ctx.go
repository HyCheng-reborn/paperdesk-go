package common

import (
	"context"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// B5: 请求级预算注入辅助函数。
// 在请求/任务入口（Retrieve/Index/ChatStream）调用，创建 RequestBudget 并注入 ctx，
// 使下游所有适配器共享同一累计 owner。

// WithRequestBudget 从当前 Registry 创建 RequestBudget 并注入 ctx。
// Registry 未设置时返回原始 ctx（适配器会自行创建独立预算）。
// opts 可选地设定请求级子作用域上限。
func WithRequestBudget(ctx context.Context, opts ...providers.BudgetOption) context.Context {
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
func WithTaskBudget(ctx context.Context, opts ...providers.BudgetOption) (context.Context, *providers.TaskBudget) {
	r := GetRegistry()
	if r == nil {
		return ctx, nil
	}
	// TaskBudget 需要从 ProcessBudget 创建；通过 Registry 接口获取
	// ProviderRegistry 接口目前只暴露 NewRequestBudget，TaskBudget 通过类型断言获取
	type taskBudgetCreator interface {
		Budget() *providers.ProcessBudget
	}
	tbc, ok := r.(taskBudgetCreator)
	if !ok || tbc.Budget() == nil {
		return ctx, nil
	}
	tb, err := tbc.Budget().NewTaskBudget(ctx, opts...)
	if err != nil {
		return ctx, nil
	}
	// TaskBudget 通过 RequestBudget 接口注入 ctx（适配器读取 RequestBudget）
	// 为兼容，同时注入一个 RequestBudget
	rb, err := tbc.Budget().NewRequestBudget(ctx, opts...)
	if err != nil {
		return ctx, nil
	}
	return providers.ContextWithRequestBudget(ctx, rb), tb
}
