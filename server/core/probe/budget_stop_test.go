package probe

// 复核第 4 轮 P2 的特征化测试：停止标记后仍可能新增**预算承诺**。
//
// 事实（可观测）：
//   - 预算 Reserve → Commit 发生在角色适配器内部（`validatedEmbedRole`
//     /`budgetEmbedRole`），位置在发送闸门 `commitSend` **之前**；
//   - `stop` 只发布停止标记并取消在途句柄的 ctx，不参与预算账本；
//   - 因此一个"已准入（beginAttempt）、尚未到闸门"的尝试，如果横跨停止标记，
//     仍会完成一次 Reserve→Commit，随后才被闸门拒绝（对端零命中）。
//
// 本批**不**把这条写成"已满足停止标记后零新预算提交"：它是待决边界
// （见 design/stage2d-plan.md §9.1 与 §9.2 的预算参与说明）。
// 能保证的是：停止标记之后不再有新的**入口准入**，因此这类承诺只可能属于
// 标记前已准入的尝试；已承诺账本保守结算（不退款）、lease 恰好终结一次、
// slot 归零、`errors.Is` 错误链保留 `ErrAlreadyStopped`。
//
// `Chain.beforeCancel` 是仅测试用的同步点（生产为 nil），用于把
// "标记已发布"与"取消已施加"确定性地分开；它固定的是时序，不代表常规负载触发概率。
// 模型零命中同样不等于预算零承诺。

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestStopMarkerDoesNotRetractBudgetCommit(t *testing.T) {
	model := NewLocalModelServer()
	defer model.Close()
	model.SetBehavior(ModelBehavior{EmbedDims: 8})

	plan := sendGateTestPlan(model)
	target, err := NewLocalFakeESTarget(plan.Target, plan.Embed.Model, plan.Embed.Dimensions)
	if err != nil {
		t.Fatalf("new local fake es target: %v", err)
	}
	defer target.Close()

	// 在 fake ES 的 guard 读取处加一个可控闸门：把一次已准入的尝试固定在
	// "已准入、尚未到发送闸门"的位置（guard 在预留/承诺与发送之前）。
	original := target.Server.Config.Handler
	var enabled atomic.Bool
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
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

	releaseCancel := make(chan struct{})
	chain.beforeCancel = func() { <-releaseCancel }
	t.Cleanup(func() {
		// 防止失败路径把 stop 的钩子永久阻塞。
		select {
		case <-releaseCancel:
		default:
			close(releaseCancel)
		}
		select {
		case <-release:
		default:
			close(release)
		}
	})
	enabled.Store(true)

	type result struct {
		report *StepReport
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := chain.RunStep(context.Background(), Step{Kind: StepEmbed, Texts: []string{"budget-after-stop"}})
		done <- result{report: report, err: err}
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("guard was never reached")
	}

	stopDone := make(chan struct{})
	go func() {
		chain.stop(errors.New("offline review stop"))
		close(stopDone)
	}()
	waitStoppedMarker(t, chain)

	before := chain.ProcessSnapshot().CommittedAttempts
	close(release)

	deadline := time.Now().Add(4 * time.Second)
	for chain.ProcessSnapshot().CommittedAttempts == before {
		if time.Now().After(deadline) {
			t.Fatal("no budget commit observed after the stop marker; the characterization no longer holds")
		}
		time.Sleep(time.Millisecond)
	}
	after := chain.ProcessSnapshot().CommittedAttempts

	close(releaseCancel)
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the cancel hook was released")
	}

	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("step did not settle")
	}

	// 1) 反例事实：停止标记之后新增了一次预算承诺（只可能属于标记前已准入的尝试）。
	if before != 0 || after != 1 {
		t.Fatalf("committed before/after = %d/%d, want 0/1", before, after)
	}
	// 2) 对端零命中、闸门与 transport 均未发生：承诺 ≠ 发送。
	if hits := model.EmbedHits(); hits != 0 {
		t.Fatalf("embed hits = %d, want 0 (the gate must refuse after the marker)", hits)
	}
	if got.report == nil {
		t.Fatal("step must return a report")
	}
	if got.report.Attempts != 1 || got.report.GateAdmissions != 0 || got.report.TransportCalls != 0 {
		t.Fatalf("step report counts = attempts/gate/transport %d/%d/%d, want 1/0/0",
			got.report.Attempts, got.report.GateAdmissions, got.report.TransportCalls)
	}
	// 3) 错误链保留 ErrAlreadyStopped，调用方仍可识别停止原因。
	if !errors.Is(got.err, ErrAlreadyStopped) {
		t.Fatalf("step error = %v, want errors.Is ErrAlreadyStopped", got.err)
	}
	// 4) 保守结算：不退款、lease 终结、slot 归零、无残留预留。
	snapshot := chain.ProcessSnapshot()
	if snapshot.CommittedAttempts != 1 {
		t.Fatalf("final committed = %d, want 1 (no refund after abort)", snapshot.CommittedAttempts)
	}
	if snapshot.InFlight != 0 || snapshot.InputReserved != 0 || snapshot.OutputReserved != 0 {
		t.Fatalf("final budget = %+v, want inFlight/reserved 0", snapshot)
	}
	if snapshot.InputUsed <= 0 {
		t.Fatalf("input used = %d, want conservative settlement > 0", snapshot.InputUsed)
	}
	t.Logf("OBSERVED commit after stop marker: before=%d after=%d gate=%d transport=%d hits=%d inputUsed=%d inFlight=%d; NOT a guarantee of zero commits after the marker",
		before, after, got.report.GateAdmissions, got.report.TransportCalls, model.EmbedHits(), snapshot.InputUsed, snapshot.InFlight)
}
