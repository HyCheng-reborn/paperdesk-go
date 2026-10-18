package providers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
)

// 预算相关错误。
var (
	// ErrBudgetExhausted 表示任一作用域（请求/任务级或进程级）额度不足，
	// 预留失败，绝不进入 sender（stage2-plan §4.4）。
	ErrBudgetExhausted = errors.New("providers: budget exhausted")
	// ErrBudgetInvalidEstimate 表示 TokenEstimate 含负数。
	ErrBudgetInvalidEstimate = errors.New("providers: token estimate must be non-negative")
	// ErrBudgetInvalidOption 表示 BudgetOption 含非法（负数）上限。
	ErrBudgetInvalidOption = errors.New("providers: budget option must be non-negative")
	// ErrLeaseFinalized 表示 lease 已终结（Finish/Abort/Close 已执行），不可再 Commit。
	ErrLeaseFinalized = errors.New("providers: attempt lease already finalized")
	// ErrBudgetTransferred 表示 TaskBudget 已显式移交后台，同步侧不得再预留。
	ErrBudgetTransferred = errors.New("providers: task budget already transferred to background")
	// ErrLeaseClosed 是 Close() 记录的哨兵错误。
	ErrLeaseClosed = errors.New("providers: attempt lease closed")
)

// budgetCounters 是单个作用域（进程级或请求/任务级）的限额与运行时计数。
// 所有字段均由所属 ProcessBudget.mu 统一保护（见下方“单锁协调”说明）。
type budgetCounters struct {
	// 限额（<=0 表示该维度不限）
	maxRequests     int64
	maxInputTokens  int64
	maxOutputTokens int64
	maxConcurrency  int64

	// 运行时计数
	attemptsUsed   int64 // 已预留 + 已承诺的尝试数（reserved 未 commit 可回退）
	committed      int64 // 已承诺尝试数（单调，实际结算账本之一）
	inFlight       int64 // 在途并发 slot
	inputReserved  int64 // 输入 token 预留账本
	outputReserved int64 // 输出 token 预留账本
	inputUsed      int64 // 输入 token 实际结算账本（单调）
	outputUsed     int64 // 输出 token 实际结算账本（单调）
}

// canReserve 在不修改状态的前提下判断能否再预留一次（1 尝试 + 1 slot + est token）。
// 使用 exceeds 辅助避免 int64 溢出。
func (c *budgetCounters) canReserve(e *TokenEstimate) bool {
	if exceeds(c.attemptsUsed, 1, c.maxRequests) {
		return false
	}
	if exceeds(c.inFlight, 1, c.maxConcurrency) {
		return false
	}
	if exceeds(c.inputUsed+c.inputReserved, e.InputTokens, c.maxInputTokens) {
		return false
	}
	if exceeds(c.outputUsed+c.outputReserved, e.MaxOutput, c.maxOutputTokens) {
		return false
	}
	return true
}

// applyReserve 施加一次预留（调用方必须已确认 canReserve 为真且持锁）。
func (c *budgetCounters) applyReserve(e *TokenEstimate) {
	c.attemptsUsed++
	c.inFlight++
	c.inputReserved += e.InputTokens
	c.outputReserved += e.MaxOutput
}

// exceeds 判断 current+add 是否超过 limit，limit<=0 视为不限，且不发生溢出。
func exceeds(current, add, limit int64) bool {
	if limit <= 0 {
		return false // 不限
	}
	if current >= limit {
		return true
	}
	return add > limit-current // limit-current>0，安全
}

// ProcessBudget 是进程级预算 owner（stage2-plan §4.3）。
// 由进程 owner 在启动时创建，Registry 构造时绑定；不可通过新建 Registry 重置。
//
// 单锁协调：mu 是唯一协调锁，保护进程级与所有子作用域（RequestBudget/TaskBudget）
// 的 budgetCounters。所有跨作用域的预留/结算都在持有 mu 时“先整体校验、后整体施加”，
// 从而以等价机制保证多作用域多资源的准入/回退原子性，无需固定多锁顺序，也不引入后台工作。
type ProcessBudget struct {
	mu sync.Mutex
	c  budgetCounters
}

// NewProcessBudget 创建进程级预算。任一上限 <=0 表示该维度不限。
func NewProcessBudget(maxRequests, maxInputTokens, maxOutputTokens int64, maxConcurrency int) *ProcessBudget {
	return &ProcessBudget{
		c: budgetCounters{
			maxRequests:     sanitize64(maxRequests),
			maxInputTokens:  sanitize64(maxInputTokens),
			maxOutputTokens: sanitize64(maxOutputTokens),
			maxConcurrency:  sanitize64(int64(maxConcurrency)),
		},
	}
}

func sanitize64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// BudgetSnapshot 是作用域计数的只读快照，供测试与观测使用。
type BudgetSnapshot struct {
	AttemptsUsed      int64 // 已预留 + 已承诺尝试数
	CommittedAttempts int64 // 已承诺尝试数（单调）
	InFlight          int64 // 在途并发 slot
	InputReserved     int64 // 输入 token 预留
	OutputReserved    int64 // 输出 token 预留
	InputUsed         int64 // 输入 token 实际结算（单调）
	OutputUsed        int64 // 输出 token 实际结算（单调）
}

func snapshotOf(c *budgetCounters) BudgetSnapshot {
	return BudgetSnapshot{
		AttemptsUsed:      c.attemptsUsed,
		CommittedAttempts: c.committed,
		InFlight:          c.inFlight,
		InputReserved:     c.inputReserved,
		OutputReserved:    c.outputReserved,
		InputUsed:         c.inputUsed,
		OutputUsed:        c.outputUsed,
	}
}

// Snapshot 返回进程级作用域的只读快照。
func (pb *ProcessBudget) Snapshot() BudgetSnapshot {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return snapshotOf(&pb.c)
}

// scope 是请求/任务级作用域的共享状态（指针语义，便于 TaskBudget 显式移交后共享累计）。
type scope struct {
	pb *ProcessBudget
	c  budgetCounters
}

// reserve 执行双作用域整体原子预留：进程级 + 子作用域。
// 任一不足返回 ErrBudgetExhausted 且不施加任何变更（无需回退，因为先校验后施加）。
func (s *scope) reserve(ctx context.Context, est *TokenEstimate) (*AttemptLease, error) {
	if err := ctx.Err(); err != nil { // 取消传播
		return nil, err
	}
	e, err := normalizeEstimate(est)
	if err != nil {
		return nil, err
	}
	pb := s.pb
	pb.mu.Lock()
	defer pb.mu.Unlock()

	// 单锁下整体校验两个作用域：任一不足即失败，均未施加 → 完全回退（零残留）。
	if !pb.c.canReserve(e) || !s.c.canReserve(e) {
		return nil, ErrBudgetExhausted
	}
	pb.c.applyReserve(e)
	s.c.applyReserve(e)

	return &AttemptLease{
		pb:    pb,
		child: &s.c,
		in:    e.InputTokens,
		out:   e.MaxOutput,
	}, nil
}

func (s *scope) snapshot() BudgetSnapshot {
	s.pb.mu.Lock()
	defer s.pb.mu.Unlock()
	return snapshotOf(&s.c)
}

// TokenEstimate 是单次尝试的 token 粗估（stage2-plan §4.2）。
// 明确为估算，不冒充精确金额；实际消耗以 Provider 返回的 usage 为准。
type TokenEstimate struct {
	InputTokens int64 // 输入 token 估算
	MaxOutput   int64 // 最大输出 token
}

func normalizeEstimate(est *TokenEstimate) (*TokenEstimate, error) {
	if est == nil {
		return &TokenEstimate{}, nil
	}
	if est.InputTokens < 0 || est.MaxOutput < 0 {
		return nil, ErrBudgetInvalidEstimate
	}
	e := *est
	return &e, nil
}

// ---------------------------------------------------------------------------
// RequestBudget
// ---------------------------------------------------------------------------

// RequestBudget 是请求级累计 owner（覆盖整个 Retrieve/Index/ChatStream 调用）。
// 绑定 ProcessBudget，独立累计，同时向进程级原子预扣。
type RequestBudget struct {
	s        *scope
	deadline time.Duration
}

// NewRequestBudget 创建请求级预算；opts 设定子作用域上限（<=0 表示继承为不限）。
// 负数上限返回 ErrBudgetInvalidOption。
func (pb *ProcessBudget) NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error) {
	cfg, err := newBudgetConfig(opts)
	if err != nil {
		return nil, err
	}
	_ = ctx // ctx 保留用于取消传播/追踪；构造本身不发起网络请求
	return &RequestBudget{
		s: &scope{
			pb: pb,
			c: budgetCounters{
				maxRequests:     cfg.maxRequests,
				maxInputTokens:  cfg.maxInputTokens,
				maxOutputTokens: cfg.maxOutputTokens,
				maxConcurrency:  cfg.maxConcurrency,
			},
		},
		deadline: cfg.deadline,
	}, nil
}

// Reserve 在发送前原子预留一次尝试（slot + token + 尝试计数），返回短期句柄 AttemptLease。
func (rb *RequestBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error) {
	return rb.s.reserve(context.Background(), estimate)
}

// ReserveCtx 与 Reserve 相同，但使用调用方 ctx 支持取消传播。
func (rb *RequestBudget) ReserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	return rb.s.reserve(ctx, estimate)
}

// Deadline 返回请求级 deadline（可能为 0，表示未设置）。
func (rb *RequestBudget) Deadline() time.Duration { return rb.deadline }

// ContextWithDeadline 在设置了 deadline 时返回带超时的派生 ctx，否则返回原 ctx。
func (rb *RequestBudget) ContextWithDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return withDeadline(ctx, rb.deadline)
}

// Snapshot 返回请求级作用域的只读快照。
func (rb *RequestBudget) Snapshot() BudgetSnapshot { return rb.s.snapshot() }

// ---------------------------------------------------------------------------
// TaskBudget
// ---------------------------------------------------------------------------

// TaskBudget 是任务级累计 owner，供 Index 同步 + 后台共享（stage2-plan §4.9）。
// 通过 TransferToBackground 显式移交，而非 ctx 继承；移交后共享同一累计额度（不刷新）。
type TaskBudget struct {
	s           *scope
	deadline    time.Duration
	transferred bool // 由 s.pb.mu 保护
}

// NewTaskBudget 创建任务级预算。
func (pb *ProcessBudget) NewTaskBudget(ctx context.Context, opts ...BudgetOption) (*TaskBudget, error) {
	cfg, err := newBudgetConfig(opts)
	if err != nil {
		return nil, err
	}
	_ = ctx
	return &TaskBudget{
		s: &scope{
			pb: pb,
			c: budgetCounters{
				maxRequests:     cfg.maxRequests,
				maxInputTokens:  cfg.maxInputTokens,
				maxOutputTokens: cfg.maxOutputTokens,
				maxConcurrency:  cfg.maxConcurrency,
			},
		},
		deadline: cfg.deadline,
	}, nil
}

// Reserve 在发送前原子预留一次尝试。
func (tb *TaskBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error) {
	return tb.reserveCtx(context.Background(), estimate)
}

// ReserveCtx 与 Reserve 相同，但使用调用方 ctx 支持取消传播。
func (tb *TaskBudget) ReserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	return tb.reserveCtx(ctx, estimate)
}

func (tb *TaskBudget) reserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	// 移交后同步侧句柄失效；共享 scope 的累计额度由后台句柄继续使用。
	tb.s.pb.mu.Lock()
	transferred := tb.transferred
	tb.s.pb.mu.Unlock()
	if transferred {
		return nil, ErrBudgetTransferred
	}
	return tb.s.reserve(ctx, estimate)
}

// TransferToBackground 显式移交：返回共享同一累计 scope 的新句柄，后台持有它继续操作。
// 移交后原句柄的 Reserve 返回 ErrBudgetTransferred（同步侧不再持有活跃引用）。
// 进程级 ProcessBudget 仍约束后台任务的累计消耗。
func (tb *TaskBudget) TransferToBackground() (*TaskBudget, error) {
	tb.s.pb.mu.Lock()
	defer tb.s.pb.mu.Unlock()
	if tb.transferred {
		return nil, ErrBudgetTransferred
	}
	tb.transferred = true
	return &TaskBudget{
		s:        tb.s, // 共享 scope → 累计额度不刷新
		deadline: tb.deadline,
	}, nil
}

// Deadline 返回任务级 deadline。
func (tb *TaskBudget) Deadline() time.Duration { return tb.deadline }

// ContextWithDeadline 在设置了 deadline 时返回带超时的派生 ctx。
func (tb *TaskBudget) ContextWithDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return withDeadline(ctx, tb.deadline)
}

// Snapshot 返回任务级作用域的只读快照。
func (tb *TaskBudget) Snapshot() BudgetSnapshot { return tb.s.snapshot() }

func withDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// ---------------------------------------------------------------------------
// BudgetOption
// ---------------------------------------------------------------------------

type budgetConfig struct {
	maxRequests     int64
	maxInputTokens  int64
	maxOutputTokens int64
	maxConcurrency  int64
	deadline        time.Duration
}

func newBudgetConfig(opts []BudgetOption) (*budgetConfig, error) {
	cfg := &budgetConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.maxRequests < 0 || cfg.maxInputTokens < 0 || cfg.maxOutputTokens < 0 || cfg.maxConcurrency < 0 {
		return nil, ErrBudgetInvalidOption
	}
	if cfg.deadline < 0 {
		return nil, ErrBudgetInvalidOption
	}
	return cfg, nil
}

// BudgetOption 配置请求/任务级子作用域上限；<=0 表示该维度不限（仍受进程级约束）。
type BudgetOption func(*budgetConfig)

// WithMaxRequests 设定最大发送尝试数（含重试）。
func WithMaxRequests(n int64) BudgetOption { return func(c *budgetConfig) { c.maxRequests = n } }

// WithMaxInputTokens 设定输入 token 预留上限。
func WithMaxInputTokens(n int64) BudgetOption { return func(c *budgetConfig) { c.maxInputTokens = n } }

// WithMaxOutputTokens 设定输出 token 预留上限。
func WithMaxOutputTokens(n int64) BudgetOption {
	return func(c *budgetConfig) { c.maxOutputTokens = n }
}

// WithMaxConcurrency 设定在途并发 slot 上限。
func WithMaxConcurrency(n int) BudgetOption {
	return func(c *budgetConfig) { c.maxConcurrency = int64(n) }
}

// WithDeadline 设定请求/任务级 deadline。
func WithDeadline(d time.Duration) BudgetOption { return func(c *budgetConfig) { c.deadline = d } }

// ---------------------------------------------------------------------------
// AttemptLease
// ---------------------------------------------------------------------------

// AttemptLease 是单次 HTTP 尝试的短期句柄（stage2-plan §4.3/§4.6）。
// 生命周期：Reserve → Commit → Finish/Abort/Close。
// Finish/Abort/Close 通过 sync.Once 共享同一终结路径，幂等且并发安全；
// slot 释放与 token 结算在终结时各执行一次，二者解耦（保留 token 不泄漏 slot）。
type AttemptLease struct {
	pb    *ProcessBudget
	child *budgetCounters // 子作用域计数（与 pb.c 一同在 pb.mu 下结算）

	in  int64 // 本次预留的输入 token
	out int64 // 本次预留的输出 token

	once      sync.Once
	committed bool  // 由 pb.mu 保护
	finalized bool  // 由 pb.mu 保护
	abortErr  error // 由 pb.mu 保护
}

// Commit 执行 reserved → committed 原子承诺；承诺后不可撤销。
// 幂等：重复 Commit 返回 nil；若已终结返回 ErrLeaseFinalized。
func (al *AttemptLease) Commit() error {
	al.pb.mu.Lock()
	defer al.pb.mu.Unlock()
	if al.finalized {
		return ErrLeaseFinalized
	}
	if al.committed {
		return nil // 幂等
	}
	al.committed = true
	// 承诺计入单调结算账本（attemptsUsed 已在 Reserve 时递增，此处不重复）
	al.pb.c.committed++
	al.child.committed++
	return nil
}

// Finish 以真实 usage 一次性结算（幂等）。usage 为 nil 或全零视为 unknown，保守保留预留。
func (al *AttemptLease) Finish(usage *model.TokenUsage) {
	al.once.Do(func() { al.finalize(usage, false, nil) })
}

// Abort 取消尝试（幂等）。未 commit 时全额回退；已 commit 时保守保留 token 预留。
func (al *AttemptLease) Abort(err error) {
	al.once.Do(func() { al.finalize(nil, true, err) })
}

// Close 等价于 Abort（幂等），记录 ErrLeaseClosed。
func (al *AttemptLease) Close() {
	al.once.Do(func() { al.finalize(nil, true, ErrLeaseClosed) })
}

// finalize 是唯一终结路径，由 once 保证至多执行一次。持 pb.mu 结算两个作用域。
func (al *AttemptLease) finalize(usage *model.TokenUsage, aborted bool, abortErr error) {
	al.pb.mu.Lock()
	defer al.pb.mu.Unlock()

	al.finalized = true
	if abortErr != nil {
		al.abortErr = abortErr
	}

	// usage 缺失/全零视为 unknown（不把 unknown 当 0）
	known := !aborted && usageKnown(usage)

	for _, c := range []*budgetCounters{&al.pb.c, al.child} {
		if !al.committed {
			// 尚未发送的失败：全额回退尝试预留 + token 预留（不消耗计数）
			c.attemptsUsed--
			c.inputReserved -= al.in
			c.outputReserved -= al.out
		} else {
			// 已 commit：尝试计数单调消耗（不退）；token 结算
			c.inputReserved -= al.in
			c.outputReserved -= al.out
			if known {
				c.inputUsed += int64(usage.PromptTokens)
				c.outputUsed += int64(usage.CompletionTokens)
			} else {
				// 保守保留：把预留量记为实际消耗（网络失败/取消/unknown usage）
				c.inputUsed += al.in
				c.outputUsed += al.out
			}
		}
		// slot 释放：与 token 结算解耦，任何终结路径都释放且仅一次
		c.inFlight--
	}
}

func usageKnown(u *model.TokenUsage) bool {
	return u != nil && (u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0)
}

// Committed 报告 lease 是否已承诺。
func (al *AttemptLease) Committed() bool {
	al.pb.mu.Lock()
	defer al.pb.mu.Unlock()
	return al.committed
}

// Finalized 报告 lease 是否已终结。
func (al *AttemptLease) Finalized() bool {
	al.pb.mu.Lock()
	defer al.pb.mu.Unlock()
	return al.finalized
}

// Err 返回 Abort/Close 记录的错误（若有）。
func (al *AttemptLease) Err() error {
	al.pb.mu.Lock()
	defer al.pb.mu.Unlock()
	return al.abortErr
}
