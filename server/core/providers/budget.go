package providers

import (
	"context"
	"errors"
	"math"
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

	// exhausted 是 fail-closed 标志：结算溢出等账本异常后置位，
	// 该作用域永久不可再准入（canReserve 直接拒绝），绝不回绕重新获得额度。
	exhausted bool
}

// canReserve 在不修改状态的前提下判断能否再预留一次（1 尝试 + 1 slot + est token）。
//
// 关键契约（stage2 复核 R1）：applyReserve 会对 attemptsUsed/inFlight/inputReserved/
// outputReserved 做未受检累加；因此这四个量的“+本次增量”可表示性必须在此独立于
// limit 分支预先拦截——即使 limit<=0（不限），也绝不因“不限”而放行 int64 回绕，
// 否则未受检累加会把账本绕回可用区间（重新获得额度）。
// 任一中间量不可表示即整体拒绝；由于 reserve 先整体校验后整体施加，拒绝即零变更。
//
// 注：used+newReserved 的可表示性不在此预判——结算溢出由 finalize 的受检加法 fail-closed
// 负责；在此拒绝会误伤“inputUsed 已达 MaxInt64、但本次预留仍可表示”的合法场景。
func (c *budgetCounters) canReserve(e *TokenEstimate) bool {
	// fail-closed：账本异常（如结算溢出）后该作用域永久不可再准入
	if c.exhausted {
		return false
	}
	// 1. attemptsUsed + 1 可表示性（独立于 limit）
	if _, ok := addChecked(c.attemptsUsed, 1); !ok {
		return false
	}
	// 2. inFlight + 1 可表示性（独立于 limit）
	if _, ok := addChecked(c.inFlight, 1); !ok {
		return false
	}
	// 3. inputReserved + e.InputTokens 可表示性（独立于 limit）：applyReserve 直接累加此账本
	if _, ok := addChecked(c.inputReserved, e.InputTokens); !ok {
		return false
	}
	// 4. outputReserved + e.MaxOutput 可表示性（独立于 limit）
	if _, ok := addChecked(c.outputReserved, e.MaxOutput); !ok {
		return false
	}
	// 5. 有限上限检查（仅在 limit>0 时生效；沿用既有边界语义：total==limit 允许，>limit 拒绝）
	if exceeds(c.attemptsUsed, 1, c.maxRequests) {
		return false
	}
	if exceeds(c.inFlight, 1, c.maxConcurrency) {
		return false
	}
	// used+reserved 求和同样受检：溢出即拒绝，避免以回绕后的负值参与上限判定。
	inSum, ok := addChecked(c.inputUsed, c.inputReserved)
	if !ok || exceeds(inSum, e.InputTokens, c.maxInputTokens) {
		return false
	}
	outSum, ok := addChecked(c.outputUsed, c.outputReserved)
	if !ok || exceeds(outSum, e.MaxOutput, c.maxOutputTokens) {
		return false
	}
	return true
}

// applyReserve 施加一次预留（调用方必须已确认 canReserve 为真且持锁）。
// canReserve 已保证下列四项累加均可表示（不回绕），故此处直接施加是安全的。
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

// addChecked 执行受检加法：a+b 溢出 int64 时返回 (0, false)，绝不回绕。
// 用于账本累计与 used+reserved 求和，保证畸形/极端用量不会把额度绕回可用区间。
func addChecked(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
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
// 负上限是配置错误：显式返回 ErrBudgetInvalidOption，绝不 sanitize 成 0（不限）
// 而静默关闭全部预算保护（与请求/任务级 BudgetOption 的负值拒绝语义一致）。
func NewProcessBudget(maxRequests, maxInputTokens, maxOutputTokens int64, maxConcurrency int) (*ProcessBudget, error) {
	if maxRequests < 0 || maxInputTokens < 0 || maxOutputTokens < 0 || maxConcurrency < 0 {
		return nil, ErrBudgetInvalidOption
	}
	return &ProcessBudget{
		c: budgetCounters{
			maxRequests:     maxRequests,
			maxInputTokens:  maxInputTokens,
			maxOutputTokens: maxOutputTokens,
			maxConcurrency:  int64(maxConcurrency),
		},
	}, nil
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
//
// owner 约束（stage2 复核 B2）：构造期 ctx 与固定绝对 deadline 被保留，
// Reserve/ReserveCtx 不得绕过 owner 的取消/过期状态；重试与派生 context 不刷新总期限。
type RequestBudget struct {
	s             *scope
	deadline      time.Duration   // 配置值（供 Deadline() 返回）
	ownerCtx      context.Context // 构造期 ctx：取消传播，Reserve 不得绕过
	ownerDeadline time.Time       // 固定绝对 deadline（零值表示未设置）；重试/派生不刷新
}

// NewRequestBudget 创建请求级预算；opts 设定子作用域上限（<=0 表示继承为不限）。
// 负数上限返回 ErrBudgetInvalidOption。构造期 ctx 与固定 deadline 被保留用于取消传播。
func (pb *ProcessBudget) NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error) {
	cfg, err := newBudgetConfig(opts)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 固定绝对 deadline：构造期一次性确定，后续重试/派生 context 不得刷新
	var ownerDeadline time.Time
	if cfg.deadline > 0 {
		ownerDeadline = time.Now().Add(cfg.deadline)
	}
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
		deadline:      cfg.deadline,
		ownerCtx:      ctx,
		ownerDeadline: ownerDeadline,
	}, nil
}

// Reserve 在发送前原子预留一次尝试（slot + token + 尝试计数），返回短期句柄 AttemptLease。
// 先检查 owner 取消/固定 deadline 过期，绝不绕过构造期 ctx 的取消状态。
func (rb *RequestBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error) {
	return rb.reserveCtx(rb.ownerCtx, estimate)
}

// ReserveCtx 与 Reserve 相同，但额外使用调用方 ctx 支持取消传播。
// owner 取消/过期优先于调用方 ctx 检查。
func (rb *RequestBudget) ReserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	return rb.reserveCtx(ctx, estimate)
}

func (rb *RequestBudget) reserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	if err := ownerErr(rb.ownerCtx, rb.ownerDeadline); err != nil {
		return nil, err
	}
	return rb.s.reserve(ctx, estimate)
}

// Deadline 返回请求级 deadline（可能为 0，表示未设置）。
func (rb *RequestBudget) Deadline() time.Duration { return rb.deadline }

// ContextWithDeadline 返回受固定 owner deadline 约束的派生 ctx：
// 取调用方 ctx 的 deadline 与 owner 固定 deadline 的更早者，绝不刷新总期限。
func (rb *RequestBudget) ContextWithDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return contextWithFixedDeadline(ctx, rb.ownerDeadline)
}

// Snapshot 返回请求级作用域的只读快照。
func (rb *RequestBudget) Snapshot() BudgetSnapshot { return rb.s.snapshot() }

// ---------------------------------------------------------------------------
// TaskBudget
// ---------------------------------------------------------------------------

// TaskBudget 是任务级累计 owner，供 Index 同步 + 后台共享（stage2-plan §4.9）。
// 通过 TransferToBackground 显式移交，而非 ctx 继承；移交后共享同一累计额度（不刷新）
// 与同一【固定】deadline（不重新计时）。
type TaskBudget struct {
	s             *scope
	deadline      time.Duration
	ownerCtx      context.Context // 构造期 ctx：取消传播，Reserve 不得绕过
	ownerDeadline time.Time       // 固定绝对 deadline（零值表示未设置）；移交不刷新
	transferred   bool            // 由 s.pb.mu 保护
}

// NewTaskBudget 创建任务级预算。构造期 ctx 与固定 deadline 被保留用于取消传播。
func (pb *ProcessBudget) NewTaskBudget(ctx context.Context, opts ...BudgetOption) (*TaskBudget, error) {
	cfg, err := newBudgetConfig(opts)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var ownerDeadline time.Time
	if cfg.deadline > 0 {
		ownerDeadline = time.Now().Add(cfg.deadline)
	}
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
		deadline:      cfg.deadline,
		ownerCtx:      ctx,
		ownerDeadline: ownerDeadline,
	}, nil
}

// Reserve 在发送前原子预留一次尝试。先检查 owner 取消/固定 deadline 过期。
func (tb *TaskBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error) {
	return tb.reserveCtx(tb.ownerCtx, estimate)
}

// ReserveCtx 与 Reserve 相同，但额外使用调用方 ctx 支持取消传播。
func (tb *TaskBudget) ReserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	return tb.reserveCtx(ctx, estimate)
}

func (tb *TaskBudget) reserveCtx(ctx context.Context, estimate *TokenEstimate) (*AttemptLease, error) {
	// owner 取消/固定 deadline 过期优先检查，绝不绕过构造期 ctx
	if err := ownerErr(tb.ownerCtx, tb.ownerDeadline); err != nil {
		return nil, err
	}
	// 移交后同步侧句柄失效；共享 scope 的累计额度由后台句柄继续使用。
	tb.s.pb.mu.Lock()
	transferred := tb.transferred
	tb.s.pb.mu.Unlock()
	if transferred {
		return nil, ErrBudgetTransferred
	}
	return tb.s.reserve(ctx, estimate)
}

// TransferToBackground 显式移交：返回共享同一累计 scope 与【固定】deadline 的新句柄，
// 后台持有它继续操作。移交不重新计时（ownerDeadline 原样保留）；后台执行 context
// 仍按 Stage 1 服务 root/task timeout 派生，不把 HTTP 请求取消错误迁移到后台。
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
		s:             tb.s, // 共享 scope → 累计额度不刷新
		deadline:      tb.deadline,
		ownerCtx:      tb.ownerCtx,
		ownerDeadline: tb.ownerDeadline, // 固定边界，绝不重新计时
	}, nil
}

// Deadline 返回任务级 deadline。
func (tb *TaskBudget) Deadline() time.Duration { return tb.deadline }

// ContextWithDeadline 返回受固定 owner deadline 约束的派生 ctx（取更早者，绝不刷新）。
func (tb *TaskBudget) ContextWithDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return contextWithFixedDeadline(ctx, tb.ownerDeadline)
}

// Snapshot 返回任务级作用域的只读快照。
func (tb *TaskBudget) Snapshot() BudgetSnapshot { return tb.s.snapshot() }

// NewSharedRequestBudget 创建共享同一 scope 的 RequestBudget，供适配器消费。
// 返回的 RequestBudget 与 TaskBudget 共享累计额度（不刷新），用于 Index 后台等场景。
func (tb *TaskBudget) NewSharedRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error) {
	cfg, err := newBudgetConfig(opts)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var ownerDeadline time.Time
	if cfg.deadline > 0 {
		ownerDeadline = time.Now().Add(cfg.deadline)
	}
	return &RequestBudget{
		s:             tb.s, // 共享 scope → 累计额度不刷新
		deadline:      cfg.deadline,
		ownerCtx:      ctx,
		ownerDeadline: ownerDeadline,
	}, nil
}

// ownerErr 检查 owner 取消与固定 deadline 过期，返回相应错误（nil 表示可继续）。
// ownerCtx 取消→返回其 Err（Canceled/DeadlineExceeded）；固定 deadline 已过→DeadlineExceeded。
func ownerErr(ownerCtx context.Context, ownerDeadline time.Time) error {
	if ownerCtx != nil {
		if err := ownerCtx.Err(); err != nil {
			return err
		}
	}
	if !ownerDeadline.IsZero() && !time.Now().Before(ownerDeadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// contextWithFixedDeadline 以固定绝对 deadline 约束 ctx：取 ctx 自身 deadline 与
// ownerDeadline 的更早者；ownerDeadline 为零时仅返回可取消的 ctx（不施加预算期限）。
func contextWithFixedDeadline(ctx context.Context, ownerDeadline time.Time) (context.Context, context.CancelFunc) {
	if ownerDeadline.IsZero() {
		return context.WithCancel(ctx)
	}
	if dl, ok := ctx.Deadline(); ok && dl.Before(ownerDeadline) {
		return context.WithDeadline(ctx, dl)
	}
	return context.WithDeadline(ctx, ownerDeadline)
}

// ---------------------------------------------------------------------------
// Context key: RequestBudget owner reuse (Fix 2a)
// ---------------------------------------------------------------------------

// budgetCtxKey 是存储 RequestBudget 的 context key 类型（不可导出，防碰撞）。
type budgetCtxKey struct{}

// ContextWithRequestBudget 将 RequestBudget 绑定到 ctx，供下游适配器复用。
// 入口处（如 Retrieve/Index/ChatStream）构造一次，通过 ctx 传递。
func ContextWithRequestBudget(ctx context.Context, rb *RequestBudget) context.Context {
	return context.WithValue(ctx, budgetCtxKey{}, rb)
}

// RequestBudgetFromContext 从 ctx 提取已绑定的 RequestBudget（nil 表示未绑定）。
func RequestBudgetFromContext(ctx context.Context) *RequestBudget {
	if ctx == nil {
		return nil
	}
	rb, _ := ctx.Value(budgetCtxKey{}).(*RequestBudget)
	return rb
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

	// usage 缺失/全零/非法（负分项或 total 不一致）视为 unknown（不把 unknown 当 0）
	known := !aborted && usageKnown(usage)

	for _, c := range []*budgetCounters{&al.pb.c, al.child} {
		if !al.committed {
			// 尚未发送的失败：全额回退尝试预留 + token 预留（不消耗计数）
			c.attemptsUsed--
			c.inputReserved -= al.in
			c.outputReserved -= al.out
		} else {
			// 已 commit：尝试计数单调消耗（不退）；token 结算
			// 结算量：known 用真实 usage，否则保守用预留量（网络失败/取消/unknown）
			settleIn, settleOut := al.in, al.out
			if known {
				settleIn, settleOut = int64(usage.PromptTokens), int64(usage.CompletionTokens)
			}
			// 受检加法结算：溢出则 fail-closed，绝不回绕重新获得额度
			newIn, okIn := addChecked(c.inputUsed, settleIn)
			newOut, okOut := addChecked(c.outputUsed, settleOut)
			if okIn && okOut {
				c.inputUsed = newIn
				c.outputUsed = newOut
				c.inputReserved -= al.in
				c.outputReserved -= al.out
			} else {
				// 结算溢出：fail-closed —— 标记作用域不可再准入，保守保留预留（不释放），
				// 不更新 used（保持非负）。slot 仍在下方释放（与结算解耦）。
				c.exhausted = true
			}
		}
		// slot 释放：与 token 结算解耦，任何终结路径（含 fail-closed）都释放且仅一次
		c.inFlight--
	}
}

// usageKnown 严格判定 usage 是否可信结算（stage2 复核 B1）：
//   - 任一分项为负 → 不可信（保守保留预留，绝不写入负数）；
//   - TotalTokens>0 但与 Prompt+Completion 不一致（含求和溢出）→ 不可信；
//   - 全零 → unknown（不把 unknown 当 0）。
func usageKnown(u *model.TokenUsage) bool {
	if u == nil {
		return false
	}
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 {
		return false
	}
	if u.TotalTokens > 0 {
		sum, ok := addChecked(int64(u.PromptTokens), int64(u.CompletionTokens))
		if !ok || int64(u.TotalTokens) != sum {
			return false
		}
	}
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
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
