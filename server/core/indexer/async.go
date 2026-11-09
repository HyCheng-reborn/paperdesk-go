package indexer

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/core/config"
)

// buildAsyncGraph 组装异步索引 Graph：Indexer 节点 + QA 节点（启用/关闭分支）+ 边 + Compile。
// 这是唯一的装配实现：生产 BuildIndexerAsync 传入真实 indexer.Indexer；
// 测试向同一 helper 注入实现 indexer.Indexer 接口的 fake。
// QA 并发上限处理、节点连接与 Compile 只在此处实现一次，并检查/传播所有构图错误。
func buildAsyncGraph(
	ctx context.Context,
	idr indexer.Indexer,
	qaEnabled bool,
	maxConcurrent int,
	qaModelFactory func(context.Context) (model.BaseChatModel, error),
) (compose.Runnable[[]*schema.Document, []string], error) {
	const (
		indexerNode = "Indexer"
		qaNode      = "QA"
	)

	g := compose.NewGraph[[]*schema.Document, []string]()

	if err := g.AddIndexerNode(indexerNode, idr); err != nil {
		return nil, fmt.Errorf("buildAsyncGraph: add indexer node: %w", err)
	}

	mc := maxConcurrent
	if mc <= 0 {
		mc = 4
	}
	if mc > 32 {
		mc = 32
	}

	if qaEnabled && qaModelFactory != nil {
		// QA 启用：errgroup 有限并发生成
		factory := qaModelFactory
		qaLambda := compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return qa(ctx, docs, factory, mc)
		})
		if err := g.AddLambdaNode(qaNode, qaLambda); err != nil {
			return nil, fmt.Errorf("buildAsyncGraph: add qa node: %w", err)
		}
	} else {
		// QA 关闭：identity lambda，直接返回 docs 不变
		identity := compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
			return docs, nil
		})
		if err := g.AddLambdaNode(qaNode, identity); err != nil {
			return nil, fmt.Errorf("buildAsyncGraph: add qa identity node: %w", err)
		}
	}

	if err := g.AddEdge(compose.START, qaNode); err != nil {
		return nil, fmt.Errorf("buildAsyncGraph: add edge START->QA: %w", err)
	}
	if err := g.AddEdge(qaNode, indexerNode); err != nil {
		return nil, fmt.Errorf("buildAsyncGraph: add edge QA->Indexer: %w", err)
	}
	if err := g.AddEdge(indexerNode, compose.END); err != nil {
		return nil, fmt.Errorf("buildAsyncGraph: add edge Indexer->END: %w", err)
	}

	r, err := g.Compile(ctx, compose.WithGraphName("indexer_async"))
	if err != nil {
		return nil, fmt.Errorf("buildAsyncGraph: compile graph: %w", err)
	}
	return r, nil
}

// BuildIndexerAsync 构建异步索引器。
// qaModelFactory: 构建时固定的 QA 模型获取函数；为 nil 时 QA 节点不生成内容。
// 创建真实 Indexer 后委派给共享的 buildAsyncGraph 装配 helper。
func BuildIndexerAsync(ctx context.Context, conf *config.Config, qaModelFactory func(context.Context) (model.BaseChatModel, error)) (r compose.Runnable[[]*schema.Document, []string], err error) {
	idr, err := newAsyncIndexer(ctx, conf)
	if err != nil {
		return nil, err
	}

	qaEnabled := conf.QAEnabled == nil || *conf.QAEnabled
	return buildAsyncGraph(ctx, idr, qaEnabled, conf.QAMaxConcurrent, qaModelFactory)
}
