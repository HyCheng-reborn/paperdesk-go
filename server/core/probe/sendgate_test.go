package probe

// 复核 P1（停止标记后的发送窗口）的回归测试：发送准入的线性化点。
//
// 反例（修复前，见独立复核 independent-2d-bdd3a6-concurrency/review_stop_20k_test.go）：
// beginAttempt 在锁内登记句柄后，stop 只发布 stopped 并**在锁外**逐个取消；
// 一个已准入但阻塞在 fake ES guard 的步骤，在 stopped=true 之后仍会发出一次
// embedding 请求（人工登记 20,000 个句柄放大取消窗口）。
//
// 修复后的契约（见 sendgate.go）：
//   - 入口准入（beginAttempt）只表示"允许开始"，不代表已发出；
//   - 真正发送前必须通过 sendGate 的 commitSend：它在 c.mu 读侧观察停止标记，
//     而 stop 在写侧发布它，因此两者有确定的全序；
//   - 停止标记之后，任何尚未通过闸门的尝试一律返回 ErrAlreadyStopped，零字节发出；
//   - 已经通过闸门（已经在途）的请求不被声称可撤销，只取消其 ctx，且不再重试。
//
// 说明：原复现用 20,000 个手工句柄把"停止标记 → 取消所有句柄"的耗时放大，
// 用来构造竞争窗口；它不代表一般负载下的触发概率。本文件既复现该形状（并要求
// 零发送），也用可控同步点做确定性验证。
//
// 第 3 轮复核补充：上面这些用例只能证明"未过闸门 → 零发送"。已通过闸门、尚未
// 交给底层 transport 的请求仍可能在标记之后开始传输，该残余窗口**未闭合**：
// 见 TestSendGate_PostGatePreTransportWindowIsNotClosed（记录事实，不声称闭合）。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripFunc 把函数适配成 http.RoundTripper（仅包内测试用）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// sendGateTestPlan 构造一份全部端点指向本机 fake 模型的合法计划。
func sendGateTestPlan(model *LocalModelServer) Plan {
	return Plan{
		Target: TargetSpec{IndexName: "rag-2d-sendgate", ProviderName: "openai", Similarity: "cosine"},
		Chat:   ChatSpec{Endpoint: model.URL(), Model: "probe-chat", MaxOutputTokens: 16},
		Embed:  EmbedSpec{Endpoint: model.URL(), Model: "probe-embed", Dimensions: 8},
		Rerank: RerankSpec{URL: model.RerankURL(), Model: "probe-rerank", TopK: 2},
		Budget: BudgetPlan{
			MaxRequests:     8,
			MaxInputTokens:  4096,
			MaxOutputTokens: 256,
			MaxConcurrency:  8,
			StepDeadline:    30 * time.Second,
		},
		Retry: RetryPolicy{MaxAttempts: 1},
	}
}

// newSendGateChain 构造"本机 fake ES 真实绑定 + 本机 fake 模型"的探针链；
// mutate 在构造前调整计划（如重试策略）。
func newSendGateChain(t *testing.T, model *LocalModelServer, mutate func(*Plan)) *Chain {
	t.Helper()
	plan := sendGateTestPlan(model)
	if mutate != nil {
		mutate(&plan)
	}
	target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	t.Cleanup(target.Close)
	chain, err := NewChain(context.Background(), plan, target, ChainOptions{APIKey: "probe-offline-placeholder-key"})
	if err != nil {
		t.Fatalf("new probe chain: %v", err)
	}
	t.Cleanup(chain.Close)
	return chain
}

// waitStoppedMarker 轮询到停止标记可观察为止（带超时，避免死等）。
func waitStoppedMarker(t *testing.T, chain *Chain) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !chain.isStopped() {
		if time.Now().After(deadline) {
			t.Fatal("stop marker never became observable")
		}
		time.Sleep(time.Microsecond)
	}
}

// TestSendGate_StopMarkerRefusesAdmittedButUnsentAttempt 是确定性验证：
// 用 sendGate.beforeCommit 把一次已准入的 embedding 尝试**固定**在
// "已准入、尚未在途"的位置，然后发布停止标记，再放行。
// 结果必须是：零模型命中、ErrAlreadyStopped、预算 lease 收敛、后续入口仍拒绝。
func TestSendGate_StopMarkerRefusesAdmittedButUnsentAttempt(t *testing.T) {
	model := NewLocalModelServer()
	t.Cleanup(model.Close)
	model.SetBehavior(ModelBehavior{EmbedDims: 8})
	chain := newSendGateChain(t, model, nil)

	atGate := make(chan struct{}, 1)
	release := make(chan struct{})
	chain.gate.beforeCommit = func() {
		select {
		case atGate <- struct{}{}:
		default:
		}
		<-release
	}

	stepErr := make(chan error, 1)
	go func() {
		_, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
		stepErr <- err
	}()

	select {
	case <-atGate:
	case <-time.After(5 * time.Second):
		t.Fatal("attempt never reached the send gate")
	}
	// 已准入、尚未通过闸门：此时还没有任何模型请求。
	if got := model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits before the gate = %d, want 0", got)
	}

	// stop 同步返回即表示停止标记已发布（写侧线性化点）。
	chain.stop(errors.New("offline stop"))
	if !chain.isStopped() {
		t.Fatal("stop marker must be observable once stop returns")
	}

	close(release)
	select {
	case err := <-stepErr:
		if !errors.Is(err, ErrAlreadyStopped) {
			t.Fatalf("admitted-but-unsent step = %v, want ErrAlreadyStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("step did not settle after the gate refused the send")
	}

	if got := model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits after the stop marker = %d, want 0 (refused at the send gate)", got)
	}
	snap := chain.ProcessSnapshot()
	if snap.InFlight != 0 {
		t.Fatalf("process in-flight = %d, want 0 (admitted work must converge)", snap.InFlight)
	}
	if snap.InputReserved != 0 || snap.OutputReserved != 0 {
		t.Fatalf("process budget = %+v, want released reservations", snap)
	}
	// 已准入尝试在预算账本上是单调承诺（Commit 在发送前，Abort 不回退计数），
	// 这里记录该事实，避免把"零发送"误读成"零预算占用"。
	if snap.CommittedAttempts != 1 {
		t.Fatalf("committed attempts = %d, want 1 (admission is monotonic in the ledger)", snap.CommittedAttempts)
	}

	// 停止之后的新入口仍然在发送前被拒绝。
	if _, err := chain.RunStep(context.Background(), Step{Kind: StepChat, Prompt: "probe ping"}); !errors.Is(err, ErrAlreadyStopped) {
		t.Fatalf("RunStep() after stop = %v, want ErrAlreadyStopped", err)
	}
	if got := model.ChatHits() + model.EmbedHits() + model.RerankHits(); got != 0 {
		t.Fatalf("model hits after post-stop RunStep = %d, want 0", got)
	}
}

// TestSendGate_InFlightRequestIsNotRevokedAndNotRetried 验证另一半语义：
// 已通过闸门的请求是"已经在途"，停止不声称能撤销它（服务端命中数保持 1），
// 但停止之后不得有新的发送或新的重试（即使 RetryOnTransportError 开启）。
func TestSendGate_InFlightRequestIsNotRevokedAndNotRetried(t *testing.T) {
	modelGate := NewGate()
	model := NewLocalModelServer()
	t.Cleanup(model.Close)
	model.SetBehavior(ModelBehavior{ChatUsage: true, ChatGate: modelGate})
	// 夹具清理顺序（LIFO）：先放行 handler，再关闭对端。
	t.Cleanup(modelGate.Release)

	chain := newSendGateChain(t, model, func(plan *Plan) {
		// 允许重试，用来证明"停止之后的取消不会被当成可重试的传输错误"。
		plan.Retry = RetryPolicy{MaxAttempts: 2, RetryOnTransportError: true}
	})

	stepErr := make(chan error, 1)
	go func() {
		_, err := chain.RunStep(context.Background(), Step{Kind: StepChat, Prompt: "probe ping"})
		stepErr <- err
	}()

	select {
	case <-modelGate.Reached():
	case <-time.After(5 * time.Second):
		t.Fatal("chat request never reached the local model endpoint")
	}
	if got := model.ChatHits(); got != 1 {
		t.Fatalf("chat hits in flight = %d, want 1", got)
	}

	chain.stop(errors.New("offline stop"))
	if !chain.isStopped() {
		t.Fatal("stop marker must be observable once stop returns")
	}
	// 放行 handler：它的 ctx 已被停止取消，请求不可撤销，但也不会被重试。
	modelGate.Release()

	select {
	case err := <-stepErr:
		if !errors.Is(err, ErrAlreadyStopped) {
			t.Fatalf("in-flight step = %v, want ErrAlreadyStopped (no retry after stop)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight step did not converge after the stop marker")
	}
	if got := model.ChatHits(); got != 1 {
		t.Fatalf("chat hits after stop = %d, want 1 (in-flight is not revoked, no new send/retry)", got)
	}
	if snap := chain.ProcessSnapshot(); snap.InFlight != 0 {
		t.Fatalf("process in-flight = %d, want 0", snap.InFlight)
	}
}

// TestSendGate_OriginalReviewStopWindowShapeHasNoSend 复用独立复核的 20,000 句柄形状：
// fake ES guard 阻塞一个已准入步骤，人工登记大量在途句柄放大"停止 → 取消"的耗时，
// 观察到停止标记后放行 guard。修复前该形状会命中 1 次 embedding；修复后必须恒为 0。
//
// 人工句柄只用于放大窗口，不代表一般负载下的触发概率。
func TestSendGate_OriginalReviewStopWindowShapeHasNoSend(t *testing.T) {
	const iterations = 5
	const artificialHandles = 20000
	for iteration := 0; iteration < iterations; iteration++ {
		if got := runStopWindowShape(t, artificialHandles); got != 0 {
			t.Fatalf("iteration %d: embedding sends after the stop marker = %d, want 0", iteration, got)
		}
	}
}

// runStopWindowShape 执行一次"已准入步骤阻塞在 guard → 停止 → 放行"的构造，
// 返回停止标记之后该步骤产生的模型命中数。
func runStopWindowShape(t *testing.T, artificialHandles int) int64 {
	t.Helper()
	model := NewLocalModelServer()
	defer model.Close()
	model.SetBehavior(ModelBehavior{EmbedDims: 8})

	plan := sendGateTestPlan(model)
	target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	defer target.Close()

	var enabled atomic.Bool
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	original := target.Server.Config.Handler
	target.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() {
			select {
			case reached <- struct{}{}:
			default:
			}
			<-release
		}
		original.ServeHTTP(w, r)
	})

	chain, err := NewChain(context.Background(), plan, target, ChainOptions{APIKey: "probe-offline-placeholder-key"})
	if err != nil {
		t.Fatalf("new probe chain: %v", err)
	}
	defer chain.Close()

	enabled.Store(true)
	result := make(chan error, 1)
	go func() {
		_, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
		result <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("guard did not reach the fake ES target")
	}

	// 只登记"已准入"句柄；实际 HTTP 只有被 guard 阻塞的那一个 embedding 步骤。
	releases := make([]func(), 0, artificialHandles)
	for i := 0; i < artificialHandles; i++ {
		_, rel, err := chain.beginAttempt(context.Background())
		if err != nil {
			t.Fatalf("beginAttempt #%d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()

	stopDone := make(chan struct{})
	go func() {
		chain.stop(errors.New("offline stop"))
		close(stopDone)
	}()
	waitStoppedMarker(t, chain)
	close(release)

	select {
	case <-stopDone:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not return")
	}
	select {
	case <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked step did not settle")
	}
	return model.EmbedHits()
}

// TestSendGate_TransportBoundaryRefusesAfterStopMarker 直接验证发送准入的线性化点：
// 停止标记发布之后，任何新的 RoundTrip 都必须被闸门拒绝。
//
// 这里刻意使用**未取消**的请求上下文：如果把“上下文已取消所以没发出”当成闸门的功劳，
// 用例就无法区分“有闸门”与“只靠 ctx 取消”。剥离 commitSend 的变异会让这个请求真的
// 到达 fake 模型（见 verification 证据 30-mutation-sendgate.txt）。
func TestSendGate_TransportBoundaryRefusesAfterStopMarker(t *testing.T) {
	model := NewLocalModelServer()
	t.Cleanup(model.Close)
	model.SetBehavior(ModelBehavior{EmbedDims: 8})
	chain := newSendGateChain(t, model, nil)

	chain.stop(errors.New("offline stop"))
	if !chain.isStopped() {
		t.Fatal("stop marker must be observable once stop returns")
	}

	// 三个角色共用同一个 providers.Transport，因此逐一验证闸门都在网络 I/O 之前拒绝。
	cases := []struct {
		name string
		path string
		hits func() int64
	}{
		{"chat", "/chat/completions", model.ChatHits},
		{"embedding", "/embeddings", model.EmbedHits},
		{"rerank", "/rerank", model.RerankHits},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				model.URL()+tc.path, strings.NewReader(`{"model":"probe-embed","input":["probe document"]}`))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := chain.gate.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, ErrAlreadyStopped) {
				t.Fatalf("RoundTrip() = %v, want ErrAlreadyStopped (the gate must refuse after the marker)", err)
			}
			if got := tc.hits(); got != 0 {
				t.Fatalf("hits = %d, want 0: the refusal must happen before any network I/O", got)
			}
		})
	}
}

// TestSendGate_ConcurrentStopConverges 用并发入口 + 停止做收敛性验证
// （配合 -race：不得出现数据竞争，且不得残留 in-flight / 预留）。
func TestSendGate_ConcurrentStopConverges(t *testing.T) {
	model := NewLocalModelServer()
	t.Cleanup(model.Close)
	model.SetBehavior(ModelBehavior{EmbedDims: 8, ChatUsage: true})
	chain := newSendGateChain(t, model, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			step := Step{Kind: StepChat, Prompt: "probe ping"}
			if i%2 == 1 {
				step = Step{Kind: StepEmbed, Texts: []string{"probe document"}}
			}
			// 成功或停止都允许；这里只断言收敛，不断言具体错误。
			_, _ = chain.RunStep(context.Background(), step)
		}(i)
	}
	// 让部分尝试进入入口准入后再发布停止标记。
	time.Sleep(5 * time.Millisecond)
	chain.stop(errors.New("offline concurrent stop"))

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent steps did not converge after the stop marker")
	}
	snap := chain.ProcessSnapshot()
	if snap.InFlight != 0 {
		t.Fatalf("process in-flight = %d, want 0", snap.InFlight)
	}
	if snap.InputReserved != 0 || snap.OutputReserved != 0 {
		t.Fatalf("process budget = %+v, want released reservations", snap)
	}
}

// TestSendGate_PostGatePreTransportWindowIsNotClosed 特征化复核第 3 轮 P1 的
// **残余窗口**：请求已经通过发送闸门（commitSend 已返回）、尚未交给底层 transport
// 时横跨停止标记，仍可能被发出并到达对端。
//
// 两个子例都用确定性同步点，而不是靠调度时序：
//   - gate.inner 包装把请求固定在“已过闸门、未 handoff”的位置；
//   - Chain.beforeCancel（仅测试钩子）把“停止标记已发布”与“取消在途句柄”分开。
//
// 结论（**未闭合**）：本包只能保证“停止标记后零新闸门准入、零新重试”，
// 不能保证字面“零新发送”。本测试记录事实，等待用户对
// design/stage2d-plan.md §9.1 的停止语义做决定；不得当作原契约通过。
func TestSendGate_PostGatePreTransportWindowIsNotClosed(t *testing.T) {
	// 取消先到：停止（含取消句柄）完成后才放行 handoff。
	// 本轮观测到 0 命中，但这不是保证：取消是并发的、尽力而为的，
	// 落后于 handoff 时请求照常发出（下一个子例）。
	t.Run("cancel_applied_before_handoff", func(t *testing.T) {
		model := NewLocalModelServer()
		t.Cleanup(model.Close)
		model.SetBehavior(ModelBehavior{EmbedDims: 8})
		chain := newSendGateChain(t, model, nil)

		postGate := make(chan struct{}, 1)
		release := make(chan struct{})
		inner := chain.gate.inner
		chain.gate.inner = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			postGate <- struct{}{}
			<-release
			return inner.RoundTrip(req)
		})

		stepErr := make(chan error, 1)
		go func() {
			_, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
			stepErr <- err
		}()
		select {
		case <-postGate:
		case <-time.After(5 * time.Second):
			t.Fatal("request never passed the send gate")
		}
		if got := model.EmbedHits(); got != 0 {
			t.Fatalf("embed hits before handoff = %d, want 0", got)
		}

		// stop 同步返回：停止标记已发布，且取消已施加到在途句柄。
		chain.stop(errors.New("offline stop"))
		close(release)

		select {
		case err := <-stepErr:
			if !errors.Is(err, ErrAlreadyStopped) {
				t.Fatalf("step = %v, want ErrAlreadyStopped", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("step did not settle")
		}
		if got := model.EmbedHits(); got != 0 {
			t.Fatalf("embed hits = %d, want 0 in this ordering (cancel reached the transport first)", got)
		}
		if snap := chain.ProcessSnapshot(); snap.InFlight != 0 {
			t.Fatalf("process in-flight = %d, want 0", snap.InFlight)
		}
		t.Logf("cancel-before-handoff: hits=0; not a guarantee, cancellation races the handoff")
	})

	// 取消滞后：标记已发布、取消尚未施加时放行 handoff，请求真的到达对端。
	// 这正是复核用 100,000 个句柄放大出的时序（人工句柄只构造时序，
	// 不代表常规触发概率）。
	t.Run("stop_marker_does_not_block_handoff", func(t *testing.T) {
		model := NewLocalModelServer()
		t.Cleanup(model.Close)
		model.SetBehavior(ModelBehavior{EmbedDims: 8})
		chain := newSendGateChain(t, model, nil)

		var closeAtCancel sync.Once
		atCancel := make(chan struct{})
		releaseCancel := make(chan struct{})
		chain.beforeCancel = func() {
			closeAtCancel.Do(func() { close(atCancel) })
			<-releaseCancel
		}
		t.Cleanup(func() {
			select {
			case <-releaseCancel:
			default:
				close(releaseCancel)
			}
		})

		postGate := make(chan struct{}, 1)
		release := make(chan struct{})
		inner := chain.gate.inner
		chain.gate.inner = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			postGate <- struct{}{}
			<-release
			return inner.RoundTrip(req)
		})

		stepErr := make(chan error, 1)
		go func() {
			_, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
			stepErr <- err
		}()
		select {
		case <-postGate:
		case <-time.After(5 * time.Second):
			t.Fatal("request never passed the send gate")
		}
		if chain.isStopped() {
			t.Fatal("stop marker published before the test stopped the chain")
		}
		if got := model.EmbedHits(); got != 0 {
			t.Fatalf("embed hits before the stop marker = %d, want 0", got)
		}

		stopDone := make(chan struct{})
		go func() {
			chain.stop(errors.New("offline stop"))
			close(stopDone)
		}()
		select {
		case <-atCancel:
		case <-time.After(5 * time.Second):
			t.Fatal("stop never reached the cancel phase")
		}
		if !chain.isStopped() {
			t.Fatal("stop marker must be observable before the cancel phase")
		}
		if got := model.EmbedHits(); got != 0 {
			t.Fatalf("embed hits after the marker but before handoff = %d, want 0", got)
		}

		// 放行 handoff：此时句柄尚未被取消，请求带着仍然有效的 ctx 被交给 transport。
		close(release)
		deadline := time.Now().Add(5 * time.Second)
		for model.EmbedHits() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := model.EmbedHits(); got != 1 {
			t.Fatalf("embed hits after handoff = %d, want 1 (the request had already passed the gate)", got)
		}

		close(releaseCancel)
		select {
		case <-stopDone:
		case <-time.After(5 * time.Second):
			t.Fatal("stop did not finish")
		}
		select {
		case err := <-stepErr:
			t.Logf("step error after an in-flight send: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("step did not settle")
		}
		if snap := chain.ProcessSnapshot(); snap.InFlight != 0 {
			t.Fatalf("process in-flight = %d, want 0", snap.InFlight)
		}
		t.Logf("OBSERVED: a request that had passed the send gate was handed to the transport " +
			"after the stop marker and reached the local fake model (hits=1); window NOT closed")
	})
}

// TestStepReport_CountsSeparateCommitFromSend 把“预算承诺 / 通过闸门 / 交给 transport /
// 对端接收”拆成可分别断言的字段（复核第 3 轮 P2）：
// 闸门拒绝时 Attempts/CommittedAttempts 仍为 1（预算保守结算，不回滚），
// 但 GateAdmissions/TransportCalls 为 0、对端命中为 0。
func TestStepReport_CountsSeparateCommitFromSend(t *testing.T) {
	t.Run("gate_rejected_keeps_committed_budget", func(t *testing.T) {
		model := NewLocalModelServer()
		t.Cleanup(model.Close)
		model.SetBehavior(ModelBehavior{EmbedDims: 8})
		chain := newSendGateChain(t, model, nil)

		atGate := make(chan struct{}, 1)
		release := make(chan struct{})
		chain.gate.beforeCommit = func() {
			select {
			case atGate <- struct{}{}:
			default:
			}
			<-release
		}

		type result struct {
			report *StepReport
			err    error
		}
		done := make(chan result, 1)
		go func() {
			report, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
			done <- result{report: report, err: err}
		}()
		select {
		case <-atGate:
		case <-time.After(5 * time.Second):
			t.Fatal("attempt never reached the send gate")
		}
		chain.stop(errors.New("offline stop"))
		close(release)

		var res result
		select {
		case res = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("step did not settle")
		}
		if !errors.Is(res.err, ErrAlreadyStopped) {
			t.Fatalf("step = %v, want ErrAlreadyStopped", res.err)
		}
		if res.report == nil {
			t.Fatal("report = nil, want the refusal report")
		}
		if got := model.EmbedHits(); got != 0 {
			t.Fatalf("embed hits = %d, want 0 (refused at the gate)", got)
		}
		// 预算承诺发生在闸门之前：闸门拒绝不回滚它（保守结算），但闸门/传输计数为 0。
		if res.report.Attempts != 1 || res.report.CommittedAttempts != 1 {
			t.Fatalf("budget counters = attempts %d / committed %d, want 1 / 1",
				res.report.Attempts, res.report.CommittedAttempts)
		}
		if res.report.GateAdmissions != 0 || res.report.TransportCalls != 0 {
			t.Fatalf("send counters = gate %d / transport %d, want 0 / 0",
				res.report.GateAdmissions, res.report.TransportCalls)
		}
		snap := chain.ProcessSnapshot()
		if snap.InFlight != 0 || snap.InputReserved != 0 || snap.OutputReserved != 0 {
			t.Fatalf("process budget = %+v, want released lease", snap)
		}
		if snap.CommittedAttempts != 1 {
			t.Fatalf("process committed attempts = %d, want 1 (admission is monotonic)", snap.CommittedAttempts)
		}
	})

	t.Run("sent_once", func(t *testing.T) {
		model := NewLocalModelServer()
		t.Cleanup(model.Close)
		model.SetBehavior(ModelBehavior{EmbedDims: 8})
		chain := newSendGateChain(t, model, nil)

		report, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
		if err != nil {
			t.Fatalf("RunStep() = %v, want nil", err)
		}
		if report.Attempts != 1 || report.GateAdmissions != 1 || report.TransportCalls != 1 {
			t.Fatalf("counters = attempts %d / gate %d / transport %d, want 1 / 1 / 1",
				report.Attempts, report.GateAdmissions, report.TransportCalls)
		}
		if got := model.EmbedHits(); got != 1 {
			t.Fatalf("peer hits = %d, want 1", got)
		}
	})

	t.Run("retry_counts_each_attempt", func(t *testing.T) {
		model := NewLocalModelServer()
		t.Cleanup(model.Close)
		// 首次请求以连接中断结束：触发一次受控重试（第二次成功）。
		model.SetBehavior(ModelBehavior{EmbedDims: 8, EmbedAbortFirst: 1})
		chain := newSendGateChain(t, model, func(plan *Plan) {
			plan.Retry = RetryPolicy{MaxAttempts: 2, RetryOnTransportError: true}
		})

		report, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"probe document"}})
		if err != nil {
			t.Fatalf("RunStep() = %v, want nil after the retry", err)
		}
		if report.Attempts != 2 || report.GateAdmissions != 2 || report.TransportCalls != 2 {
			t.Fatalf("counters = attempts %d / gate %d / transport %d, want 2 / 2 / 2",
				report.Attempts, report.GateAdmissions, report.TransportCalls)
		}
		if got := model.EmbedHits(); got != 2 {
			t.Fatalf("peer hits = %d, want 2 (one per attempt)", got)
		}
	})
}
