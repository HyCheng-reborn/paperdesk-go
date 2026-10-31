//go:build online

// 在线构建（`-tags online`）的入口回归测试（复核问题 2/4/5）。
//
// 边界：
//   - 所有对端都是本机 fake：本机 fake 模型、本机 fake ES、本机 fake 代理；
//     远端端点一律使用 `.invalid` 域名，且任何外发都会先落到本机 fake 代理上被计数，
//     因此"零命中"是可观测事实，不是假设。
//   - 代理环境变量在 TestMain 中设置（早于 m.Run），因为 ProxyFromEnvironment
//     每个进程只解析一次；测试中途 Setenv 不会影响已缓存的配置。
//   - 本文件不执行真实在线探针：不访问互联网、不使用真实凭据、不接触真实 ES/Qdrant/MySQL。

package probe_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/providers"
	"github.com/wangle201210/go-rag/server/core/rerank"
	"github.com/wangle201210/go-rag/server/core/vector"
)

// onlineProxyHits 统计所有非回环外发请求；正确实现应恒为 0。
var onlineProxyHits atomic.Int64

// placeholderSecret 是占位敏感标记：它只能出现在测试输入里，
// 绝不允许出现在错误文本、Readiness 详情或报告字段中。
const placeholderSecret = "probe-placeholder-secret-DO-NOT-LEAK"

// placeholderCredentialEnv 是凭据占位环境变量名（值同样是占位标记）。
const placeholderCredentialEnv = "PROBE_PLACEHOLDER_API_KEY"

// TestMain 在任何测试之前装好本机 fake 代理，使"零外发"断言真实有效。
func TestMain(m *testing.M) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		onlineProxyHits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		os.Setenv(name, proxy.URL)
	}
	os.Setenv("NO_PROXY", "")
	os.Setenv("no_proxy", "")

	os.Exit(m.Run())
}

// remotePlan 返回一份已授权、凭据齐备、端点指向远端 `.invalid` 域名的计划。
// scheme 决定是明文（必须被拒绝）还是 HTTPS（策略层允许）。
func remotePlan(t *testing.T, scheme string) probe.Plan {
	t.Helper()
	base := scheme + "://api.example.invalid"
	plan := validPlan()
	plan.Chat.Endpoint = base + "/v1"
	plan.Embed.Endpoint = base + "/v1"
	plan.Rerank.URL = base + "/rerank"
	plan.Authorization = probe.Authorization{
		OnlineApproved: true,
		Scope:          probe.ScopeS1,
		ApprovedBy:     "offline-reviewer",
		ApprovedAt:     "2026-10-28",
	}
	t.Setenv(placeholderCredentialEnv, placeholderSecret)
	return plan
}

// assertNoSecret 断言对外文本不含占位敏感标记或端点 userinfo 片段。
func assertNoSecret(t *testing.T, label, text string) {
	t.Helper()
	for _, banned := range []string{placeholderSecret, "placeholder-user"} {
		if strings.Contains(text, banned) {
			t.Fatalf("%s leaks %q: %s", label, banned, text)
		}
	}
}

// TestProxyControl_EnvironmentIsHonored 是控制组：证明本机 fake 代理真的会被
// ProxyFromEnvironment 使用，因此各用例的“零命中”断言不是空断言。
// 请求只到本机代理（.invalid 域名 + 本机代理），不产生互联网访问。
func TestProxyControl_EnvironmentIsHonored(t *testing.T) {
	before := onlineProxyHits.Load()
	req, err := http.NewRequest(http.MethodGet, "http://probe-control.example.invalid/", nil)
	if err != nil {
		t.Fatalf("build control request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("control request did not reach the local fake proxy: %v", err)
	}
	_ = resp.Body.Close()
	if got := onlineProxyHits.Load() - before; got != 1 {
		t.Fatalf("control proxy hits = %d, want 1 (the zero-hit assertions depend on this)", got)
	}
}

// TestOnlineBuild_HasRemoteCapability 记录构建能力与端点策略的差异：
// online 构建允许非回环端点，但**只允许 HTTPS**；回环 fake 仍可用明文。
func TestOnlineBuild_HasRemoteCapability(t *testing.T) {
	if !probe.RemoteEndpointsAllowed() {
		t.Fatal("online build must allow remote endpoints (capability comes from the build tag)")
	}

	httpsPlan := validPlan()
	httpsPlan.Chat.Endpoint = "https://api.example.invalid/v1"
	httpsPlan.Embed.Endpoint = "https://api.example.invalid/v1"
	httpsPlan.Rerank.URL = "https://api.example.invalid/rerank"
	if err := probe.CheckEndpoints(httpsPlan); err != nil {
		t.Fatalf("https remote plan must pass the endpoint policy: %v", err)
	}

	httpPlan := validPlan()
	httpPlan.Chat.Endpoint = "http://api.example.invalid/v1"
	if err := probe.CheckEndpoints(httpPlan); !errors.Is(err, probe.ErrPlaintextRemote) {
		t.Fatalf("plaintext remote = %v, want ErrPlaintextRemote", err)
	}

	// 受控的本机 fake 仍可用明文 http。
	if err := probe.CheckEndpoints(validPlan()); err != nil {
		t.Fatalf("loopback plan must pass the endpoint policy: %v", err)
	}
}

// TestOnlineBuild_PlaintextRemoteRejectedBeforeAnySend：复核问题 2。
// 在线入口收到远端明文端点时，必须在发送前拒绝，且不得回显凭据。
func TestOnlineBuild_PlaintextRemoteRejectedBeforeAnySend(t *testing.T) {
	before := onlineProxyHits.Load()
	plan := remotePlan(t, "http")

	report, err := probe.RunOnline(context.Background(), probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       probe.DefaultSteps(),
	})
	if !errors.Is(err, probe.ErrPlaintextRemote) {
		t.Fatalf("RunOnline() = %v, want ErrPlaintextRemote", err)
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	if report.Stopped || len(report.Steps) != 0 {
		t.Fatalf("report = %+v, want a zero-step report (rejected before sending)", report)
	}
	if got := onlineProxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0 (no request may leave the process)", got)
	}
}

// TestOnlineBuild_UserInfoEndpointRejectedWithoutEcho：复核问题 2。
// 端点内嵌 userinfo 一律拒绝，且错误里不得出现用户名/密码。
func TestOnlineBuild_UserInfoEndpointRejectedWithoutEcho(t *testing.T) {
	before := onlineProxyHits.Load()
	plan := remotePlan(t, "https")
	plan.Chat.Endpoint = "https://placeholder-user:" + placeholderSecret + "@api.example.invalid/v1"

	_, err := probe.RunOnline(context.Background(), probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       probe.DefaultSteps(),
	})
	if !errors.Is(err, probe.ErrEndpointUserInfo) {
		t.Fatalf("RunOnline() = %v, want ErrEndpointUserInfo", err)
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	if got := onlineProxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0", got)
	}
}

// TestOnlineBuild_PreflightRejectsLaterInvalidStepBeforeAnySend：复核问题 4。
// 前一步合法、后一步无效时，必须在第一步发送前整体拒绝（零命中）。
func TestOnlineBuild_PreflightRejectsLaterInvalidStepBeforeAnySend(t *testing.T) {
	before := onlineProxyHits.Load()
	plan := remotePlan(t, "https")
	steps := []probe.Step{
		{Kind: probe.StepChat, Prompt: "probe ping"},
		{Kind: probe.StepEmbed}, // 缺少 texts：运行期才校验就会先发出一条 chat
	}
	cfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       steps,
	}

	// Readiness 与执行入口必须同源：Readiness 说未就绪，入口也必须拒绝。
	if cfg.Ready() {
		t.Fatal("Readiness() = ready, want the invalid later step to keep it unready")
	}
	var stepsDetail string
	for _, item := range probe.Readiness(cfg) {
		if item.Name == "steps" {
			stepsDetail = item.Detail
		}
	}
	if stepsDetail == "" || !strings.Contains(stepsDetail, "steps[1]") {
		t.Fatalf("readiness steps detail = %q, want it to name steps[1]", stepsDetail)
	}

	report, err := probe.RunOnline(context.Background(), cfg)
	if !errors.Is(err, probe.ErrEmptyStep) {
		t.Fatalf("RunOnline() = %v, want ErrEmptyStep", err)
	}
	if !strings.Contains(err.Error(), "preflight") || !strings.Contains(err.Error(), "steps[1]") {
		t.Fatalf("RunOnline() = %v, want the preflight error to name steps[1]", err)
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	assertNoSecret(t, "readiness detail", stepsDetail)
	if len(report.Steps) != 0 {
		t.Fatalf("report steps = %d, want 0 (nothing may be sent)", len(report.Steps))
	}
	if got := onlineProxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0 (preflight must run before the first send)", got)
	}
}

// TestOnlineBuild_QuerySecretNotEchoedInEvidence：复核问题 2/5。
// 端点查询参数里的秘密不得进入错误文本或 Readiness 详情。
func TestOnlineBuild_QuerySecretNotEchoedInEvidence(t *testing.T) {
	plan := remotePlan(t, "https")
	plan.Chat.Endpoint = "https://api.example.invalid/v1?api_key=" + placeholderSecret

	// 端点策略只检查 scheme/host/userinfo，查询参数本身合法；
	// 真正要保证的是它绝不会被回显。
	if err := probe.CheckEndpoints(plan); err != nil {
		t.Fatalf("query-carrying https endpoint must pass the policy: %v", err)
	}

	cfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       []probe.Step{{Kind: probe.StepChat, Prompt: "probe ping"}, {Kind: "unknown"}},
	}
	_, err := probe.RunOnline(context.Background(), cfg)
	if err == nil {
		t.Fatal("RunOnline() = nil, want the unknown step kind to be rejected")
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	for _, item := range probe.Readiness(cfg) {
		assertNoSecret(t, "readiness detail "+item.Name, item.Detail)
	}
}

// TestOnlineBuild_CredentialValueNeverAppearsInEvidence：复核问题 5。
// 凭据只按环境变量名出现；其值不得进入任何对外文本。
func TestOnlineBuild_CredentialValueNeverAppearsInEvidence(t *testing.T) {
	plan := remotePlan(t, "https")

	// 凭据缺失路径：只报告变量名。
	t.Setenv(placeholderCredentialEnv, "")
	missingCfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       probe.DefaultSteps(),
	}
	if _, err := probe.RunOnline(context.Background(), missingCfg); !errors.Is(err, probe.ErrMissingCredentials) {
		t.Fatalf("RunOnline() = %v, want ErrMissingCredentials", err)
	} else {
		if !strings.Contains(err.Error(), placeholderCredentialEnv) {
			t.Fatalf("RunOnline() = %v, want it to name the missing environment variable", err)
		}
		assertNoSecret(t, "RunOnline error", err.Error())
	}

	// 凭据存在但后续步骤非法：错误与就绪详情都不能带上凭据值。
	t.Setenv(placeholderCredentialEnv, placeholderSecret)
	invalidCfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       []probe.Step{{Kind: probe.StepChat, Prompt: "probe ping"}, {Kind: probe.StepEmbed}},
	}
	_, err := probe.RunOnline(context.Background(), invalidCfg)
	if err == nil {
		t.Fatal("RunOnline() = nil, want the invalid later step to be rejected")
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	for _, item := range probe.Readiness(invalidCfg) {
		assertNoSecret(t, "readiness detail "+item.Name, item.Detail)
	}
}

// TestOnlineBuild_LoopbackFakeStillRunsThroughRunOnline 证明 online 入口对受控本机
// fake（明文 http 回环）仍然可用：HTTPS 要求只作用于非回环端点。
func TestOnlineBuild_LoopbackFakeStillRunsThroughRunOnline(t *testing.T) {
	before := onlineProxyHits.Load()

	model := probe.NewLocalModelServer()
	defer model.Close()

	plan := validPlan()
	plan.Chat.Endpoint = model.URL()
	plan.Embed.Endpoint = model.URL()
	plan.Rerank.URL = model.RerankURL()
	plan.Authorization = probe.Authorization{
		OnlineApproved: true,
		Scope:          probe.ScopeS1,
		ApprovedBy:     "offline-reviewer",
		ApprovedAt:     "2026-10-28",
	}
	t.Setenv(placeholderCredentialEnv, placeholderSecret)
	model.SetBehavior(probe.ModelBehavior{ChatUsage: true, EmbedDims: plan.Embed.Dimensions})

	report, err := probe.RunOnline(context.Background(), probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       probe.DefaultSteps(),
	})
	if err != nil {
		t.Fatalf("RunOnline() unexpected error: %v", err)
	}
	if report.Stopped || len(report.Steps) != 3 {
		t.Fatalf("report = %+v, want 3 completed steps", report)
	}
	if got := onlineProxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0 (loopback must bypass the proxy)", got)
	}
}

// approvedS1 返回一份完整的 S-1 授权声明（占位审批人/时点，不含真实信息）。
func approvedS1() probe.Authorization {
	return probe.Authorization{
		OnlineApproved: true,
		Scope:          probe.ScopeS1,
		ApprovedBy:     "offline-reviewer",
		ApprovedAt:     "2026-10-28",
	}
}

// findReadiness 取指定名称的就绪项。
func findReadiness(items []probe.ReadinessItem, name string) (probe.ReadinessItem, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return probe.ReadinessItem{}, false
}

// ensureEnvAbsent 在测试期间确保变量确实不存在（t.Setenv 无法表达“不存在”）。
func ensureEnvAbsent(t *testing.T, name string) {
	t.Helper()
	original, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, original)
		}
	})
}

// TestOnlineBuild_DirectNewChainRequiresAuthorization：复核 P1（授权绕过）。
//
// Chain 只能经 NewChain 获得，因此授权/凭据边界必须落在该唯一构造入口上：
// 直接 NewChain → RunStep 不得再能把凭据发往非回环端点。四个子用例分别证明：
// 未授权拒绝、字面量密钥拒绝、授权+环境变量凭据可构造但不发送、
// 未授权的回环 fake 仍可直接使用（不破坏本机离线测试）。
func TestOnlineBuild_DirectNewChainRequiresAuthorization(t *testing.T) {
	t.Run("unapproved_mixed_endpoints_rejected_before_any_send", func(t *testing.T) {
		before := onlineProxyHits.Load()
		model := probe.NewLocalModelServer()
		defer model.Close()
		model.SetBehavior(probe.ModelBehavior{EmbedDims: 8})

		plan := validPlan()
		plan.Chat.Endpoint = model.URL()                       // 回环：本身允许
		plan.Embed.Endpoint = "https://api.example.invalid/v1" // 非回环：需要 S-1 授权
		plan.Rerank.URL = "https://api.example.invalid/rerank"
		// Authorization 保持零值（未批准）。
		t.Setenv(placeholderCredentialEnv, placeholderSecret)

		target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()

		chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{
			APIKey:      "probe-literal-placeholder-key",
			Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		})
		if chain != nil {
			chain.Close()
		}
		if !errors.Is(err, probe.ErrNotAuthorized) {
			t.Fatalf("NewChain() = %v, want ErrNotAuthorized", err)
		}
		assertNoSecret(t, "NewChain error", err.Error())
		if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
			t.Fatalf("model hits = %d, want 0 (rejected during construction)", got)
		}
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0 (no remote send may be attempted)", got)
		}
	})

	t.Run("approved_literal_api_key_rejected", func(t *testing.T) {
		before := onlineProxyHits.Load()
		plan := remotePlan(t, "https")

		target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()

		// 已授权，但凭据以字面量传入：非回环端点只接受环境变量声明。
		chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: placeholderSecret})
		if chain != nil {
			chain.Close()
		}
		if !errors.Is(err, probe.ErrNotConfigured) {
			t.Fatalf("NewChain() = %v, want ErrNotConfigured (literal api key is not accepted remotely)", err)
		}
		assertNoSecret(t, "NewChain error", err.Error())
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0", got)
		}
	})

	t.Run("approved_env_credentials_construct_without_send", func(t *testing.T) {
		before := onlineProxyHits.Load()
		plan := remotePlan(t, "https")

		target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()

		chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{
			Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		})
		if err != nil {
			t.Fatalf("NewChain() unexpected error: %v", err)
		}
		defer chain.Close()
		// 构造只装配 adapter，不产生任何发送。
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0 (construction must not send)", got)
		}
	})

	t.Run("unapproved_loopback_fake_still_runs_directly", func(t *testing.T) {
		before := onlineProxyHits.Load()
		model := probe.NewLocalModelServer()
		defer model.Close()

		plan := validPlan()
		plan.Chat.Endpoint = model.URL()
		plan.Embed.Endpoint = model.URL()
		plan.Rerank.URL = model.RerankURL()
		model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
		// Authorization 保持零值：回环端点不要求授权，本机 fake 测试不被破坏。

		target, err := probe.NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()

		chain, err := probe.NewChain(context.Background(), plan, target, probe.ChainOptions{APIKey: "probe-offline-placeholder-key"})
		if err != nil {
			t.Fatalf("NewChain() unexpected error: %v", err)
		}
		defer chain.Close()

		if _, err := chain.RunStep(context.Background(), probe.Step{Kind: probe.StepChat, Prompt: "probe ping"}); err != nil {
			t.Fatalf("direct RunStep on loopback fake = %v, want nil", err)
		}
		if got := model.ChatHits(); got != 1 {
			t.Fatalf("chat hits = %d, want 1 (loopback fake stays usable without authorization)", got)
		}
		if got := model.EmbedHits() + model.RerankHits(); got != 0 {
			t.Fatalf("other model hits = %d, want 0", got)
		}
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0", got)
		}
	})
}

// TestOnlineBuild_PreflightRejectsVersionedLaterRoleBeforeAnySend：复核 P2（预检漏角色身份）。
// 前一步合法、后续 Chat 或 Rerank 使用不可表达的 m@v1 时，Readiness 与执行入口
// 必须一致拒绝，且所有模型端点零命中（chat 也不例外）。
func TestOnlineBuild_PreflightRejectsVersionedLaterRoleBeforeAnySend(t *testing.T) {
	for _, tc := range versionedRoleCases() {
		t.Run(tc.name, func(t *testing.T) {
			before := onlineProxyHits.Load()
			model := probe.NewLocalModelServer()
			defer model.Close()

			plan := validPlan()
			plan.Chat.Endpoint = model.URL()
			plan.Embed.Endpoint = model.URL()
			plan.Rerank.URL = model.RerankURL()
			plan.Authorization = approvedS1()
			tc.mutate(&plan)
			model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
			t.Setenv(placeholderCredentialEnv, placeholderSecret)

			cfg := probe.OnlineConfig{
				Plan:        plan,
				Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
				Steps:       tc.steps,
			}
			if cfg.Ready() {
				t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
			}
			item, ok := findReadiness(probe.Readiness(cfg), "plan")
			if !ok || item.OK || !strings.Contains(item.Detail, "model version") {
				t.Fatalf("readiness plan item = %+v, want the model-version reason", item)
			}

			report, err := probe.RunOnline(context.Background(), cfg)
			if !errors.Is(err, providers.ErrModelVersionNotExpressible) {
				t.Fatalf("RunOnline() = %v, want ErrModelVersionNotExpressible", err)
			}
			assertNoSecret(t, "RunOnline error", err.Error())
			if report.Stopped || len(report.Steps) != 0 {
				t.Fatalf("report = %+v, want a zero-step report (rejected before sending)", report)
			}
			if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
				t.Fatalf("model hits = %d, want 0 (no role may be sent before the role check)", got)
			}
			if got := onlineProxyHits.Load() - before; got != 0 {
				t.Fatalf("fake proxy hits = %d, want 0", got)
			}
		})
	}
}

// TestOnlineBuild_CredentialBoundarySharedByReadinessAndExecution：复核 P2（空凭据误报就绪）。
// Readiness 的 credentials 项与 RunOnline 的凭据判定必须是同一组规则：
// 未声明/空白变量名/多声明/不存在/空值/纯空白一律未就绪且零发送，错误只含变量名。
func TestOnlineBuild_CredentialBoundarySharedByReadinessAndExecution(t *testing.T) {
	const (
		emptyEnv  = "PROBE_FIX2_ONLINE_CREDENTIAL_EMPTY"
		blankEnv  = "PROBE_FIX2_ONLINE_CREDENTIAL_BLANK"
		absentEnv = "PROBE_FIX2_ONLINE_CREDENTIAL_ABSENT"
	)
	t.Setenv(emptyEnv, "")
	t.Setenv(blankEnv, "   ")
	ensureEnvAbsent(t, absentEnv)

	cases := []struct {
		name     string
		declared []string
		wantName string // 期望错误文本里出现的变量名（空表示不要求）
	}{
		{"not_declared", nil, ""},
		{"blank_name", []string{"   "}, ""},
		{"two_declarations", []string{placeholderCredentialEnv, absentEnv}, placeholderCredentialEnv},
		{"absent_variable", []string{absentEnv}, absentEnv},
		{"empty_value", []string{emptyEnv}, emptyEnv},
		{"whitespace_value", []string{blankEnv}, blankEnv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := onlineProxyHits.Load()
			plan := remotePlan(t, "https")
			cfg := probe.OnlineConfig{
				Plan:        plan,
				Credentials: probe.Credentials{EnvNames: tc.declared},
				Steps:       probe.DefaultSteps(),
			}

			if cfg.Ready() {
				t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
			}
			item, ok := findReadiness(probe.Readiness(cfg), "credentials")
			if !ok || item.OK || item.Detail == "" {
				t.Fatalf("readiness credentials item = %+v, want a non-empty failure detail", item)
			}
			assertNoSecret(t, "readiness credentials detail", item.Detail)

			report, err := probe.RunOnline(context.Background(), cfg)
			if !errors.Is(err, probe.ErrMissingCredentials) {
				t.Fatalf("RunOnline() = %v, want ErrMissingCredentials", err)
			}
			if tc.wantName != "" && !strings.Contains(err.Error(), tc.wantName) {
				t.Fatalf("RunOnline() = %v, want it to name %s", err, tc.wantName)
			}
			assertNoSecret(t, "RunOnline error", err.Error())
			if len(report.Steps) != 0 {
				t.Fatalf("report steps = %d, want 0 (invalid credentials must not send)", len(report.Steps))
			}
			if got := onlineProxyHits.Load() - before; got != 0 {
				t.Fatalf("fake proxy hits = %d, want 0", got)
			}
		})
	}

	t.Run("valid_credentials_ready_and_executable", func(t *testing.T) {
		before := onlineProxyHits.Load()
		model := probe.NewLocalModelServer()
		defer model.Close()

		plan := validPlan()
		plan.Chat.Endpoint = model.URL()
		plan.Embed.Endpoint = model.URL()
		plan.Rerank.URL = model.RerankURL()
		plan.Authorization = approvedS1()
		model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
		t.Setenv(placeholderCredentialEnv, placeholderSecret)

		cfg := probe.OnlineConfig{
			Plan:        plan,
			Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
			Steps:       probe.DefaultSteps(),
		}
		if !cfg.Ready() {
			t.Fatalf("Ready() = false for valid credentials: %+v", probe.Readiness(cfg))
		}
		report, err := probe.RunOnline(context.Background(), cfg)
		if err != nil {
			t.Fatalf("RunOnline() unexpected error: %v", err)
		}
		if report.Stopped || len(report.Steps) != 3 {
			t.Fatalf("report = %+v, want 3 completed steps", report)
		}
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0 (loopback must bypass the proxy)", got)
		}
	})
}

// TestOnlineBuild_NilRerankDocumentRejectedBeforeAnySend：复核 P2（nil Rerank 文档）。
// 合法 Chat 在前、Rerank 文档含 nil 在后时，Readiness 与 RunOnline 必须同源拒绝，
// 且所有模型端点零命中——预检复用生产 rerank.ValidateInput，而不是在 Rerank 发送时才校验。
func TestOnlineBuild_NilRerankDocumentRejectedBeforeAnySend(t *testing.T) {
	before := onlineProxyHits.Load()
	model := probe.NewLocalModelServer()
	defer model.Close()

	plan := validPlan()
	plan.Chat.Endpoint = model.URL()
	plan.Embed.Endpoint = model.URL()
	plan.Rerank.URL = model.RerankURL()
	plan.Authorization = approvedS1()
	model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
	t.Setenv(placeholderCredentialEnv, placeholderSecret)

	steps := []probe.Step{
		{Kind: probe.StepChat, Prompt: "probe ping"},
		{Kind: probe.StepRerank, Query: "probe query", TopK: 2, Docs: []*schema.Document{nil, {ID: "probe-doc-1", Content: "probe document alpha"}}},
	}

	// 预检：错误链同时保留步骤哨兵与生产输入规则哨兵。
	err := probe.Preflight(plan, steps)
	if !errors.Is(err, probe.ErrEmptyStep) || !errors.Is(err, rerank.ErrInvalidInput) {
		t.Fatalf("Preflight() = %v, want ErrEmptyStep and rerank.ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "nil document at index 0") {
		t.Fatalf("Preflight() = %v, want the offending document index", err)
	}

	cfg := probe.OnlineConfig{
		Plan:        plan,
		Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
		Steps:       steps,
	}
	if cfg.Ready() {
		t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
	}
	item, ok := findReadiness(probe.Readiness(cfg), "steps")
	if !ok || item.OK || !strings.Contains(item.Detail, "nil document at index 0") {
		t.Fatalf("readiness steps item = %+v, want the shared rerank rule reason", item)
	}

	report, err := probe.RunOnline(context.Background(), cfg)
	if !errors.Is(err, probe.ErrEmptyStep) || !errors.Is(err, rerank.ErrInvalidInput) {
		t.Fatalf("RunOnline() = %v, want ErrEmptyStep and rerank.ErrInvalidInput", err)
	}
	assertNoSecret(t, "RunOnline error", err.Error())
	if report.Stopped || len(report.Steps) != 0 {
		t.Fatalf("report = %+v, want a zero-step report (nothing may start before the full preflight)", report)
	}
	if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
		t.Fatalf("model hits = %d, want 0 (the legal chat step must not be sent)", got)
	}
	if got := onlineProxyHits.Load() - before; got != 0 {
		t.Fatalf("fake proxy hits = %d, want 0", got)
	}
}

// TestOnlineBuild_TargetRulesReadinessMatchesExecution：复核 P2（目标配置误报就绪）。
// 非法物理名与不匹配的 similarity 必须在 Readiness 与 RunOnline 中一致拒绝，
// 且零发送；正常 cosine 仍就绪并可执行。
//
// 边界：这里只验证“配置自洽性”的静态预检；真实资源在预检之后是否被替换，
// 不由预检保证（运行期由 target binding / guard 重新读取）。
func TestOnlineBuild_TargetRulesReadinessMatchesExecution(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*probe.Plan)
		wantIs     error
		wantDetail string
	}{
		{
			name:       "invalid_physical_name",
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
			name:       "mismatched_similarity",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "dot" },
			wantIs:     vector.ErrFingerprintMismatch,
			wantDetail: "production expectation",
		},
		// 复核第 3 轮 P2：非规范输入（首尾空白/大写）不再被下层静默 TrimSpace/ToLower，
		// online 入口与默认构建、Readiness、绑定入口一致拒绝。
		{
			name:       "spaced_embedding_model",
			mutate:     func(p *probe.Plan) { p.Embed.Model = "  probe-embed  " },
			wantIs:     probe.ErrNotConfigured,
			wantDetail: "embed.model",
		},
		{
			name:       "spaced_uppercase_similarity",
			mutate:     func(p *probe.Plan) { p.Target.Similarity = "  COSINE  " },
			wantIs:     probe.ErrNotConfigured,
			wantDetail: "target.similarity",
		},
		{
			name:       "uppercase_provider",
			mutate:     func(p *probe.Plan) { p.Target.ProviderName = "OpenAI" },
			wantIs:     probe.ErrNotConfigured,
			wantDetail: "target.provider_name",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := onlineProxyHits.Load()
			model := probe.NewLocalModelServer()
			defer model.Close()

			plan := validPlan()
			plan.Chat.Endpoint = model.URL()
			plan.Embed.Endpoint = model.URL()
			plan.Rerank.URL = model.RerankURL()
			plan.Authorization = approvedS1()
			model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
			tc.mutate(&plan)
			t.Setenv(placeholderCredentialEnv, placeholderSecret)

			cfg := probe.OnlineConfig{
				Plan:        plan,
				Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
				Steps:       probe.DefaultSteps(),
			}
			if cfg.Ready() {
				t.Fatalf("Ready() = true, want false: %+v", probe.Readiness(cfg))
			}
			item, ok := findReadiness(probe.Readiness(cfg), "plan")
			if !ok || item.OK || !strings.Contains(item.Detail, tc.wantDetail) {
				t.Fatalf("readiness plan item = %+v, want it to mention %q", item, tc.wantDetail)
			}

			report, err := probe.RunOnline(context.Background(), cfg)
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("RunOnline() = %v, want %v", err, tc.wantIs)
			}
			assertNoSecret(t, "RunOnline error", err.Error())
			if report.Stopped || len(report.Steps) != 0 {
				t.Fatalf("report = %+v, want a zero-step report (rejected by the preflight)", report)
			}
			if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
				t.Fatalf("model hits = %d, want 0", got)
			}
			if got := onlineProxyHits.Load() - before; got != 0 {
				t.Fatalf("fake proxy hits = %d, want 0", got)
			}
		})
	}

	t.Run("cosine_ready_and_executable", func(t *testing.T) {
		before := onlineProxyHits.Load()
		model := probe.NewLocalModelServer()
		defer model.Close()

		plan := validPlan()
		plan.Chat.Endpoint = model.URL()
		plan.Embed.Endpoint = model.URL()
		plan.Rerank.URL = model.RerankURL()
		plan.Authorization = approvedS1()
		model.SetBehavior(probe.ModelBehavior{EmbedDims: plan.Embed.Dimensions})
		t.Setenv(placeholderCredentialEnv, placeholderSecret)

		cfg := probe.OnlineConfig{
			Plan:        plan,
			Credentials: probe.Credentials{EnvNames: []string{placeholderCredentialEnv}},
			Steps:       probe.DefaultSteps(),
		}
		if !cfg.Ready() {
			t.Fatalf("Ready() = false for a cosine target: %+v", probe.Readiness(cfg))
		}
		report, err := probe.RunOnline(context.Background(), cfg)
		if err != nil {
			t.Fatalf("RunOnline() unexpected error: %v", err)
		}
		if report.Stopped || len(report.Steps) != 3 {
			t.Fatalf("report = %+v, want 3 completed steps", report)
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
		if got := onlineProxyHits.Load() - before; got != 0 {
			t.Fatalf("fake proxy hits = %d, want 0 (loopback must bypass the proxy)", got)
		}
	})
}
