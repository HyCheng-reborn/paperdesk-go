package indexer

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
)

// BuildIndexerAsync 构建异步索引器。
// qaModelFactory: 构建时固定的 QA 模型获取函数；为 nil 时 QA 节点不生成内容。
func BuildIndexerAsync(ctx context.Context, conf *config.Config, qaModelFactory func(context.Context) (model.BaseChatModel, error)) (r compose.Runnable[[]*schema.Document, []string], err error) {
	const (
		Indexer = "Indexer"
		QA      = "QA"
	)

	g := compose.NewGraph[[]*schema.Document, []string]()
	indexer2KeyOfIndexer, err := newAsyncIndexer(ctx, conf)
	if err != nil {
		return nil, err
	}
	_ = g.AddIndexerNode(Indexer, indexer2KeyOfIndexer)

	// 计算 QA 配置
	qaEnabled := conf.QAEnabled == nil || *conf.QAEnabled
	maxConcurrent := conf.QAMaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	if maxConcurrent > 32 {
		maxConcurrent = 32
	}

	if qaEnabled && qaModelFactory != nil {
		// QA 启用：使用 errgroup 有限并发生成
		factory := qaModelFactory
		mc := maxConcurrent
		_ = g.AddLambdaNode(QA, compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return qa(ctx, docs, factory, mc)
		}))
	} else {
		// QA 关闭：identity lambda，直接返回 docs 不变
		_ = g.AddLambdaNode(QA, compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return docs, nil
		}))
	}

	_ = g.AddEdge(compose.START, QA)
	_ = g.AddEdge(QA, Indexer)
	_ = g.AddEdge(Indexer, compose.END)
	r, err = g.Compile(ctx, compose.WithGraphName("indexer_async"))
	if err != nil {
		return nil, err
	}
	return r, err
}
