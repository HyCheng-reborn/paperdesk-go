package rag

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/internal/logic/chat"
)

// ChatStream 流式输出接口
func (c *ControllerV1) ChatStream(ctx context.Context, req *v1.ChatStreamReq) (res *v1.ChatStreamRes, err error) {
	// 派生 context，可主动取消模型和后台消费
	ctx, cancel := context.WithCancel(ctx)

	// B5: 在最外层注入 RequestBudget，使 retrieve 和 answerStream 共享同一累计 owner
	ctx = common.WithRequestBudget(ctx)

	// 解析注入边界，nil 时回退生产默认（运行期间只读）
	retrieve := c.chatRetrieve
	if retrieve == nil {
		retrieve = c.Retriever
	}
	answerStream := c.chatAnswerStream
	if answerStream == nil {
		answerStream = defaultAnswerStream
	}
	sseWrite := c.chatSSEWrite
	if sseWrite == nil {
		sseWrite = defaultSSEWrite
	}

	// defer 兜底清理：异常提前退出时释放资源。
	// 正常路径 cleanupDone=true 后 defer 不做任何事。
	// 成功路径不能被兜底取消抢先影响保存。
	var cleanup func(chat.StreamResult)
	cleanupDone := false
	defer func() {
		if !cleanupDone && cleanup != nil {
			cleanup(chat.StreamResult{}) // 失败路径：cancel → 通知 → 关闭 → 等待
		}
		cancel() // 释放 controller 派生 context（成功路径时 derivedCtx 已取消，此处为 no-op 安全）
	}()

	// 获取检索结果
	retriever, err := retrieve(ctx, &v1.RetrieverReq{
		Question:      req.Question,
		TopK:          req.TopK,
		Score:         req.Score,
		KnowledgeName: req.KnowledgeName,
	})
	if err != nil {
		cancel()
		g.Log().Error(ctx, err)
		return
	}
	// 获取流式响应；建流失败返回 error，不返回空成功、不进入 SSE 输出
	streamReader, cl, err := answerStream(ctx, req.ConvID, retriever.Document, req.Question)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("get answer stream: %w", err)
	}
	cleanup = cl

	// SSE 处理
	err = sseWrite(ctx, streamReader, retriever.Document)

	if err != nil {
		// 失败路径：cleanup 内部会 cancel 派生 context
		cleanup(chat.StreamResult{})
		cleanupDone = true
		return nil, fmt.Errorf("stream response: %w", err)
	}

	// 成功路径：cleanup 完成消费与保存判定，然后释放派生 context
	cleanup(chat.StreamResult{Success: true})
	cleanupDone = true

	return &v1.ChatStreamRes{}, nil
}

// defaultAnswerStream 生产建流实现：调用 chat 单例。运行期间只读。
func defaultAnswerStream(ctx context.Context, convID string, docs []*schema.Document, question string) (*schema.StreamReader[*schema.Message], func(chat.StreamResult), error) {
	return chat.GetChat().GetAnswerStream(ctx, convID, docs, question)
}

// defaultSSEWrite 生产 SSE 写入实现。运行期间只读。
func defaultSSEWrite(ctx context.Context, reader *schema.StreamReader[*schema.Message], docs []*schema.Document) error {
	return common.SteamResponse(ctx, reader, docs)
}
