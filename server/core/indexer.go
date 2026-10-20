package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"
	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/core/common"
	"github.com/wangle201210/go-rag/server/core/providers"
	coretypes "github.com/wangle201210/go-rag/server/core/types"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

type IndexReq struct {
	URI           string // 文档地址，可以是文件路径（pdf，html，md等），也可以是网址
	KnowledgeName string // 知识库名称
	DocumentsId   int64  // 文档ID
}

type IndexAsyncReq struct {
	Docs          []*schema.Document
	KnowledgeName string // 知识库名称
	DocumentsId   int64  // 文档ID
}

type IndexAsyncByDocsIDReq struct {
	DocsIDs       []string
	KnowledgeName string // 知识库名称
	DocumentsId   int64  // 文档ID
	try           int    // es 数据同步会有部分延迟，尝试多次
}

// syncIndexer 同步索引边界：读取文档、分割、合并、存储，返回 chunk IDs
type syncIndexer interface {
	invoke(ctx context.Context, knowledgeName string, source document.Source) ([]string, error)
}

// asyncIndexer 异步索引边界：QA 生成 + embedding。documentsID 直接使用 int64，避免字符串往返转换
type asyncIndexer interface {
	indexAsyncByDocsID(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error
}

// prodSyncIndexer 生产同步索引实现
type prodSyncIndexer struct {
	runnable compose.Runnable[any, []string]
}

func (p *prodSyncIndexer) invoke(ctx context.Context, knowledgeName string, source document.Source) ([]string, error) {
	ctx = context.WithValue(ctx, coretypes.KnowledgeName, knowledgeName)
	return p.runnable.Invoke(ctx, source)
}

// prodAsyncIndexer 生产异步索引实现
type prodAsyncIndexer struct {
	rag *Rag
}

func (p *prodAsyncIndexer) indexAsyncByDocsID(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
	_, err := p.rag.indexAsyncByDocsID(ctx, &IndexAsyncByDocsIDReq{
		DocsIDs:       docsIDs,
		KnowledgeName: knowledgeName,
		DocumentsId:   documentsID,
		try:           esTryFindDoc,
	})
	return err
}

// Index
// 这里处理文档的读取、分割、合并和存储
// 真正的embedding 和 QA 异步执行
// Stage 1D-3-A: 同步前准入 + 所有权转移 + Shutdown 协调 + 两路取消
// B5: 入口创建 TaskBudget，后台通过 TransferToBackground 显式移交。
func (x *Rag) Index(ctx context.Context, req *IndexReq) (ids []string, err error) {
	// B5: 注入任务级预算 owner
	// Fix 3: WithTaskBudget 现在返回 error
	ctx, taskBudget, budgetErr := common.WithTaskBudget(ctx)
	if budgetErr != nil {
		return nil, fmt.Errorf("create task budget: %w", budgetErr)
	}
	// === 同步前准入 ===
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return nil, ErrShuttingDown
	}
	// 非阻塞获取容量
	select {
	case x.bgSem <- struct{}{}:
		// 获取成功
	default:
		x.mu.Unlock()
		return nil, ErrBackgroundFull
	}
	// 登记
	x.bgWG.Add(1)
	x.mu.Unlock()

	// defer 兜底：如果同步失败或 panic，释放容量和登记
	released := false
	defer func() {
		if !released {
			<-x.bgSem     // 释放容量
			x.bgWG.Done() // 释放登记
		}
	}()

	// === 同步阶段两路取消 ===
	// 从请求 ctx 派生 syncCtx，保留 value/deadline/取消
	syncCtx, syncCancel := context.WithCancel(ctx)
	defer syncCancel() // 同步退出后释放

	// 检查 bgRootCtx 是否已取消（Shutdown 已在同步前调用）
	if x.bgRootCtx.Err() != nil {
		return nil, x.bgRootCtx.Err()
	}

	// 桥接服务级取消到 syncCtx：bgRootCtx 取消后自动调用 syncCancel
	// AfterFunc 注册的回调在 bgRootCtx 取消时自动执行
	stopAfterFunc := context.AfterFunc(x.bgRootCtx, syncCancel)
	// 保证全部退出路径注销 AfterFunc（正常返回、panic、runtime.Goexit）
	defer func() {
		if stopAfterFunc != nil {
			stopAfterFunc()
		}
	}()

	// === 同步索引 ===
	ids, err = x.syncIndexer.invoke(syncCtx, req.KnowledgeName, document.Source{URI: req.URI})

	// 同步调用退出后直接检查双路取消（不依赖 AfterFunc 调度）
	// 若同时存在业务错误和取消，用 errors.Join 保留可识别错误链
	syncErr := syncCtx.Err()
	rootErr := x.bgRootCtx.Err()
	if syncErr != nil || rootErr != nil {
		if err != nil {
			// 业务错误 + 取消：保留两者
			return nil, errors.Join(err, syncErr, rootErr)
		}
		if syncErr != nil {
			return nil, syncErr
		}
		return nil, rootErr
	}

	if err != nil {
		g.Log().Errorf(ctx, "Index idxer.Invoke failed, err=%v", err)
		return nil, err // defer 释放容量和登记
	}
	g.Log().Infof(ctx, "Index success, generated %d chunks with IDs: %v", len(ids), ids)

	// === 同步成功：所有权转移给后台 ===
	released = true // 标记：后台负责释放

	// 固定后台输入快照，goroutine 不读取 req 或共享 ids
	knowledgeName := req.KnowledgeName
	documentsID := req.DocumentsId
	docsIDs := make([]string, len(ids))
	copy(docsIDs, ids)

	// 启动 goroutine 前将等待函数和 asyncIndexer 保存为局部只读引用，运行期不再读共享字段
	waitFn := x.asyncWait
	asyncIdx := x.asyncIndexer

	// 后台从服务级 bgRootCtx 派生任务 timeout
	bgCtx, bgCancel := context.WithTimeout(x.bgRootCtx, x.taskTimeout)

	// B5: 后台显式移交 TaskBudget，共享累计额度与固定 deadline
	if taskBudget != nil {
		bgTaskBudget, transferErr := taskBudget.TransferToBackground()
		if transferErr == nil && bgTaskBudget != nil {
			// Fix 1: 创建共享 bgTaskBudget scope 的 RequestBudget，而非从 Registry 创建全新独立 scope
			bgRb, rbErr := bgTaskBudget.NewSharedRequestBudget(bgCtx)
			if rbErr == nil {
				bgCtx = providers.ContextWithRequestBudget(bgCtx, bgRb)
			}
		}
	}

	// 后台负责唯一一次释放（容量 + 登记）
	go func() {
		defer func() {
			<-x.bgSem     // 释放容量
			x.bgWG.Done() // 释放准入登记
			bgCancel()    // 释放任务 timeout
		}()

		// 统一终结处理：recover + markFailed
		var bgErr error
		defer func() {
			if r := recover(); r != nil {
				bgErr = fmt.Errorf("panic: %v", r)
				g.Log().Errorf(bgCtx, "Index background panic: %v", r)
			}
			// 失败时调用 markFailed（使用独立 cleanupCtx，不继承 bgCtx）
			if bgErr != nil && x.markFailed != nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				x.markFailed(cleanupCtx, documentsID, bgErr)
			}
		}()

		// 初始等待（可取消）：优先使用注入函数，否则生产默认一秒
		// 测试下来这里必须等待一段时间，否则 indexAsyncByDocsID 在 es 里搜不到该条数据（ES 同步本身有延迟）
		if waitFn != nil {
			waitFn(bgCtx)
		} else {
			select {
			case <-time.After(time.Second):
			case <-bgCtx.Done():
				bgErr = bgCtx.Err()
				return
			}
		}

		// 等待后检查 context：避免已取消后仍调用 asyncIndexer
		if bgCtx.Err() != nil {
			bgErr = bgCtx.Err()
			return // 进入统一失败终结
		}

		// 后台执行（indexAsyncByDocsID 内部会检查 bgCtx 取消）
		if asyncErr := asyncIdx.indexAsyncByDocsID(bgCtx, knowledgeName, documentsID, docsIDs); asyncErr != nil {
			bgErr = asyncErr
			g.Log().Errorf(bgCtx, "indexAsyncByDocsID failed, err=%v", asyncErr)
		}
	}()

	return ids, nil
}

// IndexAsync
// 通过 schema.Document 异步 生成QA&embedding
func (x *Rag) IndexAsync(ctx context.Context, req *IndexAsyncReq) (ids []string, err error) {
	ctx = context.WithValue(ctx, coretypes.KnowledgeName, req.KnowledgeName)
	ids, err = x.idxerAsync.Invoke(ctx, req.Docs)
	if err != nil {
		return
	}

	return
}

// 通过docIDs 异步 生成QA&embedding
// 这个方法不用暴露出去
//
// 错误传播契约（Stage 1D-1）：任一阶段失败都立即返回错误，不继续后续阶段，
// 且绝不把文档标为 Active。统一的 Failed 标记（markFailed）归属 1D-3。
func (x *Rag) indexAsyncByDocsID(ctx context.Context, req *IndexAsyncByDocsIDReq) (ids []string, err error) {
	// 入口检查：避免已取消后继续执行
	// 注意：不宣称检查能消除所有"检查后立即取消"的竞争窗口
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 0. 超限保护：避免递归重试把异常入参放大成超大检索
	if len(req.DocsIDs) > 1000 {
		return nil, fmt.Errorf("indexAsyncByDocsID: DocsIDs count %d exceeds limit 1000", len(req.DocsIDs))
	}
	// 空入参无法做任何完整性校验，直接报错（保持原有「no documents found」语义，不误标 Active）
	if len(req.DocsIDs) == 0 {
		return nil, fmt.Errorf("indexAsyncByDocsID: empty DocsIDs for document %d", req.DocumentsId)
	}

	// 1. 检索文档
	g.Log().Infof(ctx, "indexAsyncByDocsID searching for %d docs in knowledge base: %s, IDs: %v", len(req.DocsIDs), req.KnowledgeName, req.DocsIDs)
	searchResp, err := x.docSearch.SearchDocumentsByIDs(ctx, req.KnowledgeName, req.DocsIDs, 1000)
	if err != nil {
		g.Log().Errorf(ctx, "indexAsyncByDocsID SearchDocumentsByIDs failed, err=%v", err)
		return nil, fmt.Errorf("indexAsyncByDocsID search documents: %w", err)
	}

	// 2. 按 ID 集合核对完整性（不再用数量比较：数量相同但 ID 不匹配同样是缺失）
	expected := make(map[string]struct{}, len(req.DocsIDs))
	for _, id := range req.DocsIDs {
		expected[id] = struct{}{}
	}
	actual := make(map[string]struct{}, len(searchResp))
	for _, doc := range searchResp {
		if doc != nil {
			actual[doc.ID] = struct{}{}
		}
	}
	var missing []string
	for id := range expected {
		if _, ok := actual[id]; !ok {
			missing = append(missing, id)
		}
	}

	// 3. 缺失时重试；重试耗尽仍缺失则报错，不带着不完整数据继续
	if len(missing) > 0 && req.try > 0 {
		g.Log().Warningf(ctx, "indexAsyncByDocsID missing %d docs, missing=%v, remaining tries=%d", len(missing), missing, req.try)
		req.try--
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		return x.indexAsyncByDocsID(ctx, req)
	}
	if len(missing) > 0 {
		sort.Strings(missing) // 确定性错误文本，便于断言与日志比对
		g.Log().Errorf(ctx, "indexAsyncByDocsID documents missing after retries, missing=%v", missing)
		return nil, fmt.Errorf("indexAsyncByDocsID: documents missing after retries: %v", missing)
	}

	// 3.5 返回集合完整性校验：非 nil、ID 必须属于预期集合、无重复 ID。
	// 任一非法返回都在保存/后续索引之前直接报错，绝不调用 saveChunksFn/idxerAsync/updateDocStatus，
	// 避免把非预期文档写入当前文档或重复保存/重复索引。（缺失/重试已在前面处理）
	seen := make(map[string]struct{}, len(searchResp))
	for i, doc := range searchResp {
		if doc == nil {
			return nil, fmt.Errorf("indexAsyncByDocsID: nil document at index %d in search response", i)
		}
		if _, ok := expected[doc.ID]; !ok {
			return nil, fmt.Errorf("indexAsyncByDocsID: unexpected document id %q returned for document %d", doc.ID, req.DocumentsId)
		}
		if _, dup := seen[doc.ID]; dup {
			return nil, fmt.Errorf("indexAsyncByDocsID: duplicate document id %q in search response", doc.ID)
		}
		seen[doc.ID] = struct{}{}
	}

	// 4. 构建 chunks（含元数据编码）；nil/非预期/重复已在上一步拒绝，此处 doc 均为有效非 nil
	var chunks []entity.KnowledgeChunks
	var marshalErrs []error
	for _, doc := range searchResp {
		docParseExt(doc)
		ext, marshalErr := sonic.Marshal(doc.MetaData)
		if marshalErr != nil {
			g.Log().Errorf(ctx, "indexAsyncByDocsID sonic.Marshal failed for doc %s, err=%v", doc.ID, marshalErr)
			marshalErrs = append(marshalErrs, fmt.Errorf("marshal doc %s metadata: %w", doc.ID, marshalErr))
			continue
		}
		// Id 设置为 0，让数据库自动分配（兼容 MySQL 和 SQLite）
		chunks = append(chunks, entity.KnowledgeChunks{
			Id:             0,
			KnowledgeDocId: req.DocumentsId,
			ChunkId:        doc.ID,
			Content:        doc.Content,
			Ext:            string(ext),
		})
	}

	// 元数据编码失败 → 聚合返回，不继续后续阶段（旧行为是 continue 静默丢 chunk）
	if len(marshalErrs) > 0 {
		return nil, errors.Join(marshalErrs...)
	}

	// 5. 保存 chunks：失败则传播，不再继续 IndexAsync，也不标 Active
	if err = x.saveChunksFn(ctx, req.DocumentsId, chunks); err != nil {
		g.Log().Errorf(ctx, "indexAsyncByDocsID insert chunks failed, err=%v", err)
		return nil, err
	}

	// 6. 后续索引（QA + embedding）
	asyncReq := &IndexAsyncReq{
		Docs:          searchResp,
		KnowledgeName: req.KnowledgeName,
		DocumentsId:   req.DocumentsId,
	}
	ids, err = x.IndexAsync(ctx, asyncReq)
	if err != nil {
		g.Log().Errorf(ctx, "indexAsyncByDocsID IndexAsync failed, err=%v", err)
		return nil, err // 后续索引失败不标 Active
	}

	// 7. 标记 Active（所有前置条件均满足）
	if err = x.updateDocStatus(ctx, req.DocumentsId, int(v1.StatusActive)); err != nil {
		g.Log().Errorf(ctx, "indexAsyncByDocsID UpdateDocumentsStatus Active failed, err=%v", err)
		return nil, err
	}

	return ids, nil
}

// 检索会把原来的 MetaData 放到 MetaData.ext 中，这里需要把原来的 MetaData 恢复
func docParseExt(doc *schema.Document) {
	if ext, ok := doc.MetaData[coretypes.FieldExtra].(string); ok && len(ext) > 0 {
		extData := map[string]any{}
		if err := sonic.Unmarshal([]byte(doc.MetaData[coretypes.FieldExtra].(string)), &extData); err != nil {
			// 忽略err
			g.Log().Errorf(gctx.New(), "documentParseExt unmarshal failed, err=%v", err)
			return
		}
		doc.MetaData = extData
	}
}

func (x *Rag) DeleteDocument(ctx context.Context, documentID string) error {
	return x.conf.DeleteDocument(ctx, documentID)
}
