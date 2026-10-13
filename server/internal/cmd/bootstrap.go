package cmd

import (
	"errors"
	"fmt"
)

// InitStep 定义一个初始化步骤，包含名称、执行函数和清理函数。
// 用于 RunInitSteps 协调多步初始化和失败时的级联清理。
type InitStep struct {
	Name    string
	Fn      func() error
	Cleanup func() error
}

// RunInitSteps 按顺序执行初始化步骤。
// 如果某步失败，逆序清理已完成的步骤，返回包含原始错误和清理错误的汇总。
func RunInitSteps(steps []InitStep) error {
	var completed []InitStep

	for _, step := range steps {
		if err := step.Fn(); err != nil {
			// 逆序清理已完成的步骤
			var cleanupErrors []error
			for i := len(completed) - 1; i >= 0; i-- {
				if cleanErr := completed[i].Cleanup(); cleanErr != nil {
					cleanupErrors = append(cleanupErrors, cleanErr)
				}
			}

			// 保留错误链：原始错误 + 清理错误
			allErrors := append([]error{fmt.Errorf("stage[%s] failed: %w", step.Name, err)}, cleanupErrors...)
			return errors.Join(allErrors...)
		}
		completed = append(completed, step)
	}

	return nil
}
