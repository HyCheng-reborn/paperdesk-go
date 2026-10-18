package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
)

// est 是测试常用的最小 token 估算。
func est(in, out int64) *TokenEstimate { return &TokenEstimate{InputTokens: in, MaxOutput: out} }

// usage 构造一个已知的 model.TokenUsage。
func usage(prompt, completion int) *model.TokenUsage {
	return &model.TokenUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

// TestBudget_ConcurrentReserve_MaxRequests：MaxRequests=1 时并发实际发送计数为 1。
// 采用 barrier channel 触发并发，超时兜底，不依赖固定 sleep。
func TestBudget_ConcurrentReserve_MaxRequests(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(1, 0, 0, 0) // 进程级 maxRequests=1，其余不限
	rb, err := pb.NewRequestBudget(ctx, WithMaxRequests(1))
	if err != nil {
		t.Fatalf("NewRequestBudget: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var okCount, exhausted, sendCount int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // barrier：所有 goroutine 同时冲向 Reserve
			lease, err := rb.Reserve(est(1, 1))
			if err != nil {
				if errors.Is(err, ErrBudgetExhausted) {
					atomic.AddInt64(&exhausted, 1)
				}
				return
			}
			atomic.AddInt64(&okCount, 1)
			// reserved → committed（进入实际发送边界），随后“发送”计数 +1
			if cerr := lease.Commit(); cerr != nil {
				t.Errorf("Commit: %v", cerr)
				return
			}
			atomic.AddInt64(&sendCount, 1)
			lease.Finish(usage(1, 1))
		}()
	}

	close(start)
	waitDone(t, &wg, 5*time.Second)

	if got := atomic.LoadInt64(&okCount); got != 1 {
		t.Fatalf("successful reserves = %d, want exactly 1", got)
	}
	if got := atomic.LoadInt64(&sendCount); got != 1 {
		t.Fatalf("actual send count = %d, want exactly 1", got)
	}
	if got := atomic.LoadInt64(&exhausted); got != n-1 {
		t.Fatalf("exhausted = %d, want %d", got, n-1)
	}
}

// TestBudget_DualScope_PartialFailure：请求足够/进程不足，或请求不足/进程足够，
// 两种情况均零发送且完全回退（无任何残留计数）。
func TestBudget_DualScope_PartialFailure(t *testing.T) {
	ctx := context.Background()

	// Case A：进程级输入 token 不足，请求级充足
	pbA := NewProcessBudget(0, 5, 0, 0) // 进程 maxInputTokens=5
	rbA, _ := pbA.NewRequestBudget(ctx, WithMaxInputTokens(100))
	assertZeroSendFullRollback(t, "process-insufficient", pbA, rbA, est(10, 0))

	// Case B：请求级输入 token 不足，进程级充足
	pbB := NewProcessBudget(0, 100, 0, 0)
	rbB, _ := pbB.NewRequestBudget(ctx, WithMaxInputTokens(5))
	assertZeroSendFullRollback(t, "request-insufficient", pbB, rbB, est(10, 0))

	// Case C：进程级并发不足，请求级充足（slot 维度）
	pbC := NewProcessBudget(0, 0, 0, 1) // 进程 maxConcurrency=1
	rbC, _ := pbC.NewRequestBudget(ctx, WithMaxConcurrency(10))
	// 先占满进程 slot
	held, err := rbC.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("first reserve should hold the only process slot: %v", err)
	}
	if cerr := held.Commit(); cerr != nil {
		t.Fatalf("commit: %v", cerr)
	}
	// 第二个请求：进程 slot 已满 → 拒绝，且不残留
	if _, err := rbC.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second reserve must be exhausted by process concurrency, got %v", err)
	}
	// 请求级作用域不得因失败的进程级校验而残留
	if s := rbC.Snapshot(); s.AttemptsUsed != 1 || s.InFlight != 1 {
		t.Fatalf("request scope residue after dual-scope failure: %+v", s)
	}
	held.Close() // 释放，避免泄漏
}

// assertZeroSendFullRollback 校验一次注定失败的 Reserve 后两个作用域完全无残留，
// 且随后一个“两作用域都满足”的 Reserve 能成功（证明失败未污染额度）。
func assertZeroSendFullRollback(t *testing.T, name string, pb *ProcessBudget, rb *RequestBudget, e *TokenEstimate) {
	t.Helper()
	if _, err := rb.Reserve(e); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("[%s] expected ErrBudgetExhausted, got %v", name, err)
	}
	ps := pb.Snapshot()
	if ps.AttemptsUsed != 0 || ps.InFlight != 0 || ps.InputReserved != 0 || ps.OutputReserved != 0 {
		t.Fatalf("[%s] process scope residue: %+v", name, ps)
	}
	rs := rb.Snapshot()
	if rs.AttemptsUsed != 0 || rs.InFlight != 0 || rs.InputReserved != 0 || rs.OutputReserved != 0 {
		t.Fatalf("[%s] request scope residue: %+v", name, rs)
	}
	// 失败后仍能进行一个满足两作用域的小请求 → 完全回退
	fit := rb.mustReserveFit(t, name)
	fit.Close()
}

// mustReserveFit 预留一个恰好满足进程级上限的小请求（此处输入 token=5，等于进程上限）。
func (rb *RequestBudget) mustReserveFit(t *testing.T, name string) *AttemptLease {
	t.Helper()
	l, err := rb.Reserve(est(5, 0))
	if err != nil {
		t.Fatalf("[%s] fitting reserve after rollback failed: %v", name, err)
	}
	return l
}

// TestBudget_RetryNoRefresh：重试/子步骤/后台接管不刷新累计额度。
func TestBudget_RetryNoRefresh(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(0, 0, 0, 0)
	tb, err := pb.NewTaskBudget(ctx, WithMaxRequests(2))
	if err != nil {
		t.Fatalf("NewTaskBudget: %v", err)
	}

	// 两次尝试（模拟重试/子步骤），累计消耗额度
	for i := 0; i < 2; i++ {
		l, err := tb.Reserve(est(1, 1))
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if cerr := l.Commit(); cerr != nil {
			t.Fatalf("commit %d: %v", i, cerr)
		}
		l.Finish(usage(1, 1))
	}
	// 第三次：额度未刷新 → 耗尽
	if _, err := tb.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("third reserve must be exhausted, got %v", err)
	}
	// 后台接管：共享同一累计，不刷新
	bg, err := tb.TransferToBackground()
	if err != nil {
		t.Fatalf("TransferToBackground: %v", err)
	}
	if _, err := bg.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("background must not refresh cumulative budget, got %v", err)
	}
	// 同步侧句柄移交后失效
	if _, err := tb.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetTransferred) {
		t.Fatalf("sync side must be deactivated after transfer, got %v", err)
	}
	// 累计 committed=2、attemptsUsed=2（单调，未刷新）
	s := bg.Snapshot()
	if s.CommittedAttempts != 2 || s.AttemptsUsed != 2 {
		t.Fatalf("cumulative not preserved: %+v", s)
	}
}

// TestBudget_CommittedNoRefund：发送后（已 commit）网络失败不返还尝试计数，但释放 slot。
func TestBudget_CommittedNoRefund(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(1, 0, 0, 0) // maxRequests=1
	rb, _ := pb.NewRequestBudget(ctx, WithMaxRequests(1))

	l, err := rb.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if cerr := l.Commit(); cerr != nil {
		t.Fatalf("commit: %v", cerr)
	}
	// 模拟发送后网络失败
	l.Abort(errors.New("network failure after send"))

	s := rb.Snapshot()
	if s.CommittedAttempts != 1 || s.AttemptsUsed != 1 {
		t.Fatalf("committed attempt must not be refunded: %+v", s)
	}
	if s.InFlight != 0 {
		t.Fatalf("slot must be released after abort: %+v", s)
	}
	// 尝试计数未返还 → 第二次预留失败
	if _, err := rb.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second reserve must fail (no refund), got %v", err)
	}
}

// TestBudget_LeaseLifecycle：完整生命周期 占 slot→commit→结算→释放 slot。
func TestBudget_LeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(0, 0, 0, 0)
	rb, _ := pb.NewRequestBudget(ctx)

	l, err := rb.Reserve(est(10, 20))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// 预留后：slot 占用、尝试预留、token 预留，未 commit
	s := rb.Snapshot()
	if s.InFlight != 1 || s.AttemptsUsed != 1 || s.CommittedAttempts != 0 ||
		s.InputReserved != 10 || s.OutputReserved != 20 {
		t.Fatalf("after reserve: %+v", s)
	}
	if l.Committed() || l.Finalized() {
		t.Fatal("lease must be reserved-not-committed initially")
	}

	// commit：承诺计数 +1，slot 仍占用
	if cerr := l.Commit(); cerr != nil {
		t.Fatalf("commit: %v", cerr)
	}
	s = rb.Snapshot()
	if s.CommittedAttempts != 1 || s.InFlight != 1 {
		t.Fatalf("after commit: %+v", s)
	}
	if !l.Committed() {
		t.Fatal("lease must report committed")
	}
	// 幂等：重复 Commit 不重复计数
	if cerr := l.Commit(); cerr != nil {
		t.Fatalf("idempotent commit: %v", cerr)
	}
	if s2 := rb.Snapshot(); s2.CommittedAttempts != 1 {
		t.Fatalf("repeat commit must not double count: %+v", s2)
	}

	// Finish(usage)：按实际结算，释放预留与 slot
	l.Finish(usage(5, 7))
	s = rb.Snapshot()
	if s.InFlight != 0 {
		t.Fatalf("slot must be released: %+v", s)
	}
	if s.InputReserved != 0 || s.OutputReserved != 0 {
		t.Fatalf("reservation must be cleared: %+v", s)
	}
	if s.InputUsed != 5 || s.OutputUsed != 7 {
		t.Fatalf("settled to actual usage: %+v", s)
	}
	if !l.Finalized() {
		t.Fatal("lease must report finalized")
	}
}

// TestBudget_IdempotentFinalize：重复 Finish/Abort/Close 不重复退款/结算。
func TestBudget_IdempotentFinalize(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(0, 0, 0, 0)
	rb, _ := pb.NewRequestBudget(ctx)

	// 已 commit 的 lease：重复终结只按首次结算一次
	l, _ := rb.Reserve(est(10, 10))
	_ = l.Commit()
	l.Finish(usage(4, 6))
	l.Abort(errors.New("late abort"))
	l.Close()
	l.Finish(usage(100, 100)) // 不得覆盖首次结算
	s := rb.Snapshot()
	if s.InputUsed != 4 || s.OutputUsed != 6 {
		t.Fatalf("must settle exactly once with first usage: %+v", s)
	}
	if s.InFlight != 0 || s.CommittedAttempts != 1 || s.AttemptsUsed != 1 {
		t.Fatalf("post-finalize state: %+v", s)
	}

	// 未 commit 的 lease：重复 Abort/Close 只回退一次，计数不为负
	l2, _ := rb.Reserve(est(5, 5))
	l2.Abort(errors.New("x"))
	l2.Abort(errors.New("y"))
	l2.Close()
	s2 := rb.Snapshot()
	// l(committed) 使 attemptsUsed=1；l2 预留 +1=2，未 commit 回退 -1=1
	if s2.AttemptsUsed != 1 {
		t.Fatalf("refund must happen exactly once: %+v", s2)
	}
	if s2.InFlight != 0 {
		t.Fatalf("inFlight must not go negative: %+v", s2)
	}
	if s2.InputReserved != 0 || s2.OutputReserved != 0 {
		t.Fatalf("reservation refund exactly once: %+v", s2)
	}
}

// TestBudget_SlotRelease_NonStream：httptest 阻塞 body 时，下个请求不越过并发上限；
// body 消费完成并 Close 后 slot 释放，后续请求可用。
func TestBudget_SlotRelease_NonStream(t *testing.T) {
	ctx := context.Background()
	var releaseOnce sync.Once
	release := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // 确保客户端 Do 先返回，body 读取阻塞
		}
		enterOnce.Do(func() { close(entered) })
		<-release // 阻塞 body：模拟慢响应
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	defer releaseOnce.Do(func() { close(release) }) // 兜底解除阻塞，避免 goroutine 泄漏

	pb := NewProcessBudget(0, 0, 0, 1) // maxConcurrency=1
	rb, _ := pb.NewRequestBudget(ctx, WithMaxConcurrency(1))
	tr := NewTransport(WithHTTPClient(srv.Client()))

	// 请求 1：预留+commit 后发起 HTTP，body 阻塞期间持有 slot
	l1, err := rb.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("reserve req1: %v", err)
	}
	if cerr := l1.Commit(); cerr != nil {
		t.Fatalf("commit req1: %v", cerr)
	}
	bodyDone := make(chan struct{})
	go func() {
		defer close(bodyDone)
		defer l1.Finish(usage(1, 1)) // body 消费/关闭后释放 slot（先于 bodyDone）
		req, rerr := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("{}"))
		if rerr != nil {
			return
		}
		resp, derr := tr.Do(req)
		if derr != nil {
			return
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body) // 阻塞直到 handler 释放
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not entered (request not in-flight)")
	}

	// 请求 2：slot 被 req1 占用（body 阻塞中）→ 不得越过并发上限
	if _, err := rb.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("req2 must be blocked by concurrency limit while body pending, got %v", err)
	}

	// 释放 body → req1 完成 → slot 释放
	releaseOnce.Do(func() { close(release) })
	select {
	case <-bodyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("body read did not finish")
	}

	// 请求 3：slot 已释放 → 成功
	l3, err := rb.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("req3 must reuse freed slot: %v", err)
	}
	_ = l3.Commit()
	l3.Finish(usage(1, 1))
}

// TestBudget_SlotRelease_StreamPaths：非流式读失败 / stream 提前 Close / 取消 / 错误
// 四条路径均最终释放 slot，且释放后可被下个请求复用。
func TestBudget_SlotRelease_StreamPaths(t *testing.T) {
	ctx := context.Background()
	paths := []struct {
		name     string
		finalize func(*AttemptLease)
	}{
		{"nonstream-read-failure", func(l *AttemptLease) { l.Abort(errors.New("read failure")) }},
		{"stream-early-close", func(l *AttemptLease) { l.Close() }},
		{"stream-cancel", func(l *AttemptLease) { l.Abort(context.Canceled) }},
		{"stream-error-unknown-usage", func(l *AttemptLease) { l.Finish(nil) }},
	}
	for _, p := range paths {
		p := p
		t.Run(p.name, func(t *testing.T) {
			pb := NewProcessBudget(0, 0, 0, 1)
			rb, _ := pb.NewRequestBudget(ctx, WithMaxConcurrency(1))
			l, err := rb.Reserve(est(1, 1))
			if err != nil {
				t.Fatalf("reserve: %v", err)
			}
			if cerr := l.Commit(); cerr != nil {
				t.Fatalf("commit: %v", cerr)
			}
			if s := rb.Snapshot(); s.InFlight != 1 {
				t.Fatalf("slot must be held before finalize: %+v", s)
			}
			p.finalize(l)
			if s := rb.Snapshot(); s.InFlight != 0 {
				t.Fatalf("slot must be released on path %s: %+v", p.name, s)
			}
			// 释放的 slot 可被下个请求复用
			l2, err := rb.Reserve(est(1, 1))
			if err != nil {
				t.Fatalf("freed slot must be reusable: %v", err)
			}
			_ = l2.Commit()
			l2.Close()
		})
	}
}

// TestBudget_ConcurrentFinalize：Close 与 EOF(Finish) 并发只 finalize 一次。
func TestBudget_ConcurrentFinalize(t *testing.T) {
	ctx := context.Background()
	for iter := 0; iter < 64; iter++ {
		pb := NewProcessBudget(0, 0, 0, 0)
		rb, _ := pb.NewRequestBudget(ctx)
		l, err := rb.Reserve(est(10, 10))
		if err != nil {
			t.Fatalf("iter %d reserve: %v", iter, err)
		}
		if cerr := l.Commit(); cerr != nil {
			t.Fatalf("iter %d commit: %v", iter, cerr)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; l.Close() }()             // 提前 Close
		go func() { defer wg.Done(); <-start; l.Finish(usage(3, 4)) }() // EOF 结算
		close(start)
		waitDone(t, &wg, 3*time.Second)

		s := rb.Snapshot()
		if s.InFlight != 0 {
			t.Fatalf("iter %d: inFlight=%d, want 0 (finalize exactly once)", iter, s.InFlight)
		}
		// 只结算一次：要么 Close 的保守值(10/10)，要么 Finish 的实际值(3/4)，绝不叠加
		conservative := s.InputUsed == 10 && s.OutputUsed == 10
		actual := s.InputUsed == 3 && s.OutputUsed == 4
		if !conservative && !actual {
			t.Fatalf("iter %d: exactly one settlement expected, got in=%d out=%d", iter, s.InputUsed, s.OutputUsed)
		}
		if s.CommittedAttempts != 1 {
			t.Fatalf("iter %d: committed=%d, want 1", iter, s.CommittedAttempts)
		}
	}
}

// TestBudget_UnknownUsage：unknown usage 不退款 token（保守保留），
// 但下一请求能用释放的 slot。
func TestBudget_UnknownUsage(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(0, 0, 0, 1) // concurrency=1，凸显 slot 释放
	rb, _ := pb.NewRequestBudget(ctx, WithMaxConcurrency(1))

	l, err := rb.Reserve(est(10, 20))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if cerr := l.Commit(); cerr != nil {
		t.Fatalf("commit: %v", cerr)
	}
	l.Finish(nil) // usage 缺失 → unknown

	s := rb.Snapshot()
	if s.InputUsed != 10 || s.OutputUsed != 20 {
		t.Fatalf("unknown usage must conservatively retain reservation: %+v", s)
	}
	if s.InputReserved != 0 || s.OutputReserved != 0 {
		t.Fatalf("reservation must move to used (not refunded): %+v", s)
	}
	if s.InFlight != 0 {
		t.Fatalf("slot must be released despite unknown usage: %+v", s)
	}
	// 下一请求能用释放的 slot
	l2, err := rb.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("freed slot must be reusable: %v", err)
	}
	_ = l2.Commit()
	l2.Finish(usage(1, 1))
}

// TestBudget_TransferToBackground：Index 同步 + 后台共享同一 TaskBudget，
// 显式移交后累计额度不刷新，同步侧句柄失效。
func TestBudget_TransferToBackground(t *testing.T) {
	ctx := context.Background()
	pb := NewProcessBudget(0, 0, 0, 0)
	tb, err := pb.NewTaskBudget(ctx, WithMaxRequests(5))
	if err != nil {
		t.Fatalf("NewTaskBudget: %v", err)
	}

	// 同步阶段一次尝试
	l1, err := tb.Reserve(est(1, 1))
	if err != nil {
		t.Fatalf("sync reserve: %v", err)
	}
	if cerr := l1.Commit(); cerr != nil {
		t.Fatalf("sync commit: %v", cerr)
	}
	l1.Finish(usage(1, 1))

	// 显式移交后台
	bg, err := tb.TransferToBackground()
	if err != nil {
		t.Fatalf("TransferToBackground: %v", err)
	}
	// 同步侧句柄失效
	if _, err := tb.Reserve(est(1, 1)); !errors.Is(err, ErrBudgetTransferred) {
		t.Fatalf("sync side must be deactivated, got %v", err)
	}
	// 重复移交被拒
	if _, err := tb.TransferToBackground(); !errors.Is(err, ErrBudgetTransferred) {
		t.Fatalf("double transfer must be rejected, got %v", err)
	}

	// 后台在【同一累计】上继续（token 累计 1→3）
	l2, err := bg.Reserve(est(2, 2))
	if err != nil {
		t.Fatalf("background reserve: %v", err)
	}
	if cerr := l2.Commit(); cerr != nil {
		t.Fatalf("background commit: %v", cerr)
	}
	l2.Finish(usage(2, 2))

	ts := bg.Snapshot()
	if ts.CommittedAttempts != 2 {
		t.Fatalf("shared cumulative committed=2: %+v", ts)
	}
	if ts.InputUsed != 3 {
		t.Fatalf("cumulative input tokens 1+2=3: %+v", ts)
	}
	// 进程级同样计入两次尝试
	ps := pb.Snapshot()
	if ps.CommittedAttempts != 2 {
		t.Fatalf("process must also count both attempts: %+v", ps)
	}
}

// waitDone 在超时前等待 wg，超时则判定失败（避免测试挂死，同时不依赖固定 sleep）。
func waitDone(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for goroutines")
	}
}
