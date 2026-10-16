package core

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/config"
	"github.com/wangle201210/go-rag/server/core/grader"
	"github.com/wangle201210/go-rag/server/core/indexer"
	"github.com/wangle201210/go-rag/server/core/retriever"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/logic/knowledge"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

const (
	scoreThreshold = 1.05 // 设置一个很小的阈值
	esTopK         = 50
	esTryFindDoc   = 10
)

// docSearcher 文档检索边界：按 ID 集合从向量库/ES 取回文档，支持测试注入。
type docSearcher interface {
	SearchDocumentsByIDs(ctx context.Context, knowledgeName string, docIDs []string, size int) ([]*schema.Document, error)
}

type Rag struct {
	syncIndexer  syncIndexer                                    // 注入边界：同步索引
	asyncIndexer asyncIndexer                                   // 注入边界：异步索引
	idxerAsync   compose.Runnable[[]*schema.Document, []string] // 异步文档索引器
	rtrvr        compose.Runnable[string, []*schema.Document]
	qaRtrvr      compose.Runnable[string, []*schema.Document]
	client       *elasticsearch.Client // 保留用于兼容
	cm           model.BaseChatModel

	grader *grader.Grader // 暂时先弃用，使用 grader 会严重影响rag的速度
	conf   *config.Config

	docRet    docRetriever        // 注入边界：用于测试替换
	rwModel   model.BaseChatModel // 注入边界：用于测试替换 rewrite model
	asyncWait func()              // 可注入等待机制，测试用

	// 以下三个字段与 syncIndexer/asyncIndexer/docRet 同模式：
	// 构建时固定、运行期只读，不使用可变包级函数替换。
	docSearch       docSearcher                                                                   // 注入边界：文档检索
	saveChunksFn    func(ctx context.Context, docID int64, chunks []entity.KnowledgeChunks) error // 注入边界：保存 chunks
	updateDocStatus func(ctx context.Context, docID int64, status int) error                      // 注入边界：更新文档状态
}

func New(ctx context.Context, conf *config.Config) (*Rag, error) {
	if len(conf.IndexName) == 0 {
		return nil, fmt.Errorf("indexName is empty")
	}
	// 确保 index 存在
	exists, err := conf.IndexExists(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		err = conf.CreateIndex(ctx)
		if err != nil {
			return nil, err
		}
	}
	buildIndex, err := indexer.BuildIndexer(ctx, conf)
	if err != nil {
		return nil, err
	}

	// QA 配置：条件预热 qaModel
	qaEnabled := conf.QAEnabled == nil || *conf.QAEnabled
	var qaFactory func(context.Context) (model.BaseChatModel, error)
	if qaEnabled {
		qaModel, err := common.GetQAModel(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("GetQAModel failed: %w", err)
		}
		_ = qaModel // 预热完成
		qaFactory = func(ctx context.Context) (model.BaseChatModel, error) {
			return common.GetQAModel(ctx, nil)
		}
	}

	buildIndexAsync, err := indexer.BuildIndexerAsync(ctx, conf, qaFactory)
	if err != nil {
		return nil, err
	}
	buildRetriever, err := retriever.BuildRetriever(ctx, conf)
	if err != nil {
		return nil, err
	}
	qaCtx := context.WithValue(ctx, coretypes.RetrieverFieldKey, coretypes.FieldQAContentVector)
	qaRetriever, err := retriever.BuildRetriever(qaCtx, conf)
	if err != nil {
		return nil, err
	}
	cm, err := common.GetChatModel(ctx, nil)
	if err != nil {
		g.Log().Error(ctx, "GetChatModel failed, err=%v", err)
		return nil, err
	}
	rag := &Rag{
		idxerAsync: buildIndexAsync,
		rtrvr:      buildRetriever,
		qaRtrvr:    qaRetriever,
		client:     conf.Client,
		cm:         cm,
		conf:       conf,
		// grader:  grader.NewGrader(cm),
	}
	rag.syncIndexer = &prodSyncIndexer{runnable: buildIndex} // 发布前设置注入字段，运行期间只读
	rag.asyncIndexer = &prodAsyncIndexer{rag: rag}           // 发布前设置注入字段，运行期间只读
	rag.docRet = &prodDocRetriever{rag: rag}                 // 发布前设置注入字段，运行期间只读
	rag.docSearch = conf                                     // *config.Config 实现 SearchDocumentsByIDs
	rag.saveChunksFn = knowledge.SaveChunksData
	rag.updateDocStatus = knowledge.UpdateDocumentsStatus
	return rag, nil
}

// GetKnowledgeBaseList 获取知识库列表
func (x *Rag) GetKnowledgeBaseList(ctx context.Context) (list []string, err error) {
	return x.conf.GetKnowledgeBaseList(ctx)
}
