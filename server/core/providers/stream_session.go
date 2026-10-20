// Package providers 提供 StreamSession —— 受控流会话生命周期句柄。
//
// StreamSession 封装流式调用的完整生命周期：reader、derivedCtx 取消、转发 goroutine
// 退出信号与 lease 终结。Close 操作按确定性顺序执行：cancel → reader.Close →
// 等待 goroutine → lease 由 goroutine defer 终结。
//
// 设计原则：
//   - 不使用 runtime.SetFinalizer（正确性不依赖 GC）
//   - 不使用 lease==nil 抢读 goroutine（无预算与有预算走同一 Pipe 转发路径）
//   - Close 幂等（sync.Once）
//   - Drain 用于失败路径：先 cancel 中断源，再关闭 reader 传播错误
package providers

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// StreamSession 是 Provider 层返回的受控流生命周期句柄。
//
// 会话提供 reader 供消费者读取数据，Close/Drain 提供确定性清理。
// Close 链：cancel derivedCtx → 关闭 reader → 等待转发 goroutine 退出 → lease 终结。
// Drain 用于失败路径：先 cancel 中断底层 HTTP body read，再关闭 reader。
type StreamSession struct {
	reader    *schema.StreamReader[*schema.Message]
	cancel    context.CancelFunc
	done      chan struct{}
	lease     *AttemptLease // 可为 nil
	closeOnce sync.Once
}

// NewStreamSession 构造受控流会话。
//
// reader: Pipe reader 供消费者读取。
// cancel: derivedCtx 的取消函数。
// done: 转发 goroutine 退出时关闭的 channel。
// lease: 可为 nil；由转发 goroutine 的 defer 终结。
func NewStreamSession(
	reader *schema.StreamReader[*schema.Message],
	cancel context.CancelFunc,
	done chan struct{},
	lease *AttemptLease,
) *StreamSession {
	return &StreamSession{
		reader: reader,
		cancel: cancel,
		done:   done,
		lease:  lease,
	}
}

// Reader 返回消费者使用的流分支。
func (s *StreamSession) Reader() *schema.StreamReader[*schema.Message] {
	return s.reader
}

// Done 返回转发 goroutine 退出信号。
func (s *StreamSession) Done() <-chan struct{} {
	return s.done
}

// Close 执行确定性清理（成功路径）。
//
// 顺序：cancel derivedCtx → 关闭 reader → 等待转发 goroutine 退出。
// lease 由转发 goroutine 的 defer 终结。
// Close 幂等（sync.Once），重复调用立即返回。
func (s *StreamSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()       // 1. 取消 derivedCtx → 中断底层 HTTP body read
		s.reader.Close() // 2. 关闭 pipe reader → 转发 goroutine Send→false → 退出
		<-s.done         // 3. 等待转发 goroutine 退出（lease 由 goroutine defer 终结）
	})
	return nil
}

// Drain 用于失败路径：先 cancel 中断源，再关闭 reader 传播错误。
//
// 与 Close 的区别：Drain 先 cancel（中断底层 HTTP 请求），然后关闭 reader。
// Close 先 cancel 再关闭 reader，但语义上 Drain 强调"中断并丢弃"。
// Drain 幂等（sync.Once），与 Close 共享 once。
func (s *StreamSession) Drain() error {
	s.closeOnce.Do(func() {
		s.cancel()       // 1. 先 cancel → 中断底层 HTTP body read
		s.reader.Close() // 2. 关闭 reader → 传播错误/EOF 给消费者
		<-s.done         // 3. 等待转发 goroutine 退出
	})
	return nil
}

// StreamSessioner 是可选接口：ChatRole 实现者可额外暴露 StreamSession。
// chat.go 通过类型断言获取会话；不实现此接口的角色回退到 Copy(2) 旧路径。
type StreamSessioner interface {
	StreamSession(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*StreamSession, error)
}
