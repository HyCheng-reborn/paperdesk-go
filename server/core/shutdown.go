package core

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// ShutdownCoordinator 协调 Rag Shutdown 和资源 Cleanup。
// 设计目标：
// - 先执行 Rag.Shutdown 等待后台任务收敛
// - 收敛成功后逆序执行资源 Cleanup
// - Shutdown 超时：保留依赖、不清服务引用、不报正常、返回可识别错误
// - Cleanup 错误聚合返回，避免 dao 重复关闭
type ShutdownCoordinator struct {
	rag       *Rag
	resources []io.Closer // 逆序排列：最后初始化的最先关闭
}

// NewShutdownCoordinator 创建关闭协调器。
// resources 按初始化顺序传入（先 dao 后其他），Shutdown 时逆序执行 Close。
func NewShutdownCoordinator(rag *Rag, resources ...io.Closer) *ShutdownCoordinator {
	return &ShutdownCoordinator{
		rag:       rag,
		resources: resources,
	}
}

// Shutdown 执行关闭协调：
// 1. 先执行 Rag.Shutdown 等待后台任务完成
// 2. 收敛成功后逆序执行资源 Cleanup
// 3. Shutdown 超时：跳过依赖清理，保留服务引用，返回可识别错误
// 4. Cleanup 中一个失败仍按约定清理其他资源，最终聚合返回
func (sc *ShutdownCoordinator) Shutdown(ctx context.Context) error {
	// 1. 先执行 Rag.Shutdown
	if err := sc.rag.Shutdown(ctx); err != nil {
		// Shutdown 超时：跳过依赖清理，保留服务引用，返回可识别错误
		return fmt.Errorf("rag shutdown failed: %w", err)
	}

	// 2. 收敛成功后逆序执行资源 Cleanup
	if len(sc.resources) == 0 {
		return nil
	}

	var errs []error
	for i := len(sc.resources) - 1; i >= 0; i-- {
		if sc.resources[i] != nil {
			if err := sc.resources[i].Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
