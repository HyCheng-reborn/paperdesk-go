package probe

import (
	"fmt"

	"github.com/wangle201210/go-rag/server/core/rerank"
)

// Preflight 是一次性完整预检：计划 + 端点策略 + 角色身份 + **全部**步骤。
//
// 为什么必须一次性：若入口只校验"步骤切片非空"并逐步校验，第一步会在发现
// 第二步非法之前就已真实发送（复核 P2：合法 Chat + 缺文本 Embedding 时，
// Ready() 为 false 但 RunOnline 仍发出一次请求）。因此执行入口在构造服务与
// 第一次发送之前先跑 Preflight，任何一步无效都不产生任何请求。
//
// 角色身份（ValidateRoles）也在这里：带 "@version" 的模型身份无法在请求中
// 表达（providers.ErrModelVersionNotExpressible），属于可在预检期确定的非法配置；
// 缺了它就会"先发上一步、再拒绝后一步"。
//
// Readiness 的 "plan" / "steps" 项与 Run / RunOnline 的入口检查都调用本文件
// 里的同一组函数，保证"待配置入口"与"实际执行入口"不会给出相反结论。
// RunStep 仍保留运行期校验（防绕过与防御性检查），但不再承担全量预检职责。
func Preflight(plan Plan, steps []Step) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := CheckEndpoints(plan); err != nil {
		return err
	}
	if err := ValidateRoles(plan); err != nil {
		return err
	}
	return ValidateSteps(plan, steps)
}

// ValidateSteps 校验本次声明的最小调用矩阵：空矩阵是"未声明"，不静默采用默认值。
// 错误保留 %w 链，调用方可 errors.Is 识别 ErrEmptyStep / ErrNotConfigured。
func ValidateSteps(plan Plan, steps []Step) error {
	if len(steps) == 0 {
		return fmt.Errorf("%w: no probe step is declared", ErrNotConfigured)
	}
	for i, step := range steps {
		if err := step.validate(); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
		if err := step.validateAgainst(plan); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
	}
	return nil
}

// validateAgainst 校验步骤与计划参数的一致性（跨字段边界，不是单步骤自洽性）。
//
// Rerank 分支复用生产入口 (*rerank.Rerank).Rerank 的纯输入校验（rerank.ValidateInput）：
// 空文档、nil 文档、非法 top_k 都是**本地可判定**的错误，必须在第一次模型发送前
// 拒绝（复核 P2：合法 Chat + 后续 nil 文档会在 Chat 发送后才被生产 Rerank 拒绝）。
// 额外保留探针自己的 top_k <= 文档数约束：它决定请求体是否自相矛盾。
func (s Step) validateAgainst(plan Plan) error {
	if s.Kind != StepRerank {
		return nil
	}
	docs := s.Docs
	if len(docs) == 0 {
		docs = ProbeDocuments()
	}
	// MUTATION（仅证据用）：跳过生产 Rerank 输入校验，复现“nil 文档通过预检”的旧行为。
	_ = rerank.ErrInvalidInput
	if s.TopK > len(docs) {
		return fmt.Errorf("%w: rerank top_k %d exceeds %d documents", ErrEmptyStep, s.TopK, len(docs))
	}
	return nil
}
