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

	// 4. errgroup 有限并发
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxConcurrent)

	// 5. results 数组（防御性，Wait 后统一写回）
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

			// 已有 QA 内容跳过
			if d.MetaData != nil {
				if existing, ok := d.MetaData[coretypes.FieldQAContent].(string); ok && existing != "" {
					results[i] = existing
					return nil
				}
			}

			// 获取 QA 模型
			cm, modelErr := qaModelFactory(egCtx)
			if modelErr != nil {
				return fmt.Errorf("qa: get model for doc %s: %w", d.ID, modelErr)
			}

			// 生成 QA 内容
			qaContent, genErr := getQAContent(egCtx, d, knowledgeName, cm)
			if genErr != nil {
				return fmt.Errorf("qa: generate for doc %s: %w", d.ID, genErr)
			}
			results[i] = qaContent
			return nil
		})
	}

	// 6. 等待所有已启动任务退出
	waitErr := eg.Wait()

	// 7. 统一写回结果
	if waitErr == nil {
		for i, d := range docs {
			if results[i] != "" {
				if d.MetaData == nil {
					d.MetaData = make(map[string]any)
				}
				d.MetaData[coretypes.FieldQAContent] = results[i]
			}
		}
		return docs, nil
	}

	return nil, waitErr
}

// getQAContent 使用给定的模型生成文档的 QA 内容。
func getQAContent(ctx context.Context, doc *schema.Document, knowledgeName string, cm model.BaseChatModel) (qaContent string, err error) {
	// 已经有数据了就不要再生成了
	if s, ok := doc.MetaData[coretypes.FieldQAContent].(string); ok && len(s) > 0 {
		return s, nil
	}

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
		return
	}
	qaContent = generate.Content
	return
}

// defaultQAModelFactory 默认的 QA 模型工厂函数，调用 common.GetQAModel。
func defaultQAModelFactory(ctx context.Context) (model.BaseChatModel, error) {
	return common.GetQAModel(ctx, nil)
}
