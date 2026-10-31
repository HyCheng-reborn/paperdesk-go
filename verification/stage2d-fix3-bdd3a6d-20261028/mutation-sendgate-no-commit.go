package probe

import (
	"fmt"
	"net/http"

	"github.com/wangle201210/go-rag/server/core/providers"
)

// 本文件实现"停止标记之后零新发送"的**发送准入**（send admission）线性化点。
//
// 必须区分两个状态（复核 P1）：
//   - 已准入（admitted）：beginAttempt 在 c.mu 下登记了在途句柄；此时预算预留、
//     fake ES guard、请求编码都可能还没发生，**也还没有任何字节发出**。
//     停止取消它的 ctx 后，它不会产生模型发送。
//   - 已经在途（in flight）：请求已经通过本文件的 RoundTrip 闸门。停止不能撤销
//     已交出的请求（不声称可撤销），只能取消 ctx 让传输层尽快放弃。
//
// 线性化点：stop 在 c.mu 的**写侧**发布 stopped=true；commitSend 在**读侧**检查
// stopped。Go 的 RWMutex 在写者等待时阻塞新的读者，因此两者构成全序：
//   - commitSend 先完成 → 该请求在停止标记之前已经在途；
//   - stop 先发布 → commitSend 必然看到 stopped 并返回 ErrAlreadyStopped，零字节发出。
//
// 为什么闸门必须在传输层而不是只在入口：入口准入之后还要经过预算预留、guard、
// 请求编码等步骤，任何阻塞在它们之间的步骤都可能横跨停止标记。只有紧贴
// RoundTrip 的位置才是"实际发送准入"。

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

// RoundTrip 在真正发送之前完成准入：请求要么在停止标记之前进入"在途"，
// 要么被拒绝（返回 ErrAlreadyStopped，不发起任何网络 I/O）。
func (g *sendGate) RoundTrip(req *http.Request) (*http.Response, error) {
	if hook := g.beforeCommit; hook != nil {
		hook()
	}
	// MUTATION（仅证据用）：移除发送准入，复现“停止标记后仍发送”的旧行为。
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

// commitSend 是"已准入 → 已经在途"的唯一转换点，也是停止标记的观察点。
// 只在停止标记之后拒绝；错误链保留 ErrAlreadyStopped，调用方仍可 errors.Is。
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
