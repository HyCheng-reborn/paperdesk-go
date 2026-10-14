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
	defer cancel()

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

	// 获取检索结果
	retriever, err := retrieve(ctx, &v1.RetrieverReq{
		Question:      req.Question,
		TopK:          req.TopK,
		Score:         req.Score,
		KnowledgeName: req.KnowledgeName,
	})
	if err != nil {
		g.Log().Error(ctx, err)
		return
	}
	// 获取流式响应；建流失败返回 error，不返回空成功、不进入 SSE 输出
	streamReader, cleanup, err := answerStream(ctx, req.ConvID, retriever.Document, req.Question)
	if err != nil {
		return nil, fmt.Errorf("get answer stream: %w", err)
	}
	// 清理：通知后台 goroutine 结果 + 关闭所有流分支
	cleanupDone := false
	defer func() {
		if !cleanupDone {
			cleanup(chat.StreamResult{}) // 默认零值：Success=false → 不保存
		}
	}()

	err = sseWrite(ctx, streamReader, retriever.Document)
	if err != nil {
		return nil, fmt.Errorf("stream response: %w", err)
	}

	// SSE 正常完成：通知后台保存
	cleanupDone = true
	cleanup(chat.StreamResult{Success: true})
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
