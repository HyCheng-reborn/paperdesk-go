package probe_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/probe"
)

// validPlan 返回一份"全部已配置"的离线计划（端点只是占位回环地址，不会被拨号）。
func validPlan() probe.Plan {
	return probe.Plan{
		Target: probe.TargetSpec{IndexName: "rag-2d-probe-valid", ProviderName: "openai", Similarity: "cosine"},
		Chat:   probe.ChatSpec{Endpoint: "http://127.0.0.1:18081", Model: "probe-chat", MaxOutputTokens: 16},
		Embed:  probe.EmbedSpec{Endpoint: "http://127.0.0.1:18081", Model: "probe-embed", Dimensions: 8},
		Rerank: probe.RerankSpec{URL: "http://127.0.0.1:18081/rerank", Model: "probe-rerank", TopK: 2},
		Budget: probe.BudgetPlan{
			MaxRequests:     4,
			MaxInputTokens:  1024,
			MaxOutputTokens: 64,
			MaxConcurrency:  1,
			StepDeadline:    time.Second,
		},
		Retry: probe.RetryPolicy{MaxAttempts: 1},
	}
}

// TestPlan_Validate 校验固定输入缺失/非法时显式失败，不静默补默认值。
func TestPlan_Validate(t *testing.T) {
	if err := validPlan().Validate(); err != nil {
		t.Fatalf("valid plan must pass: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*probe.Plan)
		wantErr error
	}{
		{"missing_index_name", func(p *probe.Plan) { p.Target.IndexName = "" }, probe.ErrNotConfigured},
		{"missing_provider", func(p *probe.Plan) { p.Target.ProviderName = "  " }, probe.ErrNotConfigured},
		{"missing_similarity", func(p *probe.Plan) { p.Target.Similarity = "" }, probe.ErrNotConfigured},
		{"missing_chat_model", func(p *probe.Plan) { p.Chat.Model = "" }, probe.ErrNotConfigured},
		{"zero_chat_max_output", func(p *probe.Plan) { p.Chat.MaxOutputTokens = 0 }, probe.ErrNotConfigured},
		{"missing_embed_endpoint", func(p *probe.Plan) { p.Embed.Endpoint = "" }, probe.ErrNotConfigured},
		{"zero_dims", func(p *probe.Plan) { p.Embed.Dimensions = 0 }, probe.ErrNotConfigured},
		{"missing_rerank_url", func(p *probe.Plan) { p.Rerank.URL = "" }, probe.ErrNotConfigured},
		{"zero_topk", func(p *probe.Plan) { p.Rerank.TopK = 0 }, probe.ErrNotConfigured},
		{"zero_retry_attempts", func(p *probe.Plan) { p.Retry.MaxAttempts = 0 }, probe.ErrRetryUnset},
		{"negative_retry_attempts", func(p *probe.Plan) { p.Retry.MaxAttempts = -1 }, probe.ErrRetryUnset},
		{"approved_without_scope", func(p *probe.Plan) {
			p.Authorization = probe.Authorization{OnlineApproved: true, ApprovedBy: "reviewer", ApprovedAt: "2026-10-28"}
		}, probe.ErrNotConfigured},
		{"approved_unknown_scope", func(p *probe.Plan) {
			p.Authorization = probe.Authorization{OnlineApproved: true, Scope: "S-9", ApprovedBy: "reviewer", ApprovedAt: "2026-10-28"}
		}, probe.ErrNotConfigured},
		{"approved_without_approver", func(p *probe.Plan) {
			p.Authorization = probe.Authorization{OnlineApproved: true, Scope: probe.ScopeS1, ApprovedAt: "2026-10-28"}
		}, probe.ErrNotConfigured},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			tc.mutate(&plan)
			err := plan.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestBudgetPlan_ZeroIsNotFinite 证明 0 不会被当成有限上限，也不会静默变成不限。
func TestBudgetPlan_ZeroIsNotFinite(t *testing.T) {
	base := validPlan().Budget

	t.Run("zero_requests_rejected", func(t *testing.T) {
		plan := base
		plan.MaxRequests = 0
		err := plan.Validate()
		if !errors.Is(err, probe.ErrBudgetUnset) {
			t.Fatalf("Validate() = %v, want ErrBudgetUnset", err)
		}
		if !strings.Contains(err.Error(), "requests") || !strings.Contains(err.Error(), "unlimited") {
			t.Fatalf("error must name the dimension and the unlimited semantics: %v", err)
		}
	})

	t.Run("zero_input_tokens_rejected", func(t *testing.T) {
		plan := base
		plan.MaxInputTokens = 0
		if err := plan.Validate(); !errors.Is(err, probe.ErrBudgetUnset) {
			t.Fatalf("Validate() = %v, want ErrBudgetUnset", err)
		}
	})

	t.Run("zero_concurrency_rejected", func(t *testing.T) {
		plan := base
		plan.MaxConcurrency = 0
		if err := plan.Validate(); !errors.Is(err, probe.ErrBudgetUnset) {
			t.Fatalf("Validate() = %v, want ErrBudgetUnset", err)
		}
	})

	t.Run("explicit_unlimited_accepted", func(t *testing.T) {
		plan := base
		plan.MaxRequests = 0
		plan.Unlimited = []string{"requests"}
		if err := plan.Validate(); err != nil {
			t.Fatalf("explicit unlimited must pass: %v", err)
		}
	})

	t.Run("unknown_unlimited_rejected", func(t *testing.T) {
		plan := base
		plan.Unlimited = []string{"tokens"}
		if err := plan.Validate(); !errors.Is(err, probe.ErrBudgetInvalid) {
			t.Fatalf("Validate() = %v, want ErrBudgetInvalid", err)
		}
	})

	t.Run("negative_rejected", func(t *testing.T) {
		plan := base
		plan.MaxRequests = -1
		if err := plan.Validate(); !errors.Is(err, probe.ErrBudgetInvalid) {
			t.Fatalf("Validate() = %v, want ErrBudgetInvalid", err)
		}
	})

	t.Run("deadline_required", func(t *testing.T) {
		plan := base
		plan.StepDeadline = 0
		if err := plan.Validate(); !errors.Is(err, probe.ErrBudgetUnset) {
			t.Fatalf("Validate() = %v, want ErrBudgetUnset", err)
		}
	})

	t.Run("process_budget_reuses_validation", func(t *testing.T) {
		plan := base
		plan.MaxRequests = 0
		if _, err := plan.NewProcessBudget(); !errors.Is(err, probe.ErrBudgetUnset) {
			t.Fatalf("NewProcessBudget() = %v, want ErrBudgetUnset", err)
		}
		if _, err := base.NewProcessBudget(); err != nil {
			t.Fatalf("NewProcessBudget() unexpected error: %v", err)
		}
	})
}

// TestCredentials_ReportsPresenceOnly 证明凭据入口只报告存在性。
func TestCredentials_ReportsPresenceOnly(t *testing.T) {
	const present = "PROBE_TEST_CREDENTIAL_PRESENT"
	const absent = "PROBE_TEST_CREDENTIAL_ABSENT"
	t.Setenv(present, "placeholder-not-a-real-key")
	// 确保 absent 在当前进程中确实未被设置：只清理该名字，测试结束后按原状恢复。
	original, hadValue := os.LookupEnv(absent)
	if err := os.Unsetenv(absent); err != nil {
		t.Fatalf("unsetenv %s: %v", absent, err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(absent, original)
		}
	})

	creds := probe.Credentials{EnvNames: []string{present, absent}}
	presence := creds.Presence()
	if !presence[present] {
		t.Fatalf("present namespace must be reported: %v", presence)
	}
	if presence[absent] {
		t.Fatalf("empty value must not be reported as present: %v", presence)
	}
	missing := creds.Missing()
	if len(missing) != 1 || missing[0] != absent {
		t.Fatalf("Missing() = %v, want [%s]", missing, absent)
	}
}

// TestReadiness_ListsUnconfiguredItems 证明"待配置入口"逐项列出缺失内容。
func TestReadiness_ListsUnconfiguredItems(t *testing.T) {
	items := probe.Readiness(probe.OnlineConfig{})
	if len(items) != 4 {
		t.Fatalf("Readiness() returned %d items, want 4: %+v", len(items), items)
	}
	for _, item := range items {
		if item.OK {
			t.Fatalf("empty config must not be ready: %+v", item)
		}
	}
	if (probe.OnlineConfig{}).Ready() {
		t.Fatal("empty config must not be Ready()")
	}
	// 未经授权的合法计划：plan 项 OK，但 authorization/credentials/steps 不 OK。
	cfg := probe.OnlineConfig{Plan: validPlan()}
	byName := map[string]probe.ReadinessItem{}
	for _, item := range probe.Readiness(cfg) {
		byName[item.Name] = item
	}
	if !byName["plan"].OK {
		t.Fatalf("plan item must be OK: %+v", byName["plan"])
	}
	if byName["authorization"].OK || byName["credentials"].OK || byName["steps"].OK {
		t.Fatalf("unapproved plan must not be ready: %+v", byName)
	}
}

// TestReadiness_ReadyWhenAllDeclared 证明授权+凭据存在性+步骤齐备时 Ready 为 true。
func TestReadiness_ReadyWhenAllDeclared(t *testing.T) {
	const envName = "PROBE_TEST_CREDENTIAL_READY"
	t.Setenv(envName, "placeholder-not-a-real-key")

	plan := validPlan()
	plan.Authorization = probe.Authorization{
		OnlineApproved: true,
		Scope:          probe.ScopeS1,
		ApprovedBy:     "reviewer",
		ApprovedAt:     "2026-10-28",
	}
	cfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{envName}},
		Steps:       probe.DefaultSteps(),
	}
	if !cfg.Ready() {
		t.Fatalf("config must be ready: %+v", probe.Readiness(cfg))
	}

	// 范围不是 S-1 时，授权项不 OK（骨架只覆盖 S-1 方案 A）。
	plan.Authorization.Scope = probe.ScopeS2
	cfg.Plan = plan
	if cfg.Ready() {
		t.Fatalf("S-2 scope must not be reported ready: %+v", probe.Readiness(cfg))
	}

	// 步骤声明非法时，steps 项不 OK。
	plan.Authorization.Scope = probe.ScopeS1
	cfg.Plan = plan
	cfg.Steps = []probe.Step{{Kind: probe.StepEmbed}}
	if cfg.Ready() {
		t.Fatalf("invalid steps must not be ready: %+v", probe.Readiness(cfg))
	}
}
