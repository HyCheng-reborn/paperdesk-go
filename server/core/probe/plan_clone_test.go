package probe_test

// 复核 P2（浅拷贝可变 Plan 导致真实数据竞争）的回归测试。
//
// 反例（修复前，见 verification/independent-2d-3aea024-20261028/budget-review-plan-race.log）：
// NewChain 只保存 Plan 的值副本，但 Budget.Unlimited 的底层数组仍与调用方共享；
// 本批新增的 Run → Preflight → BudgetPlan.Validate 在运行期读取该切片，
// 与"构造后并发修改原 Plan"构成真实 DATA RACE。
//
// 修复后：NewChain 对 Plan 做防御性深拷贝，Run/RunStep 在入口克隆步骤，
// 因此构造之后修改原 Plan（含 Unlimited 切片）既不改变已固定的预算/授权，
// 也不会与运行期的读取竞争。

import (
	"context"
	"errors"
	"testing"

	"github.com/wangle201210/go-rag/server/core/probe"
	"github.com/wangle201210/go-rag/server/core/providers"
)

// TestNewChain_FixedBudgetAndAuthorizationAfterConstruction：构造后修改原配置
// 不得改变已固定的预算、重试与授权。
func TestNewChain_FixedBudgetAndAuthorizationAfterConstruction(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, _ *probe.ModelBehavior) {
		plan.Budget.MaxRequests = 1
		plan.Budget.Unlimited = []string{"requests"} // 同时给出正上限是合法的显式声明
	})

	// 构造返回后修改调用方自己的计划副本。
	h.Plan.Budget.MaxRequests = 100
	h.Plan.Budget.Unlimited[0] = "bogus" // 若链仍共享该切片，Preflight 会报 ErrBudgetInvalid
	h.Plan.Budget.StepDeadline = 0
	h.Plan.Retry.MaxAttempts = 9
	h.Plan.Authorization.OnlineApproved = false

	report, err := h.Chain.Run(context.Background(), probe.DefaultSteps()[:2])
	if errors.Is(err, probe.ErrBudgetInvalid) {
		t.Fatalf("Run() = %v: the chain still aliases the caller's Budget.Unlimited slice", err)
	}
	if !errors.Is(err, providers.ErrBudgetExhausted) {
		t.Fatalf("Run() = %v, want ErrBudgetExhausted from the fixed MaxRequests=1 budget", err)
	}
	if got := h.Model.ChatHits(); got != 1 {
		t.Fatalf("chat hits = %d, want 1 (only the first admitted attempt)", got)
	}
	if got := h.Model.EmbedHits(); got != 0 {
		t.Fatalf("embed hits = %d, want 0 (budget fixed at construction)", got)
	}
	if !report.Stopped || report.StopReason != "category=budget_exhausted" {
		t.Fatalf("report = %+v, want a stopped run with the budget category", report)
	}
}

// TestNewChain_MutablePlanRaceRegression 等价复现复核报告的 DATA RACE 反例：
// 构造完成后并发修改原 Plan 的 Budget.Unlimited 切片，同时反复运行探针。
// 该用例必须在 -race 下通过（修复前会报告 WARNING: DATA RACE 并 exit 1）。
func TestNewChain_MutablePlanRaceRegression(t *testing.T) {
	h := newProbeHarness(t, func(plan *probe.Plan, _ *probe.ModelBehavior) {
		plan.Budget.MaxRequests = 1000
		plan.Budget.MaxInputTokens = 1_000_000
		plan.Budget.MaxOutputTokens = 1_000_000
		plan.Budget.Unlimited = []string{"requests"}
	})

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < 5000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h.Plan.Budget.Unlimited[0] = "requests"
			h.Plan.Budget.Unlimited[0] = "input_tokens"
		}
	}()

	const runs = 100
	var runErr error
	for i := 0; i < runs; i++ {
		if _, err := h.Chain.Run(context.Background(), []probe.Step{{Kind: probe.StepChat, Prompt: "probe ping"}}); err != nil {
			runErr = err
			break
		}
	}
	close(stop)
	<-writerDone

	if runErr != nil {
		t.Fatalf("concurrent Run() = %v, want no error while the caller mutates its own plan", runErr)
	}
	if got := h.Chain.ProcessSnapshot().CommittedAttempts; got != runs {
		t.Fatalf("committed attempts = %d, want %d (budget/authorization must stay fixed)", got, runs)
	}
}

// TestRunStep_ClonesStepInputs：调用方在步骤返回后修改自己的 Texts/Docs，
// 不得影响已经结束的运行（入口克隆，不保留调用方切片）。
func TestRunStep_ClonesStepInputs(t *testing.T) {
	h := newProbeHarness(t, nil)

	texts := []string{"probe document"}
	step := probe.Step{Kind: probe.StepEmbed, Texts: texts}
	report, err := h.Chain.RunStep(context.Background(), step)
	if err != nil {
		t.Fatalf("RunStep() unexpected error: %v", err)
	}
	texts[0] = "mutated-after-the-call"
	if report.VectorCount != 1 || len(report.VectorDims) != 1 {
		t.Fatalf("embed report = %+v, want one vector", report)
	}
	if got := h.Model.EmbedHits(); got != 1 {
		t.Fatalf("embed hits = %d, want 1", got)
	}

	docs := probe.ProbeDocuments()
	docs[0].Content = "mutated-before-the-call"
	rerank := probe.Step{Kind: probe.StepRerank, Query: "probe query", Docs: docs, TopK: 2}
	if _, err := h.Chain.RunStep(context.Background(), rerank); err != nil {
		t.Fatalf("rerank RunStep() unexpected error: %v", err)
	}
	// 运行期只读私有副本：调用方随后改文档内容不得影响已完成的结果。
	docs[0].Content = "mutated-after-the-call"
	if got := h.Model.RerankHits(); got != 1 {
		t.Fatalf("rerank hits = %d, want 1", got)
	}
}
