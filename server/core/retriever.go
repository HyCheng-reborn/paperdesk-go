package core

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cloudwego/eino-ext/components/retriever/es8"
	"github.com/cloudwego/eino/components/model"
	er "github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8/typedapi/types"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/qdrant/go-client/qdrant"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/rerank"
	"github.com/wangle201210/go-rag/server/core/retriever"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
)

// docRetriever 注入边界：检索单轮文档的抽象，便于测试替换
type docRetriever interface {
	retrieveDoOnce(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error)
}

// prodDocRetriever 生产实现：委托给 Rag.retrieveDoOnce
type prodDocRetriever struct {
	rag *Rag
}

func (p *prodDocRetriever) retrieveDoOnce(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
	return p.rag.retrieveDoOnce(ctx, req)
}

type RetrieveReq struct {
	Query         string   // 检索关键词
	TopK          int      // 检索结果数量
	Score         float64  //  分数阀值(0-2, 0 完全相反，1 毫不相干，2 完全相同,一般需要传入一个大于1的数字，如1.5)
	KnowledgeName string   // 知识库名字
	optQuery      string   // 优化后的检索关键词
	excludeIDs    []string // 要排除的 _id 列表
	rankScore     float64  // 排名分数，原本的score是0-2（实际是1-2），需要在这里改成0-1
}

// Retrieve 检索
func (x *Rag) Retrieve(ctx context.Context, req *RetrieveReq) ([]*schema.Document, error) {
	// 输入校验
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	if req.TopK <= 0 {
		return nil, fmt.Errorf("invalid TopK: %d", req.TopK)
	}

	// 预取消检查
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 创建本次调用的局部请求副本，不修改输入
	localReq := *req
	localReq.rankScore = localReq.Score
	// 大于1的需要-1
	if localReq.rankScore >= 1 {
		localReq.rankScore -= 1
	}

	// 获取 rewrite model：优先使用注入的（测试），否则走生产路径
	var rewriteModel model.BaseChatModel
	if x.rwModel != nil {
		rewriteModel = x.rwModel
	} else {
		var rwErr error
		rewriteModel, rwErr = common.GetRewriteModel(ctx, nil)
		if rwErr != nil {
			return nil, rwErr
		}
	}

	// 获取 docRetriever：使用局部变量，不写回共享字段
	dr := x.docRet
	if dr == nil {
		dr = &prodDocRetriever{rag: x}
	}

	// 派生 context：任意步骤失败后取消所有进行中的工作
	derivedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 受保护的首个错误记录
	var firstErr error
	var firstErrMu sync.Mutex

	recordError := func(err error) {
		firstErrMu.Lock()
		defer firstErrMu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	resultCh := make(chan []*schema.Document, 3)

	used := ""
	for i := 0; i < 3; i++ {
		// 取消后不继续启动新轮次
		if derivedCtx.Err() != nil {
			break
		}

		// 每轮构建独立的优化查询消息
		optMessages, genErr := getOptimizedQueryMessages(used, req.Query, req.KnowledgeName)
		if genErr != nil {
			cancel()
			recordError(fmt.Errorf("round %d build message: %w", i, genErr))
			break
		}

		rewriteMessage, genErr := rewriteModel.Generate(derivedCtx, optMessages)
		if genErr != nil {
			cancel()
			recordError(fmt.Errorf("round %d generate: %w", i, genErr))
			break
		}
		optimizedQuery := rewriteMessage.Content
		used += optimizedQuery + " "

		// 固定该轮的局部请求副本
		roundReq := localReq
		roundReq.optQuery = optimizedQuery

		// copy() 对 excludeIDs 切片创建独立副本
		if len(localReq.excludeIDs) > 0 {
			roundReq.excludeIDs = make([]string, len(localReq.excludeIDs))
			copy(roundReq.excludeIDs, localReq.excludeIDs)
		}

		wg.Add(1)
		go func(r RetrieveReq, round int) {
			defer wg.Done()
			// worker 失败立即触发取消
			result, localErr := dr.retrieveDoOnce(derivedCtx, &r)
			if localErr != nil {
				cancel()
				recordError(fmt.Errorf("round %d retrieve: %w", round, localErr))
				errCh <- localErr
				return
			}
			resultCh <- result
		}(roundReq, i)
	}

	// 等待所有已启动 worker 完成
	wg.Wait()
	close(errCh)
	close(resultCh)

	// 检查首个错误
	if firstErr != nil {
		return nil, firstErr
	}

	// 收集所有结果
	var allResults []*schema.Document
	for result := range resultCh {
		allResults = append(allResults, result...)
	}

	// 单一流程聚合
	return aggregateResults(allResults, req.TopK)
}

// aggregateResults 聚合多轮检索结果：同 ID 保留最高分，按分数降序排序，截取 TopK
func aggregateResults(results []*schema.Document, topK int) ([]*schema.Document, error) {
	// 同 ID 文档保留最高分
	seen := make(map[string]*schema.Document)
	for _, doc := range results {
		if existing, ok := seen[doc.ID]; ok {
			if doc.Score() > existing.Score() {
				seen[doc.ID] = doc
			}
		} else {
			seen[doc.ID] = doc
		}
	}

	// 转换为切片
	unique := make([]*schema.Document, 0, len(seen))
	for _, doc := range seen {
		unique = append(unique, doc)
	}

	// 按分数排序，相同分数设置稳定的次级排序规则
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Score() != unique[j].Score() {
			return unique[i].Score() > unique[j].Score()
		}
		return unique[i].ID < unique[j].ID
	})

	// 截取 TopK
	if len(unique) > topK {
		unique = unique[:topK]
	}

	return unique, nil
}

func (x *Rag) retrieveDoOnce(ctx context.Context, req *RetrieveReq) (relatedDocs []*schema.Document, err error) {
	var (
		docs   []*schema.Document
		qaDocs []*schema.Document
	)
	g.Log().Infof(ctx, "query: %v", req.optQuery)
	// 向量路一：content_vector
	docs, err = x.retrieve(ctx, req, false)
	if err != nil {
		g.Log().Errorf(ctx, "retrieve failed, err=%v", err)
		return
	}
	// 向量路二：qa_content_vector
	qaDocs, err = x.retrieve(ctx, req, true)
	if err != nil {
		g.Log().Errorf(ctx, "qa retrieve failed, err=%v", err)
		return
	}

	inputs := [][]*schema.Document{docs, qaDocs}

	// 关键字路三：ES BM25（conf.Client != nil 时生效）
	if x.conf.Client != nil {
		bm25Docs, bm25Err := retriever.Bm25Retrieve(ctx, x.conf.Client, x.conf.IndexName, req.KnowledgeName, req.optQuery, req.excludeIDs, esTopK)
		if bm25Err != nil {
			g.Log().Warningf(ctx, "bm25 retrieve failed, skip: %v", bm25Err)
		} else {
			inputs = append(inputs, bm25Docs)
		}
	}

	// RRF 融合多路结果（同时完成去重）
	docs = retriever.RRFFusion(inputs)

	// 重排
	rerankConf, err := rerank.GetConf(ctx)
	if err != nil {
		g.Log().Errorf(ctx, "rerank GetConf failed, err=%v", err)
		return
	}
	reranker := rerank.NewRerank(rerankConf.ToRerankConfig(), nil)
	docs, err = reranker.Rerank(ctx, req.optQuery, docs, req.TopK)
	if err != nil {
		g.Log().Errorf(ctx, "Rerank failed, err=%v", err)
		return
	}
	for _, doc := range docs {
		if doc.Score() < req.rankScore {
			g.Log().Debugf(ctx, "score less: %v, related: %v", doc.Score(), doc.Content)
			continue
		}
		relatedDocs = append(relatedDocs, doc)
	}
	return
}

func (x *Rag) retrieve(ctx context.Context, req *RetrieveReq, qa bool) (msg []*schema.Document, err error) {
	// 准备检索选项
	retrieveOpts := []er.Option{
		er.WithTopK(esTopK),
	}

	// 根据向量存储类型添加不同的过滤条件
	if x.conf.Client != nil {
		// ES 过滤条件
		esQuery := []types.Query{
			{
				Bool: &types.BoolQuery{
					Must: []types.Query{{Match: map[string]types.MatchQuery{coretypes.KnowledgeName: {Query: req.KnowledgeName}}}},
				},
			},
		}
		if len(req.excludeIDs) > 0 {
			esQuery[0].Bool.MustNot = []types.Query{
				{
					Terms: &types.TermsQuery{
						TermsQuery: map[string]types.TermsQueryField{
							"_id": req.excludeIDs,
						},
					},
				},
			}
		}
		retrieveOpts = append(retrieveOpts, es8.WithFilters(esQuery))
	} else if x.conf.QdrantClient != nil {
		// Qdrant 过滤条件
		qdrantFilter := &qdrant.Filter{
			Must: []*qdrant.Condition{
				{
					ConditionOneOf: &qdrant.Condition_Field{
						Field: &qdrant.FieldCondition{
							Key: coretypes.KnowledgeName,
							Match: &qdrant.Match{
								MatchValue: &qdrant.Match_Keyword{
									Keyword: req.KnowledgeName,
								},
							},
						},
					},
				},
			},
		}

		// 添加排除 ID 的条件
		if len(req.excludeIDs) > 0 {
			// 将 string IDs 转换为 PointId
			excludePointIds := make([]*qdrant.PointId, len(req.excludeIDs))
			for i, id := range req.excludeIDs {
				excludePointIds[i] = &qdrant.PointId{
					PointIdOptions: &qdrant.PointId_Uuid{Uuid: id},
				}
			}

			qdrantFilter.MustNot = []*qdrant.Condition{
				{
					ConditionOneOf: &qdrant.Condition_HasId{
						HasId: &qdrant.HasIdCondition{
							HasId: excludePointIds,
						},
					},
				},
			}
		}

		// 通过 DSLInfo 传递过滤条件（自定义 retriever 会读取）
		retrieveOpts = append(retrieveOpts, er.WithDSLInfo(map[string]any{
			"filter": qdrantFilter,
		}))
	}

	r := x.rtrvr
	if qa {
		r = x.qaRtrvr
	}

	msg, err = r.Invoke(ctx, req.optQuery,
		compose.WithRetrieverOption(retrieveOpts...),
	)

	if err != nil {
		return
	}

	// 复制 Document 和 MetaData 后再修改分数，避免污染原始数据
	copied := make([]*schema.Document, len(msg))
	for i, s := range msg {
		docCopy := *s
		if s.MetaData != nil {
			docCopy.MetaData = make(map[string]any, len(s.MetaData))
			for k, v := range s.MetaData {
				docCopy.MetaData[k] = v
			}
		}
		if docCopy.Score() > 1 {
			// 本身没意义，最终分数由rerank给，这里只是为了方便测试观察
			docCopy = *docCopy.WithScore(docCopy.Score() - 1)
		}
		copied[i] = &docCopy
	}

	return copied, nil
}
