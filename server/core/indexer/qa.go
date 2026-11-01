package indexer

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/common"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"golang.org/x/sync/errgroup"
)

// qa 使用 errgroup 有限并发地为文档生成 QA 内容。
// qaModelFactory: 构建时固定的模型获取函数；maxConcurrent: 单次调用并发上限。
// 输出为独立 Document 切片，不修改输入。
func qa(ctx context.Context, docs []*schema.Document, qaModelFactory func(context.Context) (model.BaseChatModel, error), maxConcurrent int) (output []*schema.Document, err error) {
	// 1. 提取 knowledgeName
	var knowledgeName string
	if value, ok := ctx.Value(coretypes.KnowledgeName).(string); ok {
		knowledgeName = value
	} else {
		return nil, fmt.Errorf("必须提供知识库名称")
	}

	// 2. 空 docs 快速返回
	if len(docs) == 0 {
		return docs, nil
	}

	// 3. nil 检查 + 去重
	seen := make(map[*schema.Document]struct{}, len(docs))
	for _, d := range docs {
		if d == nil {
			return nil, fmt.Errorf("qa: nil document in input")
		}
		if _, dup := seen[d]; dup {
			return nil, fmt.Errorf("qa: duplicate document pointer detected")
		}
		seen[d] = struct{}{}
	}

	// 3.5 预取消检查：factory 和 Generate 均不得调用
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 3.6 nil factory 检查
	if qaModelFactory == nil {
		return nil, fmt.Errorf("qa: nil model factory")
	}

	// 4. 在入口调用一次 factory 获取 model
	cm, modelErr := qaModelFactory(ctx)
	if modelErr != nil {
		return nil, fmt.Errorf("qa: get model: %w", modelErr)
	}
	if cm == nil {
		return nil, fmt.Errorf("qa: nil model from factory")
	}

	// 5. 创建输出 Document 数组（独立 MetaData，不修改输入）
	output = make([]*schema.Document, len(docs))
	for i, d := range docs {
		outMeta := make(map[string]any, len(d.MetaData))
		// 复制顶层 key-value（不递归复制嵌套值）
		for k, v := range d.MetaData {
			outMeta[k] = v
		}
		output[i] = &schema.Document{
			ID:       d.ID,
			Content:  d.Content,
			MetaData: outMeta,
		}
	}

	// 6. errgroup 有限并发
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxConcurrent)

	// 7. results 数组（防御性，Wait 后统一写回）
	results := make([]string, len(docs))

	for i, d := range docs {
		i, d := i, d // capture

		// 提交前检查取消
		if egCtx.Err() != nil {
			break
		}

		eg.Go(func() error {
			// 任务入口检查取消
			if egCtx.Err() != nil {
				return egCtx.Err()
			}

			// 已有 QA 内容跳过（检查输入 doc）
			if d.MetaData != nil {
				if existing, ok := d.MetaData[coretypes.FieldQAContent].(string); ok && existing != "" {
					results[i] = existing
					return nil
				}
			}

			// 模型调用前检查取消
			if egCtx.Err() != nil {
				return egCtx.Err()
			}

			// 生成 QA 内容
			qaContent, genErr := getQAContent(egCtx, d, knowledgeName, cm)
			if genErr != nil {
				return fmt.Errorf("qa: doc %s: %w", d.ID, genErr)
			}
			results[i] = qaContent
			return nil
		})
	}

	// 8. 等待所有已启动任务退出
	waitErr := eg.Wait()

	// 9. 先返回 worker 错误
	if waitErr != nil {
		return nil, waitErr
	}

	// 10. 再检查原始调用方 ctx 是否已取消
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 11. 统一写回输出 Document（不修改输入）
	for i := range docs {
		if results[i] != "" {
			output[i].MetaData[coretypes.FieldQAContent] = results[i]
		}
	}

	return output, nil
}

// getQAContent 使用给定的模型生成文档的 QA 内容。
func getQAContent(ctx context.Context, doc *schema.Document, knowledgeName string, cm model.BaseChatModel) (qaContent string, err error) {
	generate, err := cm.Generate(ctx, []*schema.Message{
		{
			Role: schema.System,
			Content: fmt.Sprintf("你是一个专业的问题生成助手，任务是从给定的文本中提取或生成可能的问题。你不需要回答这些问题，只需生成问题本身。\n"+
				"知识库名字是：《%s》\n\n"+
				"输出格式：\n"+
				"- 每个问题占一行\n"+
				"- 问题必须以问号结尾\n"+
				"- 避免重复或语义相似的问题\n\n"+
				"生成规则：\n"+
				"- 生成的问题必须严格基于文本内容，不能脱离文本虚构。\n"+
				"- 优先生成事实性问题（如谁、何时、何地、如何）。\n"+
				"- 对于复杂文本，可生成多层次问题（基础事实 + 推理问题）。\n"+
				"- 禁止生成主观或开放式问题（如'你认为...？'）。"+
				"- 数量控制在3-5个", knowledgeName),
		},
		{
			Role:    schema.User,
			Content: doc.Content,
		},
	})
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	if generate == nil {
		return "", fmt.Errorf("generate returned nil response")
	}
	return generate.Content, nil
}

// defaultQAModelFactory 默认的 QA 模型工厂函数，调用 common.GetQAModel。
func defaultQAModelFactory(ctx context.Context) (model.BaseChatModel, error) {
	return common.GetQAModel(ctx, nil)
}
