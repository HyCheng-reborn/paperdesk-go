package probe_test

// 复核 P1（并发停止后仍发起新重试）的回归测试。
//
// 反例（修复前）：Chat 已在途并阻塞，另一个 Embed 因唯一并发额度不足返回
// ErrBudgetExhausted 并停止链；随后放行 Chat 并模拟连接中断，Chat 仍从原端点
// 发起第二次请求——停止时命中数为 1，之后变成 2。
//
// 修复后的契约：全局停止与新发送/重试准入在同一把锁下协调，且停止会取消
// 已准入的在途尝试，因此
//   - 停止之后不会再有新的发送或新的重试；
//   - 已准入的尝试计入预算后收敛（in-flight 归零），不会泄漏 lease；
//   - 停止之后的任何入口调用都返回 ErrAlreadyStopped。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/providers"
)

func TestConcurrentStop_RefusesNewRetryAndConverges(t *testing.T) {
	gate := probe.NewGate()
	h := newProbeHarness(t, func(plan *probe.Plan, behavior *probe.ModelBehavior) {
		plan.Retry = probe.RetryPolicy{MaxAttempts: 2, RetryOnTransportError: true}
		plan.Budget.MaxConcurrency = 1
		behavior.ChatGate = gate
		// 修复前的反例：首个尝试"中断"后仍会发起第二次发送（命中数 1 → 2）。
		behavior.ChatAbortFirst = 1
	})
	// 夹具清理顺序（LIFO）：先放行阻塞的 handler，再关闭本机模型对端。
	t.Cleanup(gate.Release)

	firstErrCh := make(chan error, 1)
	go func() {
		_, err := h.Chain.RunStep(context.Background(), probe.Step{Kind: probe.StepChat, Prompt: "probe ping"})
		firstErrCh <- err
	}()

	select {
	case <-gate.Reached():
	case <-time.After(3 * time.Second):
		t.Fatal("chat request never reached the local model endpoint")
	}

	// 第二个步骤因唯一并发额度不足失败 → 立即触发全局停止（不等第一个步骤结束）。
	report, err := h.Chain.RunStep(context.Background(), probe.Step{Kind: probe.StepEmbed, Texts: []string{"probe document"}})
	if !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("second step = %v, want ErrBudgetExhausted", err)
	}
	if report.Attempts != 0 || report.CommittedAttempts != 0 {
		t.Fatalf("second step report = %+v, want zero attempts/commits", report)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits at stop = %d, want 1 (only the in-flight attempt)", got)
	}

	// 放行在途请求：它已被全局停止取消，不得再发起重试。
	gate.Release()
	select {
	case firstErr := <-firstErrCh:
		if firstErr == nil {
			t.Fatal("in-flight step must report the global stop, got nil")
		}
		if !errors.Is(firstErr, probe.ErrAlreadyStopped) {
			t.Fatalf("in-flight step = %v, want ErrAlreadyStopped (a new retry must not be sent)", firstErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight step did not converge after the global stop")
	}

	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits after stop = %d, want 1 (the cancelled attempt must not be retried)", got)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 (the rejected step must not send)", got)
	}

	snap := h.Chain.ProcessSnapshot()
	if snap.InFlight != 0 {
		t.Fatalf("process in-flight = %d, want 0 (all admitted work must converge)", snap.InFlight)
	}
	if snap.CommittedAttempts != 1 || snap.AttemptsUsed != 1 {
		t.Fatalf("process budget = %+v, want exactly 1 admitted attempt", snap)
	}
	if snap.InputReserved != 0 || snap.OutputReserved != 0 {
		t.Fatalf("process budget = %+v, want released reservations", snap)
	}

	// 停止之后的任何新入口都必须在发送前被拒绝。
	if _, err := h.Chain.RunStep(context.Background(), probe.Step{Kind: probe.StepChat, Prompt: "probe ping"}); !errors.Is(err, probe.ErrAlreadyStopped) {
		t.Fatalf("RunStep() after stop = %v, want ErrAlreadyStopped", err)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits after a post-stop RunStep = %d, want 1", got)
	}
}
