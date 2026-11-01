package probe

// 百炼真实注册路径的身份接口回归（复核第 4 轮 P1）。
//
// 复核结论：`chain.go` 已在 init 注册百炼 builder，Registry 实际返回
// `*bailian.validatedEmbedRole`；该类型当时只提供 Dimensions/ModelID，
// 不满足 `common.NewEmbedding` 强制的 `providers.ProfiledEmbedRole`，
// 因此 `NewChain → common.NewEmbedding → Registry.GetEmbedder` 恒失败
// （`ErrEmbeddingIdentityMismatch`，模型零请求）。接口强制来自更早的 `2c952ce`，
// 属于**前置遗留**，不是本批固定起点引入的新回归；但直接阻塞本批 S-1 百炼方案。
//
// 本文件用"真实注册 + Registry + fake ES 真实绑定 + 本机 fake HTTP"覆盖：
//   - 正常构造与三步调用（百炼与 OpenAI 共享路径对照）；
//   - 角色报告的 Profile 与实际模型/维度一致，且为防御性副本；
//   - 不同 provider / 模型 / 维度在构造期 fail closed（不放宽身份门禁）；
//   - 预算 owner/lease、取消与并发。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// newProviderChain 用指定 provider 构造"本机 fake ES 真实绑定 + 本机 fake 模型"
// 的完整探针链（与 NewChain 同一装配，不绕过任何生产校验）。
func newProviderChain(t *testing.T, model *LocalModelServer, provider string) *Chain {
	t.Helper()
	plan := sendGateTestPlan(model)
	plan.Target.ProviderName = provider
	target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	t.Cleanup(target.Close)
	chain, err := NewChain(context.Background(), plan, target, ChainOptions{APIKey: "probe-offline-placeholder-key"})
	if err != nil {
		t.Fatalf("NewChain(provider=%s) = %v: the registered role must expose verifiable identity", provider, err)
	}
	t.Cleanup(chain.Close)
	return chain
}

// TestProviderRegistrationBuildsProductionChain 证明真实注册的角色能通过身份门禁
// 并完成三步调用；百炼与 OpenAI 走同一共享路径、得到同一计数形状。
func TestProviderRegistrationBuildsProductionChain(t *testing.T) {
	for _, provider := range []string{providers.ProviderBailian, providers.ProviderOpenAI} {
		t.Run(provider, func(t *testing.T) {
			model := NewLocalModelServer()
			defer model.Close()
			model.SetBehavior(ModelBehavior{ChatUsage: true, EmbedDims: 8})

			chain := newProviderChain(t, model, provider)

			// 1) 角色身份必须可核实，且与实际模型/维度一致（不是另一套声明）。
			role, err := chain.registry.GetEmbedder(context.Background())
			if err != nil {
				t.Fatalf("GetEmbedder() = %v", err)
			}
			profiled, ok := role.(providers.ProfiledEmbedRole)
			if !ok {
				t.Fatalf("embedding role %T does not implement ProfiledEmbedRole", role)
			}
			profile := profiled.Profile()
			if got := providers.NormalizeProviderName(profile.ProviderName); got != provider {
				t.Fatalf("profile provider = %q, want %q", got, provider)
			}
			if profile.ModelID() != "probe-embed" || profile.Dimensions != 8 {
				t.Fatalf("profile identity = %q/%d, want probe-embed/8", profile.ModelID(), profile.Dimensions)
			}
			if role.ModelID() != profile.ModelID() || role.Dimensions() != profile.Dimensions {
				t.Fatalf("role identity %q/%d differs from profile %q/%d",
					role.ModelID(), role.Dimensions(), profile.ModelID(), profile.Dimensions)
			}

			// 2) 三步真实调用：预算承诺 / 闸门准入 / 交给 transport 各 1 次，
			//    对端（本机 fake HTTP）各收到 1 次。
			report, err := chain.Run(context.Background(), DefaultSteps())
			if err != nil {
				t.Fatalf("Run() = %v", err)
			}
			if len(report.Steps) != 3 {
				t.Fatalf("report steps = %d, want 3", len(report.Steps))
			}
			for _, step := range report.Steps {
				if step.Attempts != 1 || step.GateAdmissions != 1 || step.TransportCalls != 1 {
					t.Fatalf("step %s counts = attempts/gate/transport %d/%d/%d, want 1/1/1",
						step.Step, step.Attempts, step.GateAdmissions, step.TransportCalls)
				}
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

			// 3) 预算 owner/lease 收敛：承诺 3 次、无在途 slot、无残留预留。
			snapshot := chain.ProcessSnapshot()
			if snapshot.CommittedAttempts != 3 || snapshot.InFlight != 0 ||
				snapshot.InputReserved != 0 || snapshot.OutputReserved != 0 {
				t.Fatalf("process budget = %+v, want committed=3 inFlight=0 reserved=0", snapshot)
			}
			t.Logf("provider=%s role=%T identity=%s/%d counts=1/1/1 committed=%d inFlight=%d",
				provider, role, profile.ModelID(), profile.Dimensions,
				snapshot.CommittedAttempts, snapshot.InFlight)
		})
	}
}

// TestBailianEmbeddingProfileIsDefensiveCopy 证明 Profile() 返回值不是内部引用：
// 篡改返回的字符串/维度/KeyOptions 不改变角色身份，也不改变后续快照。
func TestBailianEmbeddingProfileIsDefensiveCopy(t *testing.T) {
	model := NewLocalModelServer()
	defer model.Close()
	plan := sendGateTestPlan(model)
	plan.Target.ProviderName = providers.ProviderBailian

	chat, embed, rerank, err := roleProfiles(plan)
	if err != nil {
		t.Fatalf("roleProfiles() = %v", err)
	}
	// KeyOptions 是最容易被共享的引用面：给它一个条目用来验证深拷贝。
	embed.KeyOptions = map[string]string{providers.KeyOptionStructuredOutput: "probe"}

	budget, err := plan.Budget.NewProcessBudget()
	if err != nil {
		t.Fatalf("new process budget: %v", err)
	}
	registry, err := providers.NewRegistry(providers.RegistryConfig{
		Chat:      chat,
		Embed:     embed,
		Rerank:    rerank,
		Budget:    budget,
		Transport: providers.NewTransport(),
		Credentials: func(context.Context, *providers.ProviderProfile) (string, error) {
			return "offline-placeholder", nil
		},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	role, err := registry.GetEmbedder(context.Background())
	if err != nil {
		t.Fatalf("GetEmbedder() = %v", err)
	}
	profiled, ok := role.(providers.ProfiledEmbedRole)
	if !ok {
		t.Fatalf("embedding role %T does not implement ProfiledEmbedRole", role)
	}

	first := profiled.Profile()
	if first.ProviderName != providers.ProviderBailian || first.ModelID() != "probe-embed" || first.Dimensions != 8 {
		t.Fatalf("first snapshot = %s/%s/%d, want bailian/probe-embed/8",
			first.ProviderName, first.ModelID(), first.Dimensions)
	}
	if value, ok := first.KeyOption(providers.KeyOptionStructuredOutput); !ok || value != "probe" {
		t.Fatalf("first snapshot KeyOptions = %q/%v, want probe/true", value, ok)
	}

	// 篡改返回值的所有可写面（含 map 引用）。
	first.ProviderName = "tampered"
	first.Model = "tampered"
	first.Version = "tampered"
	first.Dimensions = 999
	if first.KeyOptions != nil {
		first.KeyOptions[providers.KeyOptionStructuredOutput] = "tampered"
	}

	second := profiled.Profile()
	if second.ProviderName != providers.ProviderBailian || second.ModelID() != "probe-embed" || second.Dimensions != 8 {
		t.Fatalf("second snapshot = %s/%s/%d after tampering, want bailian/probe-embed/8",
			second.ProviderName, second.ModelID(), second.Dimensions)
	}
	if value, _ := second.KeyOption(providers.KeyOptionStructuredOutput); value != "probe" {
		t.Fatalf("second snapshot KeyOptions = %q, want probe (internal map leaked)", value)
	}
	// 角色自身身份同样不受影响。
	if role.ModelID() != "probe-embed" || role.Dimensions() != 8 {
		t.Fatalf("role identity = %s/%d after tampering, want probe-embed/8", role.ModelID(), role.Dimensions())
	}

	// 两次快照之间也不共享同一张 KeyOptions。
	if second.KeyOptions != nil {
		second.KeyOptions[providers.KeyOptionStructuredOutput] = "second"
	}
	third := profiled.Profile()
	if value, _ := third.KeyOption(providers.KeyOptionStructuredOutput); value != "probe" {
		t.Fatalf("third snapshot KeyOptions = %q, want probe (snapshots share a map)", value)
	}
}

// TestBailianEmbeddingIdentityMismatchFailsClosed 证明修复没有放宽身份门禁：
// Registry 报告的身份与配置期望不一致时，构造期即拒绝并零模型发送。
func TestBailianEmbeddingIdentityMismatchFailsClosed(t *testing.T) {
	cases := []struct {
		name             string
		registryProvider string
		registryModel    string
		registryDims     int
		confProvider     string
		confModel        string
		confDims         int
		wantDetail       string
	}{
		{
			name: "provider_mismatch", registryProvider: providers.ProviderBailian, registryModel: "probe-embed", registryDims: 8,
			confProvider: providers.ProviderOpenAI, confModel: "probe-embed", confDims: 8,
			wantDetail: "provider name",
		},
		{
			name: "model_mismatch", registryProvider: providers.ProviderBailian, registryModel: "probe-embed", registryDims: 8,
			confProvider: providers.ProviderBailian, confModel: "probe-embed-alt", confDims: 8,
			wantDetail: "model: role=",
		},
		{
			name: "dimensions_mismatch", registryProvider: providers.ProviderBailian, registryModel: "probe-embed", registryDims: 8,
			confProvider: providers.ProviderBailian, confModel: "probe-embed", confDims: 16,
			wantDetail: "dims",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := NewLocalModelServer()
			defer model.Close()
			model.SetBehavior(ModelBehavior{EmbedDims: tc.confDims})

			// Registry 侧身份（真实百炼 builder 注册）。
			plan := sendGateTestPlan(model)
			plan.Target.ProviderName = tc.registryProvider
			plan.Embed.Model = tc.registryModel
			plan.Embed.Dimensions = tc.registryDims
			chat, embed, rerank, err := roleProfiles(plan)
			if err != nil {
				t.Fatalf("roleProfiles() = %v", err)
			}
			budget, err := plan.Budget.NewProcessBudget()
			if err != nil {
				t.Fatalf("new process budget: %v", err)
			}
			registry, err := providers.NewRegistry(providers.RegistryConfig{
				Chat:      chat,
				Embed:     embed,
				Rerank:    rerank,
				Budget:    budget,
				Transport: providers.NewTransport(),
				Credentials: func(context.Context, *providers.ProviderProfile) (string, error) {
					return "offline-placeholder", nil
				},
			})
			if err != nil {
				t.Fatalf("new registry: %v", err)
			}

			// 配置侧期望身份：与本机 fake ES 的真实绑定一致，因此先通过指纹比较，
			// 再在 Registry/配置的身份比较处失败。
			target, err := NewLocalFakeESTarget(
				TargetSpec{IndexName: "rag-2d-bailian-identity", ProviderName: tc.confProvider, Similarity: "cosine"},
				tc.confModel, tc.confDims,
			)
			if err != nil {
				t.Fatalf("new local fake es target: %v", err)
			}
			defer target.Close()
			conf := &config.Config{
				Provider: &config.ProviderConfig{
					Name:                tc.confProvider,
					BaseURL:             model.URL(),
					EmbeddingModel:      tc.confModel,
					EmbeddingDimensions: tc.confDims,
					ChatModel:           "probe-chat",
				},
				ManagedResourceLifecycle: true,
				Registry:                 registry,
			}
			target.ApplyTo(conf)

			embedder, err := common.NewEmbedding(context.Background(), conf)
			if embedder != nil {
				t.Fatalf("NewEmbedding() returned an embedder for a mismatched identity")
			}
			if !errors.Is(err, common.ErrEmbeddingIdentityMismatch) {
				t.Fatalf("NewEmbedding() = %v, want ErrEmbeddingIdentityMismatch", err)
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("error = %v, want detail %q", err, tc.wantDetail)
			}
			if hits := model.EmbedHits(); hits != 0 {
				t.Fatalf("embed hits = %d, want 0 (identity must be checked before any send)", hits)
			}
			t.Logf("%s rejected at construction: %v", tc.name, err)
		})
	}
}

// TestBailianEmbeddingCancelAndConcurrentBudget 覆盖取消与并发下的预算/lease 收敛。
func TestBailianEmbeddingCancelAndConcurrentBudget(t *testing.T) {
	t.Run("cancel_mid_flight_settles_conservatively", func(t *testing.T) {
		model := NewLocalModelServer()
		defer model.Close()
		gate := NewGate()
		model.SetBehavior(ModelBehavior{EmbedDims: 8, EmbedGate: gate})

		plan := sendGateTestPlan(model)
		plan.Target.ProviderName = providers.ProviderBailian
		target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()
		chain, err := NewChain(context.Background(), plan, target, ChainOptions{APIKey: "probe-offline-placeholder-key"})
		if err != nil {
			t.Fatalf("NewChain(bailian) = %v", err)
		}
		defer chain.Close()

		ctx, cancel := context.WithCancel(context.Background())
		type result struct {
			report *StepReport
			err    error
		}
		done := make(chan result, 1)
		go func() {
			report, err := chain.RunStep(ctx, Step{Kind: StepEmbed, Texts: []string{"cancel-probe"}})
			done <- result{report: report, err: err}
		}()

		select {
		case <-gate.Reached():
		case <-time.After(5 * time.Second):
			t.Fatal("embed request never reached the local fake model")
		}
		cancel()
		gate.Release()

		var got result
		select {
		case got = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled step did not settle")
		}
		if got.err == nil {
			t.Fatal("cancelled step must return an error")
		}
		if got.report == nil {
			t.Fatal("cancelled step must return a report")
		}
		// 承诺发生在发送闸门之前：取消不会退款，账本保守结算。
		snapshot := chain.ProcessSnapshot()
		if snapshot.CommittedAttempts != 1 {
			t.Fatalf("committed = %d, want 1 (conservative, no refund)", snapshot.CommittedAttempts)
		}
		if snapshot.InFlight != 0 || snapshot.InputReserved != 0 || snapshot.OutputReserved != 0 {
			t.Fatalf("process budget after cancel = %+v, want inFlight/reserved 0", snapshot)
		}
		if snapshot.InputUsed <= 0 {
			t.Fatalf("input used = %d, want conservative settlement > 0", snapshot.InputUsed)
		}
		if got.report.Attempts != 1 || got.report.InFlight != 0 {
			t.Fatalf("step report = %+v, want attempts=1 inFlight=0", got.report)
		}
		if hits := model.EmbedHits(); hits != 1 {
			t.Fatalf("embed hits = %d, want 1", hits)
		}
		t.Logf("cancel settled: err=%v committed=%d inputUsed=%d inFlight=%d",
			got.err, snapshot.CommittedAttempts, snapshot.InputUsed, snapshot.InFlight)
	})

	t.Run("concurrent_steps_share_single_owner", func(t *testing.T) {
		model := NewLocalModelServer()
		defer model.Close()
		model.SetBehavior(ModelBehavior{EmbedDims: 8})

		plan := sendGateTestPlan(model)
		plan.Target.ProviderName = providers.ProviderBailian
		plan.Budget.MaxConcurrency = 8
		target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
		if err != nil {
			t.Fatalf("new local fake es target: %v", err)
		}
		defer target.Close()
		chain, err := NewChain(context.Background(), plan, target, ChainOptions{APIKey: "probe-offline-placeholder-key"})
		if err != nil {
			t.Fatalf("NewChain(bailian) = %v", err)
		}
		defer chain.Close()

		const workers = 4
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, errs[idx] = chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"concurrent-probe"}})
			}(i)
		}
		wg.Wait()
		for idx, err := range errs {
			if err != nil {
				t.Fatalf("worker %d: %v", idx, err)
			}
		}
		if hits := model.EmbedHits(); hits != workers {
			t.Fatalf("embed hits = %d, want %d", hits, workers)
		}
		snapshot := chain.ProcessSnapshot()
		if snapshot.CommittedAttempts != workers || snapshot.InFlight != 0 {
			t.Fatalf("process budget = %+v, want committed=%d inFlight=0", snapshot, workers)
		}
		if snapshot.InputReserved != 0 || snapshot.OutputReserved != 0 {
			t.Fatalf("process budget reserved = in %d/out %d, want 0",
				snapshot.InputReserved, snapshot.OutputReserved)
		}
		t.Logf("concurrent steps: committed=%d inFlight=%d hits=%d", snapshot.CommittedAttempts, snapshot.InFlight, workers)
	})
}
