package probe_test

// 复核 P2（完整预检遗漏后续角色的非法模型身份）的回归测试。
//
// 反例（修复前）：后续 Chat 配置 chat@v1 时 Ready()==true、Preflight()==nil，
// 前一步 embedding 已真实发出，后续 Chat 才在构造 adapter 时返回
// providers.ErrModelVersionNotExpressible（并已产生一次真实付费尝试）。
//
// 修复后：预检与构造入口都复用生产 builder 的纯校验规则
// （NewProviderProfile + (*ProviderProfile).RequestModel），
// 因此不可表达的模型身份在任何发送之前失败，且错误链保留 sentinel。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// versionedRoleCases 覆盖"前一步合法、后续角色模型身份不可表达"的两种形态。
func versionedRoleCases() []struct {
	name   string
	mutate func(*probe.Plan)
	steps  []probe.Step
} {
	return []struct {
		name   string
		mutate func(*probe.Plan)
		steps  []probe.Step
	}{
		{
			name:   "later_chat_versioned",
			mutate: func(p *probe.Plan) { p.Chat.Model = "chat@v1" },
			steps: []probe.Step{
				{Kind: probe.StepEmbed, Texts: []string{"probe document"}},
				{Kind: probe.StepChat, Prompt: "probe ping"},
			},
		},
		{
			name:   "later_rerank_versioned",
			mutate: func(p *probe.Plan) { p.Rerank.Model = "m@v1" },
			steps: []probe.Step{
				{Kind: probe.StepChat, Prompt: "probe ping"},
				{Kind: probe.StepRerank, Query: "probe query", TopK: 2},
			},
		},
		{
			name:   "first_embed_versioned",
			mutate: func(p *probe.Plan) { p.Embed.Model = "text-embedding-v4@v1" },
			steps: []probe.Step{
				{Kind: probe.StepEmbed, Texts: []string{"probe document"}},
			},
		},
	}
}

// TestPreflight_RejectsVersionedRoles：Preflight 与 Readiness 对同一份计划给出
// 一致的拒绝结论（都复用 ValidateRoles），且错误链保留 sentinel。
func TestPreflight_RejectsVersionedRoles(t *testing.T) {
	const envName = "PROBE_FIX2_PREFLIGHT_CREDENTIAL"
	t.Setenv(envName, "placeholder-not-a-real-key")

	for _, tc := range versionedRoleCases() {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			tc.mutate(&plan)

			// 计划本身字段齐全：失败必须来自"角色身份不可表达"，不是缺字段。
			if err := plan.Validate(); err != nil {
				t.Fatalf("test plan must pass field validation: %v", err)
			}
			err := probe.Preflight(plan, tc.steps)
			if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
				t.Fatalf("Preflight() = %v, want ErrModelVersionNotExpressible", err)
			}

			approved := plan
			approved.Authorization = probe.Authorization{
				OnlineApproved: true,
				Scope:          probe.ScopeS1,
				ApprovedBy:     "offline-reviewer",
				ApprovedAt:     "2026-10-28",
			}
			cfg := probe.OnlineConfig{
				Plan:        approved,
				Credentials: probe.Credentials{EnvNames: []string{envName}},
				Steps:       tc.steps,
			}
			if cfg.Ready() {
				t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
			}
			var planDetail string
			for _, item := range probe.Readiness(cfg) {
				if item.Name == "plan" {
					planDetail = item.Detail
				}
			}
			if !strings.Contains(planDetail, "model version") {
				t.Fatalf("readiness plan detail = %q, want the model-version reason", planDetail)
			}
		})
	}
}

// TestNewChain_RejectsVersionedRoleBeforeAnySend：构造入口（Run/RunStep 的唯一
// 来源）也在发送前拒绝，且本机模型对端零命中。
func TestNewChain_RejectsVersionedRoleBeforeAnySend(t *testing.T) {
	for _, tc := range versionedRoleCases() {
		t.Run(tc.name, func(t *testing.T) {
			model := probe.NewLocalModelServer()
			defer model.Close()
			model.SetBehavior(probe.ModelBehavior{EmbedDims: 8})

			plan := validPlan()
			plan.Chat.Endpoint = model.URL()
			plan.Embed.Endpoint = model.URL()
			plan.Rerank.URL = model.RerankURL()
			tc.mutate(&plan)

			target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
			if err != nil {
				t.Fatalf("new local fake es target: %v", err)
			}
			defer target.Close()

			chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: "probe-offline-placeholder-key"})
			if chain != nil {
				chain.Close()
			}
			if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
				t.Fatalf("NewChain() = %v, want ErrModelVersionNotExpressible", err)
			}
			if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
				t.Fatalf("model hits = %d, want 0 (rejected during construction)", got)
			}
		})
	}
}

// TestChainRun_VersionedRoleStopReasonIsSanitized：预检失败写进证据的停止原因
// 是固定类别，不是服务端正文或任意错误文本。
func TestChainRun_VersionedRoleStopReasonIsSanitized(t *testing.T) {
	h := newProbeHarness(t, nil)
	h.Plan.Chat.Model = "chat@v1"

	// 直接调用 Run 时链已固定为构造期配置，因此这里验证的是"类别名"本身：
	// 预检失败会以同一脱敏函数写入 StopReason。
	err := probe.Preflight(h.Plan, probe.DefaultSteps())
	if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
		t.Fatalf("Preflight() = %v, want ErrModelVersionNotExpressible", err)
	}
	if got := probe.ClassifyError(err); got != "category=model_version_not_expressible" {
		t.Fatalf("stop reason = %q, want the sanitized model-version category", got)
	}
	_ = time.Second // 保持与其他链测试相同的超时语义占位，避免误用无超时断言
}
