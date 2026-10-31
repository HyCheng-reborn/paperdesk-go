package probe_test

// 复核 P2 的两项补充回归（默认构建）：
//
//  1. 完整预检遗漏 nil Rerank 文档：合法 Chat 在前、Rerank 文档含 nil 时，
//     修复前 Preflight/Ready 通过，Chat 已真实发送，Rerank 才在生产入口拒绝。
//     修复后预检复用生产 rerank.ValidateInput，所有模型端点零命中。
//
//  2. 目标配置被误报为就绪：物理名 ``bad,*`` 与 similarity=dot 在修复前
//     Ready()==true，执行时才分别在 BindPhysicalTarget / 完整指纹比较处失败。
//     修复后 Plan.Validate 复用 vector.ValidatePhysicalName 与
//     vector.NewExpectedEmbeddingFingerprint 的完整规则，Readiness 与执行入口一致拒绝。
//
// 所有对端都是本机 fake（fake ES + fake 模型）；不访问互联网或真实设施。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/rerank"
	"github.com/wangle201210/go-rag/server/core/vector"
)

const targetRuleCredentialEnv = "PROBE_FIX3_TARGET_RULE_KEY"

// readinessItem 取指定名称的就绪项（默认构建与 online 构建共用的最小 helper）。
func readinessItem(items []probe.ReadinessItem, name string) probe.ReadinessItem {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return probe.ReadinessItem{Name: name, Detail: "<missing readiness item>"}
}

// TestPreflight_RejectsNilRerankDocumentBeforeAnySend 覆盖复核 P2（nil Rerank 文档）：
// 预检、就绪与两个实际入口（Run / RunStep）都必须拒绝，且零模型命中。
func TestPreflight_RejectsNilRerankDocumentBeforeAnySend(t *testing.T) {
	docs := []*schema.Document{nil, {ID: "probe-doc-1", Content: "probe document alpha"}}
	steps := []probe.Step{
		{Kind: probe.StepChat, Prompt: "probe ping"},
		{Kind: probe.StepRerank, Query: "probe query", TopK: 2, Docs: docs},
	}

	t.Run("preflight_rejects_with_shared_production_rule", func(t *testing.T) {
		plan := validPlan()
		err := probe.Preflight(plan, steps)
		if !errors.Is(err, probe.ErrEmptyStep) {
			t.Fatalf("Preflight() = %v, want ErrEmptyStep", err)
		}
		// 错误链保留生产规则的 sentinel，便于调用方区分"步骤非法"与"输入非法"。
		if !errors.Is(err, rerank.ErrInvalidInput) {
			t.Fatalf("Preflight() = %v, want rerank.ErrInvalidInput", err)
		}
		if !strings.Contains(err.Error(), "nil document at index 0") {
			t.Fatalf("Preflight() = %v, want the offending document index", err)
		}
		// 控制组：合法文档仍然通过预检。
		if err := probe.ValidateSteps(plan, probe.DefaultSteps()); err != nil {
			t.Fatalf("valid steps must pass, got %v", err)
		}
	})

	t.Run("readiness_matches_execution", func(t *testing.T) {
		t.Setenv(targetRuleCredentialEnv, "offline-placeholder-value")
		plan := validPlan()
		cfg := probe.OnlineConfig{
			Plan:        plan,
			Credentials: probe.Credentials{EnvNames: []string{targetRuleCredentialEnv}},
			Steps:       steps,
		}
		item := readinessItem(probe.Readiness(cfg), "steps")
		if item.OK {
			t.Fatalf("steps readiness = %+v, want not OK", item)
		}
		if !strings.Contains(item.Detail, "nil document at index 0") {
			t.Fatalf("steps readiness detail = %q, want the shared rerank rule reason", item.Detail)
		}
		if cfg.Ready() {
			t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
		}
	})

	t.Run("run_refuses_before_any_send", func(t *testing.T) {
		h := newProbeHarness(t, nil)
		report, err := h.Chain.Run(context.Background(), steps)
		if !errors.Is(err, probe.ErrEmptyStep) {
			t.Fatalf("Run() = %v, want ErrEmptyStep", err)
		}
		if len(report.Steps) != 0 {
			t.Fatalf("report steps = %d, want 0 (nothing may start before the full preflight)", len(report.Steps))
		}
		if got := h.Model.ChatHits() + h.Model.EmbedHits() + h.Model.RerankHits(); got != 0 {
			t.Fatalf("model hits = %d, want 0 (the legal chat step must not be sent)", got)
		}
	})

	t.Run("run_step_refuses_before_any_send", func(t *testing.T) {
		h := newProbeHarness(t, nil)
		if _, err := h.Chain.RunStep(context.Background(), steps[1]); !errors.Is(err, probe.ErrEmptyStep) {
			t.Fatalf("RunStep() = %v, want ErrEmptyStep", err)
		}
		if got := h.Model.ChatHits() + h.Model.EmbedHits() + h.Model.RerankHits(); got != 0 {
			t.Fatalf("model hits = %d, want 0", got)
		}
	})
}

// TestPlan_TargetRulesMatchProduction 覆盖复核 P2（目标配置误报就绪）：
// 非法物理名与不匹配的 similarity 必须在 Plan.Validate / Readiness / NewChain
// 三处一致拒绝，且执行入口零发送；正常 cosine 仍然可用。
func TestPlan_TargetRulesMatchProduction(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*probe.Plan)
		wantIs     error
		wantDetail string
	}{
		{
			name:       "wildcard_physical_name",
			mutate:     func(p *probe.Plan) { p.Target.IndexName = "bad,*" },
			wantIs:     vector.ErrPhysicalTargetRequired,
			wantDetail: "target.index_name",
		},
		{
			name:       "multiple_physical_targets",
			mutate:     func(p *probe.Plan) { p.Target.IndexName = "index-a,index-b" },
			wantIs:     vector.ErrPhysicalTargetRequired,
			wantDetail: "target.index_name",
		},
		{
			name:       "similarity_dot",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "dot" },
			wantIs:     vector.ErrFingerprintMismatch,
			wantDetail: "production expectation",
		},
		{
			name:       "similarity_not_in_allowed_set",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "hamming" },
			wantIs:     probe.ErrNotConfigured,
			wantDetail: "similarity",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			tc.mutate(&plan)

			// 1) 计划校验：复用生产规则，不再只看字段非空。
			err := plan.Validate()
			if !errors.Is(err, tc.wantIs) || !errors.Is(err, probe.ErrNotConfigured) {
				t.Fatalf("Plan.Validate() = %v, want both %v and ErrNotConfigured", err, tc.wantIs)
			}

			// 2) Readiness：plan 项必须给出同一原因（不是"看起来齐全就 OK"）。
			t.Setenv(targetRuleCredentialEnv, "offline-placeholder-value")
			cfg := probe.OnlineConfig{
				Plan:        plan,
				Credentials: probe.Credentials{EnvNames: []string{targetRuleCredentialEnv}},
				Steps:       probe.DefaultSteps(),
			}
			item := readinessItem(probe.Readiness(cfg), "plan")
			if item.OK {
				t.Fatalf("plan readiness = %+v, want not OK", item)
			}
			if !strings.Contains(item.Detail, tc.wantDetail) {
				t.Fatalf("plan readiness detail = %q, want it to mention %q", item.Detail, tc.wantDetail)
			}

			// 3) 执行入口：NewChain 在发送前拒绝。
			//    目标用一份合法 spec 构造，确保拒绝来自"计划"而不是绑定先失败。
			model := probe.NewLocalModelServer()
			defer model.Close()
			model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
			plan.Chat.Endpoint = model.URL()
			plan.Embed.Endpoint = model.URL()
			plan.Rerank.URL = model.RerankURL()

			validSpec := validPlan().Target
			target, err := probe.NewLocalFakeESTarget(validSpec, plan.Embed.Model, plan.Embed.Dimensions)
			if err != nil {
				t.Fatalf("new local fake es target: %v", err)
			}
			defer target.Close()

			chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: "probe-offline-placeholder-key"})
			if chain != nil {
				chain.Close()
			}
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("NewChain() = %v, want %v", err, tc.wantIs)
			}
			if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
				t.Fatalf("model hits = %d, want 0 (rejected before any send)", got)
			}
		})
	}

	t.Run("cosine_target_ready_and_executable", func(t *testing.T) {
		t.Setenv(targetRuleCredentialEnv, "offline-placeholder-value")
		h := newProbeHarness(t, nil)

		if err := h.Plan.Validate(); err != nil {
			t.Fatalf("cosine plan must pass validation: %v", err)
		}
		cfg := probe.OnlineConfig{
			Plan:        h.Plan,
			Credentials: probe.Credentials{EnvNames: []string{targetRuleCredentialEnv}},
			Steps:       probe.DefaultSteps(),
		}
		// online 授权项不在本构建的就绪语义内，这里只校验 plan/steps/credentials 三项。
		for _, name := range []string{"plan", "steps", "credentials"} {
			if item := readinessItem(probe.Readiness(cfg), name); !item.OK {
				t.Fatalf("%s readiness = %+v, want OK", name, item)
			}
		}

		report, err := h.Chain.Run(context.Background(), probe.DefaultSteps())
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
		if len(report.Steps) != 3 {
			t.Fatalf("report steps = %d, want 3", len(report.Steps))
		}
		if got := h.Model.ChatHits(); got != 1 {
			t.Fatalf("chat hits = %d, want 1", got)
		}
		if got := h.Model.EmbedHits(); got != 1 {
			t.Fatalf("embed hits = %d, want 1", got)
		}
		if got := h.Model.RerankHits(); got != 1 {
			t.Fatalf("rerank hits = %d, want 1", got)
		}
	})

	t.Run("local_fake_target_rejects_wildcard_name", func(t *testing.T) {
		// 生产绑定规则本身：``bad,*`` 不是可绑定的具体物理目标。
		spec := validPlan().Target
		spec.IndexName = "bad,*"
		target, err := probe.NewLocalFakeESTarget(spec, "probe-embed", 8)
		if target != nil {
			target.Close()
		}
		if !errors.Is(err, vector.ErrPhysicalTargetRequired) {
			t.Fatalf("NewLocalFakeESTarget() = %v, want ErrPhysicalTargetRequired", err)
		}
	})
}

// TestPlan_CanonicalIdentityRejectedEverywhere 覆盖复核第 3 轮 P2（目标规范化与
// 就绪一致性）：模型名带首尾空白、similarity 为 "  COSINE  " / 大写 COSINE、
// provider 大写，都必须在 Plan.Validate / Readiness / NewChain /
// NewLocalFakeESTarget 中一致拒绝，且模型零发送。
//
// 选择是“所有入口一致拒绝非规范输入”，而不是各层各自 TrimSpace/ToLower：
// 生产的多层规范化与 fingerprint 摘要/Marshal/Parse 并不完全对称，
// 静默改写会让 Ready、实际绑定与 digest 给出不同结论。
func TestPlan_CanonicalIdentityRejectedEverywhere(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*probe.Plan)
		wantDetail string
		wantReason string
	}{
		{
			name:       "embedding_model_with_spaces",
			mutate:     func(p *probe.Plan) { p.Embed.Model = "  probe-embed  " },
			wantDetail: "embed.model",
			wantReason: "leading/trailing whitespace",
		},
		{
			name:       "chat_model_with_trailing_space",
			mutate:     func(p *probe.Plan) { p.Chat.Model = "probe-chat " },
			wantDetail: "chat.model",
			wantReason: "leading/trailing whitespace",
		},
		{
			name:       "similarity_spaces_and_uppercase",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "  COSINE  " },
			wantDetail: "target.similarity",
			wantReason: "leading/trailing whitespace",
		},
		{
			name:       "similarity_uppercase",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "COSINE" },
			wantDetail: "target.similarity",
			wantReason: "must be lowercase",
		},
		{
			name:       "provider_uppercase",
			mutate:     func(p *probe.Plan) { p.Target.ProviderName = "OpenAI" },
			wantDetail: "target.provider_name",
			wantReason: "must be lowercase",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			tc.mutate(&plan)

			// 1) 计划校验：非规范输入在本地即可判定，直接拒绝，不做静默改写。
			err := plan.Validate()
			if !errors.Is(err, probe.ErrNotConfigured) {
				t.Fatalf("Plan.Validate() = %v, want ErrNotConfigured", err)
			}
			if !strings.Contains(err.Error(), tc.wantDetail) || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("Plan.Validate() = %v, want it to mention %q and %q", err, tc.wantDetail, tc.wantReason)
			}

			// 2) Readiness：同一原因（不能“看起来齐全就 OK”）。
			t.Setenv(targetRuleCredentialEnv, "offline-placeholder-value")
			cfg := probe.OnlineConfig{
				Plan:        plan,
				Credentials: probe.Credentials{EnvNames: []string{targetRuleCredentialEnv}},
				Steps:       probe.DefaultSteps(),
			}
			if cfg.Ready() {
				t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
			}
			item := readinessItem(probe.Readiness(cfg), "plan")
			if item.OK || !strings.Contains(item.Detail, tc.wantDetail) {
				t.Fatalf("plan readiness = %+v, want it to mention %q", item, tc.wantDetail)
			}

			// 3) 执行入口：NewChain 在发送前拒绝（目标用一份合法 spec 构造，
			//    确保拒绝来自“计划”而不是绑定先失败）。
			model := probe.NewLocalModelServer()
			defer model.Close()
			model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
			plan.Chat.Endpoint = model.URL()
			plan.Embed.Endpoint = model.URL()
			plan.Rerank.URL = model.RerankURL()

			validSpec := validPlan().Target
			target, err := probe.NewLocalFakeESTarget(validSpec, validPlan().Embed.Model, validPlan().Embed.Dimensions)
			if err != nil {
				t.Fatalf("new local fake es target: %v", err)
			}
			defer target.Close()

			chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: "probe-offline-placeholder-key"})
			if chain != nil {
				chain.Close()
			}
			if !errors.Is(err, probe.ErrNotConfigured) {
				t.Fatalf("NewChain() = %v, want ErrNotConfigured", err)
			}
			if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
				t.Fatalf("model hits = %d, want 0 (rejected before any send)", got)
			}
		})
	}

	// 标准 cosine：就绪、可执行，且 digest 在生产期望、实际绑定与
	// Marshal→Parse 之间一致（不改变已验收的存量 fingerprint 格式）。
	t.Run("canonical_cosine_digest_matches_binding_and_round_trip", func(t *testing.T) {
		plan := validPlan()
		target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()

		expected, err := vector.NewExpectedEmbeddingFingerprint(plan.Embed.Model, plan.Embed.Dimensions, plan.Target.ProviderName)
		if err != nil {
			t.Fatalf("production expected fingerprint: %v", err)
		}
		if !target.Fingerprint.Equal(expected) || target.Fingerprint.Digest() != expected.Digest() {
			t.Fatalf("fake target fingerprint = %s, want the production expectation %s",
				target.Fingerprint.Digest(), expected.Digest())
		}
		bound := target.Binding.Fingerprint()
		if !bound.Equal(expected) || bound.Digest() != expected.Digest() {
			t.Fatalf("bound fingerprint = %s, want %s", bound.Digest(), expected.Digest())
		}
		raw, err := json.Marshal(target.Fingerprint)
		if err != nil {
			t.Fatalf("marshal fingerprint: %v", err)
		}
		parsed, err := vector.ParseFingerprint(raw)
		if err != nil {
			t.Fatalf("parse fingerprint: %v", err)
		}
		if !parsed.Equal(target.Fingerprint) || parsed.Digest() != target.Fingerprint.Digest() {
			t.Fatalf("round trip digest = %s, want %s", parsed.Digest(), target.Fingerprint.Digest())
		}

		model := probe.NewLocalModelServer()
		defer model.Close()
		model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
		plan.Chat.Endpoint = model.URL()
		plan.Embed.Endpoint = model.URL()
		plan.Rerank.URL = model.RerankURL()

		t.Setenv(targetRuleCredentialEnv, "offline-placeholder-value")
		cfg := probe.OnlineConfig{
			Plan:        plan,
			Credentials: probe.Credentials{EnvNames: []string{targetRuleCredentialEnv}},
			Steps:       probe.DefaultSteps(),
		}
		if item := readinessItem(probe.Readiness(cfg), "plan"); !item.OK {
			t.Fatalf("plan readiness = %+v, want OK for canonical cosine", item)
		}

		chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: "probe-offline-placeholder-key"})
		if err != nil {
			t.Fatalf("NewChain() = %v, want nil", err)
		}
		defer chain.Close()
		report, err := chain.Run(context.Background(), probe.DefaultSteps())
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
		if len(report.Steps) != 3 {
			t.Fatalf("report steps = %d, want 3", len(report.Steps))
		}
		if got := model.ChatHits(); got != 1 {
			t.Fatalf("chat hits = %d, want 1", got)
		}
		if got := model.EmbedHits(); got != 1 {
			t.Fatalf("embed hits = %d, want 1", got)
		}
		if got := model.RerankHits(); got != 1 {
			t.Fatalf("rerank hits = %d, want 1", got)
		}
	})

	t.Run("local_fake_target_rejects_non_canonical_identity", func(t *testing.T) {
		// 绑定入口不自行规范化：否则 Ready/绑定/摘要会各自给出不同结论。
		spec := validPlan().Target
		target, err := probe.NewLocalFakeESTarget(spec, "  probe-embed  ", 8)
		if target != nil {
			target.Close()
		}
		if !errors.Is(err, probe.ErrNotConfigured) || !strings.Contains(err.Error(), "embed.model") {
			t.Fatalf("spaced model = %v, want ErrNotConfigured mentioning embed.model", err)
		}

		spec = validPlan().Target
		spec.Similarity = "  COSINE  "
		target, err = probe.NewLocalFakeESTarget(spec, "probe-embed", 8)
		if target != nil {
			target.Close()
		}
		if !errors.Is(err, probe.ErrNotConfigured) || !strings.Contains(err.Error(), "target.similarity") {
			t.Fatalf("spaced similarity = %v, want ErrNotConfigured mentioning target.similarity", err)
		}

		spec = validPlan().Target
		spec.ProviderName = "OpenAI"
		target, err = probe.NewLocalFakeESTarget(spec, "probe-embed", 8)
		if target != nil {
			target.Close()
		}
		if !errors.Is(err, probe.ErrNotConfigured) || !strings.Contains(err.Error(), "target.provider_name") {
			t.Fatalf("uppercase provider = %v, want ErrNotConfigured mentioning target.provider_name", err)
		}
	})
}
