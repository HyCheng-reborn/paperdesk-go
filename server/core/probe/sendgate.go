package probe

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 本文件实现"停止标记之后不再开始新的发送尝试"的**发送闸门准入**
// （send admission）。
//
// 复核第 3 轮 P1 指出：把"通过闸门"当成"已发送/对端已命中"是过强表述。
// 因此这里把四个状态分开，只有第 2 个是本包可观测、可断言的线性化点：
//
//	1. 入口已准入（entry admitted）：beginAttempt 在 c.mu 写侧登记了在途句柄；
//	   预算预留、fake ES guard、请求编码都可能还没发生，也没有任何字节发出。
//	2. 已通过发送闸门（gate admitted）：commitSend 在 c.mu 读侧观察到
//	   stopped=false 并返回；它与 stop 的写侧发布构成全序。**这是线性化点**。
//	3. 已交给底层 transport（transport handoff）：本文件调用 g.inner.RoundTrip。
//	   它发生在第 2 步之后、且两步之间没有任何锁（不能持锁跨越整个请求：
//	   那会让 stop 无界等待）。因此停止标记可能插在 2 与 3 之间。
//	4. 对端已收到（peer receipt）：只有对端（本机 fake 服务/代理）的计数能观测，
//	   不属于生产可观测事实，也不是本包的承诺。
//
// 本批**能保证**：停止标记之后不再有新的闸门准入（因而不产生新的重试），
// 第 1 类尝试一律返回 ErrAlreadyStopped 且零字节发出。
// 本批**不能保证**原始表述的字面含义"停止标记之后零新发送"：一个已通过闸门、
// 尚未交给底层 transport 的请求，仍可能在标记之后开始传输。该残余窗口
// **未闭合**（独立复核第 3 轮用可控同步点 5/5 复现），需用户决定是否接受较弱
// 语义或采用更强设计（见 design/stage2d-plan.md §9.1）；已交给 transport 的
// 请求不可撤销，只能取消 ctx 让传输层尽快放弃。
//
// 为什么闸门必须在传输层而不是只在入口：入口准入之后还要经过预算预留、guard、
// 请求编码等步骤，任何阻塞在它们之间的步骤都可能横跨停止标记。只有紧贴
// RoundTrip 的位置才是发送闸门准入。

// sendTrace 记录**一次尝试**在传输层的可观测事件（由请求 ctx 携带，见 withSendTrace）。
// 它是证据字段的依据：预算承诺 ≠ 通过闸门 ≠ 交给 transport ≠ 对端收到。
type sendTrace struct {
	gateAdmissions atomic.Int64
	transportCalls atomic.Int64
}

type sendTraceKey struct{}

// withSendTrace 把本次尝试的传输层观测点挂到 ctx 上（未挂载时闸门照常工作，
// 例如直接调用闸门的边界测试）。
func withSendTrace(ctx context.Context, trace *sendTrace) context.Context {
	if ctx == nil || trace == nil {
		return ctx
	}
	return context.WithValue(ctx, sendTraceKey{}, trace)
}

// sendTraceFrom 取出本次尝试的观测点；未挂载时返回 nil。
func sendTraceFrom(ctx context.Context) *sendTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(sendTraceKey{}).(*sendTrace)
	return trace
}

// sendGate 是所有模型请求共用的 RoundTripper 闸门。
//
// chat / embedding / rerank 三个角色都通过同一个 providers.Transport 的
// *http.Client 发送，因此一个闸门即可覆盖全部模型端点（fake ES 不使用该 transport）。
type sendGate struct {
	inner http.RoundTripper
	chain *Chain
	// beforeCommit 仅供包内测试在并发启动前设置：把一次尝试确定性地固定在
	// "已准入但尚未在途"的位置，不依赖调度时序。生产路径为 nil。
	beforeCommit func()
}

// RoundTrip 在真正发送之前完成闸门准入：请求要么在停止标记之前通过闸门，
// 要么被拒绝（返回 ErrAlreadyStopped，不发起任何网络 I/O）。
//
// 通过闸门之后到 inner.RoundTrip 之间**没有锁**：这段窗口里的停止标记不会
// 撤销该请求（见文件头第 3/4 状态）。这里只如实记录两个可观测事件，
// 不把"通过闸门"写成"已发送"或"对端已收到"。
func (g *sendGate) RoundTrip(req *http.Request) (*http.Response, error) {
	if hook := g.beforeCommit; hook != nil {
		hook()
	}
	trace := sendTraceFrom(req.Context())
	if err := g.chain.commitSend(); err != nil {
		return nil, err
	}
	if trace != nil {
		trace.gateAdmissions.Add(1)
		trace.transportCalls.Add(1)
	}
	return g.inner.RoundTrip(req)
}

// CloseIdleConnections 转发给内层传输，保持 Chain.Close 的清理语义。
func (g *sendGate) CloseIdleConnections() {
	if g == nil || g.inner == nil {
		return
	}
	if closer, ok := g.inner.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// newGatedTransport 在既有统一传输层之外包一层发送闸门，并返回闸门本体：
// 包内测试通过 chain.gate.beforeCommit 取得确定性同步点。
// 构造过程不发起任何网络请求；重定向禁止策略与超时仍由 providers.NewTransport 施加。
func newGatedTransport(base *providers.Transport, chain *Chain) (*providers.Transport, *sendGate) {
	if base == nil || chain == nil {
		return base, nil
	}
	client := base.Client()
	if client == nil {
		return base, nil
	}
	inner := client.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	gate := &sendGate{inner: inner, chain: chain}
	wrapped := *client
	wrapped.Transport = gate
	return providers.NewTransport(providers.WithHTTPClient(&wrapped)), gate
}

// commitSend 是"入口已准入 → 已通过发送闸门"的唯一转换点，也是停止标记的
// 观察点。只在停止标记之后拒绝；错误链保留 ErrAlreadyStopped，调用方仍可
// errors.Is。它返回后立即释放锁，因此**不能**被描述为"请求已发送"。
func (c *Chain) commitSend() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.stopped {
		return nil
	}
	summary := c.stopSummary
	if summary == "" {
		summary = "the run was stopped before this attempt was sent"
	}
	return fmt.Errorf("%w: %s", ErrAlreadyStopped, summary)
}
