package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	v1 "github.com/wangle201210/go-rag/server/api/rag/v1"
	"github.com/wangle201210/go-rag/server/internal/dao"
	"github.com/wangle201210/go-rag/server/internal/model/entity"
)

// ChunkStore 定义 chunk 数据访问操作，作为可注入边界支持离线测试。
type ChunkStore interface {
	// FindByChunkID 查询已存在的 chunk 记录；无记录时返回 (nil, sql.ErrNoRows)。
	FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error)
	// Insert 插入新 chunk 记录。
	Insert(ctx context.Context, chunk entity.KnowledgeChunks) error
	// Update 按 chunk_id 更新指定字段。
	Update(ctx context.Context, chunkID string, data map[string]interface{}) error
}

// DocStatusUpdater 定义文档状态更新操作，作为可注入边界支持离线测试。
type DocStatusUpdater interface {
	UpdateDocumentsStatus(ctx context.Context, documentsId int64, status int) error
}

// daoChunkStore chunkStore 的生产实现，委派到 dao。
type daoChunkStore struct{}

func (d *daoChunkStore) FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error) {
	var existing entity.KnowledgeChunks
	// gf 的 Scan 在无记录时返回 sql.ErrNoRows，由调用方区分「未找到」与「DB 故障」
	if err := dao.KnowledgeChunks.Ctx(ctx).Where("chunk_id", chunkID).Scan(&existing); err != nil {
		return nil, err
	}
	return &existing, nil
}

func (d *daoChunkStore) Insert(ctx context.Context, chunk entity.KnowledgeChunks) error {
	_, err := dao.KnowledgeChunks.Ctx(ctx).Data(chunk).OmitEmpty().Insert()
	return err
}

func (d *daoChunkStore) Update(ctx context.Context, chunkID string, data map[string]interface{}) error {
	_, err := dao.KnowledgeChunks.Ctx(ctx).Where("chunk_id", chunkID).Data(data).Update()
	return err
}

// daoDocStatusUpdater docStatusUpdater 的生产实现，委派到包级 UpdateDocumentsStatus。
type daoDocStatusUpdater struct{}

func (d *daoDocStatusUpdater) UpdateDocumentsStatus(ctx context.Context, documentsId int64, status int) error {
	return UpdateDocumentsStatus(ctx, documentsId, status)
}

// SaveChunksData 批量保存知识块数据（公开入口，签名保持不变）。
// 逐个插入或更新，避免 SQLite 的 ON CONFLICT 语法问题。
// 任一环节失败都会聚合返回，并尽力把文档状态置为 Failed。
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error {
	return saveChunks(ctx, documentsId, chunks, &daoChunkStore{}, &daoDocStatusUpdater{})
}

// SaveChunksDataWithoutStatus 与 SaveChunksData 相同但不写文档状态，
// 供后台编排使用，由后台统一终结所有者标 Failed。
func SaveChunksDataWithoutStatus(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error {
	return saveChunks(ctx, documentsId, chunks, &daoChunkStore{}, nil /* no statusUpdater */)
}

// indexingOnlyUpdater 只写 Indexing 状态，不写 Failed。
// 供后台编排使用，Failed 由后台统一终结所有者标记。
type indexingOnlyUpdater struct {
	inner DocStatusUpdater
}

func (u *indexingOnlyUpdater) UpdateDocumentsStatus(ctx context.Context, documentsId int64, status int) error {
	// 只写 Indexing，跳过 Failed
	if status == int(v1.StatusFailed) {
		return nil // 不写 Failed，由后台 markFailed 负责
	}
	return u.inner.UpdateDocumentsStatus(ctx, documentsId, status)
}

// SaveChunksDataIndexingOnly 与 SaveChunksData 共享内核，
// 但只写 Indexing 状态，不写 Failed（由后台统一终结）。
func SaveChunksDataIndexingOnly(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error {
	return saveChunks(ctx, documentsId, chunks, &daoChunkStore{}, &indexingOnlyUpdater{inner: &daoDocStatusUpdater{}})
}

// SaveChunksDataWithDeps 共享 saveChunks 内核，允许注入 chunkStore 和 docStatusUpdater。
// 用于跨包测试，使用 indexingOnly 策略：成功写 Indexing，chunk 失败仅传播不写 Failed（由后台 markFailed 统一终结）。
func SaveChunksDataWithDeps(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks, store ChunkStore, updater DocStatusUpdater) error {
	return saveChunks(ctx, documentsId, chunks, store, &indexingOnlyUpdater{inner: updater})
}

// saveChunks 内部 helper，依赖通过接口注入，便于同包测试直接验证错误传播。
//
// 契约：
//   - 空 chunks 直接返回 nil，且不写文档状态；
//   - ctx 已取消时快速返回 ctx.Err()，不执行任何 DB 操作；
//   - FindByChunkID 的非 sql.ErrNoRows 错误视为 DB 故障，不降级为 Insert；
//   - update 路径只写内容字段，不覆盖 chunk 自身的 status；
//   - 任一 chunk 失败即把文档状态降为 Failed，但继续处理剩余 chunk 并聚合所有错误。
func saveChunks(
	ctx context.Context,
	documentsId int64,
	chunks []entity.KnowledgeChunks,
	store ChunkStore,
	statusUpdater DocStatusUpdater,
) error {
	if len(chunks) == 0 {
		return nil
	}

	// 边界：预取消快速返回（仅证明应用层检查，不代表 driver 能中断执行中的 SQL）
	if ctx.Err() != nil {
		return ctx.Err()
	}

	var errs []error
	status := int(v1.StatusIndexing)

	for _, chunk := range chunks {
		// 循环中取消检查：停止处理后续 chunk
		if ctx.Err() != nil {
			errs = append(errs, fmt.Errorf("chunk %s: %w", chunk.ChunkId, ctx.Err()))
			status = int(v1.StatusFailed)
			break
		}

		existing, err := store.FindByChunkID(ctx, chunk.ChunkId)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			// DB 故障：聚合错误并跳过，绝不降级为 Insert（避免重复写入）
			errs = append(errs, fmt.Errorf("find chunk %s: %w", chunk.ChunkId, err))
			status = int(v1.StatusFailed)
			continue
		}

		if err == nil && existing != nil && existing.Id > 0 {
			// 已存在 → update 路径（不写 status 字段，避免覆盖 chunk 自身状态）
			data := map[string]interface{}{
				"knowledge_doc_id": chunk.KnowledgeDocId,
				"content":          chunk.Content,
				"ext":              chunk.Ext,
			}
			if updateErr := store.Update(ctx, chunk.ChunkId, data); updateErr != nil {
				errs = append(errs, fmt.Errorf("update chunk %s: %w", chunk.ChunkId, updateErr))
				status = int(v1.StatusFailed)
			}
		} else {
			// 不存在（sql.ErrNoRows 或 Id 为零值）→ insert 路径，id 置 0 由数据库分配
			chunk.Id = 0
			if insertErr := store.Insert(ctx, chunk); insertErr != nil {
				errs = append(errs, fmt.Errorf("insert chunk %s: %w", chunk.ChunkId, insertErr))
				status = int(v1.StatusFailed)
			}
		}
	}

	// 状态写入：失败同样聚合到返回值，不吞掉
	// statusUpdater 为 nil 时跳过状态写入（供后台编排使用，由 markFailed 统一终结）
	if statusUpdater != nil {
		if statusErr := statusUpdater.UpdateDocumentsStatus(ctx, documentsId, status); statusErr != nil {
			errs = append(errs, fmt.Errorf("update doc %d status: %w", documentsId, statusErr))
		}
	}

	return errors.Join(errs...)
}

// GetChunksList 查询知识块列表
func GetChunksList(ctx context.Context, where entity.KnowledgeChunks, page, size int) (list []entity.KnowledgeChunks, total int, err error) {
	model := dao.KnowledgeChunks.Ctx(ctx)

	// 构建查询条件
	if where.KnowledgeDocId != 0 {
		model = model.Where("knowledge_doc_id", where.KnowledgeDocId)
	}
	if where.ChunkId != "" {
		model = model.Where("chunk_id", where.ChunkId)
	}

	// 获取总数
	total, err = model.Count()
	if err != nil {
		return
	}

	// 分页查询
	if page > 0 && size > 0 {
		model = model.Page(page, size)
	}

	// 按创建时间倒序
	model = model.OrderDesc("created_at")

	err = model.Scan(&list)
	return
}

// GetChunkById 根据ID查询单个知识块
func GetChunkById(ctx context.Context, id int64) (chunk entity.KnowledgeChunks, err error) {
	err = dao.KnowledgeChunks.Ctx(ctx).Where("id", id).Scan(&chunk)
	return
}

// DeleteChunkByIds 根据ID软删除知识块
func DeleteChunkById(ctx context.Context, id int64) error {
	_, err := dao.KnowledgeChunks.Ctx(ctx).Where("id", id).Delete()
	return err
}

// UpdateChunkById 根据ID更新知识块
func UpdateChunkByIds(ctx context.Context, ids []int64, data entity.KnowledgeChunks) error {
	model := dao.KnowledgeChunks.Ctx(ctx).WhereIn("id", ids)
	if data.Content != "" {
		model = model.Data("content", data.Content)
	}
	if data.Status != 0 {
		model = model.Data("status", data.Status)
	}
	_, err := model.Update()
	return err
}

// GetAllChunksByDocId gets all chunks by document id
func GetAllChunksByDocId(ctx context.Context, docId int64, fields ...string) (list []entity.KnowledgeChunks, err error) {
	model := dao.KnowledgeChunks.Ctx(ctx).Where("knowledge_doc_id", docId)
	if len(fields) > 0 {
		for _, field := range fields {
			model = model.Fields(field)
		}
	}
	err = model.Scan(&list)
	return
}
