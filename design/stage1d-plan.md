# Stage 1D 细化实施计划

## 1. 概述

Stage 1D 目标：修复 Indexer 后台写入路径中的错误吞没、QA 无界并发和 goroutine 入库止损缺陷，使失败状态可观测、并发可控、错误可传播。

**范围**：
- F09：`SaveChunksData` 部分写入失败仍返回 nil，调用方吞没错误并无条件标记 Active
- F08：`qa()` 为每个 Chunk 启无界协程，失败仅记日志，顶层始终返回 nil
- F07：后台 goroutine 脱离调用方 ctx，recover 不标 Failed，递归重试不检查取消

**实施顺序**：F09 → F08 → F07（底层先修，上层后修）

**前置条件**：
- Stage 1C-2 已验收（基线 `26e55ee2ea43daf703ed72da60425ff5b7980cba`）
- Stage 1B-3 注入边界（`syncIndexer`/`asyncIndexer`/`asyncWait`）已就位

**不修改**：依赖版本、数据库 schema、ES/Qdrant 镜像、已验收的 Retriever/SSE 生命周期。

**不调用**：真实模型或外部付费服务。

**明确不在范围内**：

| 事项 | 归属 |
|---|---|
| 持久化任务恢复系统 | Stage 4 |
| 会话模型 / SSE v2 协议 | Stage 5 |
| 重构已验收的 Retriever、Indexer 同步路径、SSE 生命周期 | 不动 |
| 连接真实模型或执行付费调用 | 禁止 |
| 模型超时配置化 | Stage 2 |
| OpenTelemetry 链路追踪 | Stage E |
| History context / Flush 边界 | 保留现状 |
| race 未补证子包（core/retriever、core/rerank） | 保留现状 |
| Qdrant 异步 indexer 路径 | 保留现状（已知限制） |
| QA 重试逻辑 | Stage 2+ |

---

## 2. 已确认技术事实

| # | 事实 | 证据来源 |
|---|---|---|
| 1 | GoFrame `Model.Scan(&struct)` 未找到记录返回 `sql.ErrNoRows` | gdb_type_record.go L48-55（GoFrame v2.9.0） |
| 2 | Go 1.23.1；`golang.org/x/sync` v0.15.0 已在模块图中（传递依赖），首次 import 需 `go mod tidy` | go.mod; `go list -m golang.org/x/sync` |
| 3 | `common.qaModel` 是无锁懒加载缓存，并发调用存在 race | chat_model.go L12-16, L97-115 |
| 4 | eino v0.6.0 Lambda 返回 error 时 Graph 中断，包装为 `NodeRunError`，可用 `errors.Is/As` 提取 | graph_run.go L440; error.go L62-77 |
| 5 | `asyncWait` 当前签名 `func()`，无 ctx；8 处测试注入点 | rag.go L40; indexer_test.go L112/153/196/251/291/325/366/407 |
| 6 | `cmd.go` 无显式 Rag.Shutdown hook；`s.Run()` 返回即 Func 结束 | cmd.go L50-51 |
| 7 | `Config` 无 QA 相关字段；配置装配需级联修改 4 个文件 | config.go; retriever.go L26-65 |
| 8 | `sonic.Marshal` 失败仅 continue，chunk 静默丢失 | indexer.go L166-170 |
| 9 | `SaveChunksData` update 路径写入 `chunk.Status`（永远为 0）会将已 Active 的 chunk 退回 Pending | chunks.go L33 |
| 10 | Qdrant 异步 indexer 走独立路径（`indexer_async.go` L67-84），当前计划不涉及 | indexer_async.go L67-84 |
| 11 | `New()` 已预热 chatModel（L75），同模式可预热 qaModel | rag.go L75 |
| 12 | `errgroup.WithContext` ctx 取消后 `Go()` **仍启动**新 goroutine（源码确认：errgroup.go L86-92 无 ctx 检查），取消传播依赖任务函数自行检查 `egCtx` | x/sync v0.15.0 errgroup.go 源码 |
| 13 | `Rag.conf` 是 `*config.Config` 具体 struct 指针（非接口），`SearchDocumentsByIDs` 是其方法 | rag.go; config.go |
| 14 | GoFrame `s.Run()` 收到 SIGTERM/SIGINT 后 graceful 关闭 HTTP 连接，然后**正常 return**（不调用 os.Exit），`s.Run()` 返回后的清理代码确实能执行 | GoFrame v2.9.0 ghttp_server.go 源码验证 |

---

## 3. F 编号核对表

### F09 — Chunk 写入错误传播

| 项目 | 内容 |
|---|---|
| **文件** | `server/internal/logic/knowledge/chunks.go` |
| **函数** | `SaveChunksData(ctx, documentsId, chunks) error` |
| **调用链** | `Index()` → goroutine → `prodAsyncIndexer.indexAsyncByDocsID()` → `Rag.indexAsyncByDocsID()` → `knowledge.SaveChunksData()` |
| **缺陷 1** | chunks.go L23-25：DB error 与 not-found 混同处理（都走 Insert 分支）；GoFrame Scan 不存在时返回 `sql.ErrNoRows`，应区分 |
| **缺陷 2** | chunks.go L33：update 路径写入 `"status": chunk.Status`，调用方构造的 chunk 零值=0=Pending，将已 Active 的 chunk 退回 Pending |
| **缺陷 3** | chunks.go L51：`UpdateDocumentsStatus(ctx, documentsId, status)` 返回值被丢弃 |
| **缺陷 4** | chunks.go L52：函数始终 `return nil`，即使循环内有 insert/update 失败 |
| **缺陷 5** | indexer.go L166-170：`sonic.Marshal(doc.MetaData)` 失败仅 continue，chunk 静默丢失，最终仍标 Active |
| **缺陷 6** | indexer.go L180-183：`SaveChunksData` 返回 error 仅记日志不 return，后续继续执行 |
| **缺陷 7** | indexer.go L194：无条件 `UpdateDocumentsStatus(ctx, req.DocumentsId, int(v1.StatusActive))`，覆盖 SaveChunksData 内设置的 Failed |
| **缺陷 8** | indexer.go L149：重试条件仅比较数量 `len(searchResp) < len(req.DocsIDs)`，未按 ID 集合核对缺失 |
| **触发条件** | 任意 chunk insert/update 失败；sonic.Marshal 失败；ES 部分文档未同步 |
| **修复后行为** | `SaveChunksData` 返回 `errors.Join` 聚合错误；`indexAsyncByDocsID` 传播错误并标 Failed |

### F08 — QA 有限并发和失败状态

| 项目 | 内容 |
|---|---|
| **文件** | `server/core/indexer/qa.go` |
| **函数** | `qa(ctx, docs) ([]*schema.Document, error)` |
| **调用链** | `Rag.IndexAsync()` → `idxerAsync.Invoke()` → eino Graph → Lambda Node `qa()` → `getQAContent()` → `common.GetQAModel()` → `cm.Generate()` |
| **缺陷 1** | qa.go L22-36：裸 `sync.WaitGroup`，每个 doc 一个 goroutine，无并发上限 |
| **缺陷 2** | qa.go L28-31：`getQAContent` 失败仅 `g.Log().Errorf` + return，不传播 |
| **缺陷 3** | qa.go L37：顶层始终 `return docs, nil`，调用方无法感知 QA 失败 |
| **缺陷 4** | 无 QA 配置开关，无法关闭 QA 生成 |
| **缺陷 5** | 无 `ctx.Err()` 检查，取消后 goroutine 继续调用模型 |
| **缺陷 6** | indexer_async.go L54-57：无条件写 `FieldQAContent` 到 ES，QA 关闭时写入空值 |
| **缺陷 7** | `common.GetQAModel` 无锁懒加载缓存，并发首次调用存在 race |
| **触发条件** | 模型调用失败（超时、限流、网络错误）；大量 chunk 同时 QA 导致 OOM/限流 |
| **修复后行为** | errgroup 有限并发；首错取消；Graph 中断传播 error；QA 可配置关闭 |

### F07 — goroutine 入库止损

| 项目 | 内容 |
|---|---|
| **文件** | `server/core/indexer.go`、`server/core/rag.go` |
| **函数** | `Rag.Index()` 内 goroutine、`Rag.indexAsyncByDocsID()`、新增 `Rag.Shutdown()` |
| **调用链** | Controller → `Rag.Index()` → goroutine → `prodAsyncIndexer.indexAsyncByDocsID()` → `Rag.indexAsyncByDocsID()` |
| **缺陷 1** | indexer.go L100：goroutine 使用 `gctx.New()` 脱离调用方 ctx，无法被取消/超时 |
| **缺陷 2** | indexer.go L102-106：recover 只打日志，不标记文档 Failed |
| **缺陷 3** | indexer.go L152：递归重试 `time.Sleep(time.Second)` 不检查 ctx，取消后继续重试 |
| **缺陷 4** | 无并发上限，每次 `Index()` 调用都启新 goroutine（goroutine 可无限增长） |
| **缺陷 5** | 无 shutdown 联动，进程退出时后台 goroutine 被强杀 |
| **缺陷 6** | indexer.go L116-118：失败仅记日志，不调用 markFailed |
| **缺陷 7** | prodAsyncIndexer L65：内部重新创建 `gctx.New()`，外层超时和 shutdown 无法作用于下游 |
| **触发条件** | 进程重启/关闭时后台任务丢失；ctx 取消后 goroutine 继续执行；panic 后文档卡在 Indexing 状态 |
| **修复后行为** | bgCtx 超时控制；准入信号量；markFailed 标记；Shutdown 等待+取消 |

---

## 4. 批次划分与实施顺序

### 批次总览

```
1D-1 (F09) ──→ 1D-2 (F08) ──→ 1D-3 (F07)
  chunks.go       qa.go           indexer.go
  indexer.go      async.go        rag.go
  错误传播        有限并发         止损+生命周期
```

| 批次 | 目标 | 依赖 |
|---|---|---|
| 1D-1 | `SaveChunksData` 正确聚合错误；`indexAsyncByDocsID` 传播错误而非吞没；仅成功时标 Active | 无 |
| 1D-2 | `qa()` 使用 errgroup 有限并发；失败返回 error；QA 可配置关闭；ctx 取消时停止 | 1D-1（QA 失败需通过 IndexAsync→indexAsyncByDocsID 传播） |
| 1D-3 | 后台 goroutine 使用可取消 bgCtx；**同步索引前预留后台容量**（准入/登记/所有权转移）；失败/panic/超时标记 Failed；Shutdown 收敛与超时保护（等待已准入的同步阶段与后台） | 1D-1+1D-2（需下游错误正确传播才能 markFailed） |

---

## 5. 统一依赖注入方式

### 设计原则

**禁止**：可变包级函数变量替换方案（如 `chunkInsertFn`、`chunkUpdateFn`、`getQAModelFn`）。

**替代**：
1. **内部 helper 接收依赖**：公开入口 `SaveChunksData(ctx, docID, chunks)` 签名不变，内部委派到 `saveChunks(ctx, docID, chunks, store, statusUpdater)` 私有函数，测试直接调用 helper 注入 fake
2. **构建时固定**：QA model 通过 `BuildIndexerAsync` 构建时传入工厂函数，运行期只读
3. **结构体字段注入**：`markFailed`/`docSearch` 通过 `Rag` struct 字段注入，构建时设置、运行期只读（与 1B-3 的 `syncIndexer`/`asyncIndexer` 同模式）
4. **公开入口委派同一生产逻辑**：测试不得复制实现

### chunkStore 接口（1D-1）

```go
// server/internal/logic/knowledge/chunks.go 同包内定义
type chunkStore interface {
    FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error)
    Insert(ctx context.Context, chunk entity.KnowledgeChunks) error
    Update(ctx context.Context, chunkID string, data map[string]interface{}) error
}

// 生产实现（直接使用 dao）
type daoChunkStore struct{}

func (d *daoChunkStore) FindByChunkID(ctx context.Context, chunkID string) (*entity.KnowledgeChunks, error) { ... }
func (d *daoChunkStore) Insert(ctx context.Context, chunk entity.KnowledgeChunks) error { ... }
func (d *daoChunkStore) Update(ctx context.Context, chunkID string, data map[string]interface{}) error { ... }
```

**公开签名不变**，内部委派到接收依赖的 helper：

```go
// 公开入口（签名不变，已有调用方无需修改）
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error {
    return saveChunks(ctx, documentsId, chunks, &daoChunkStore{}, &daoDocStatusUpdater{})
}

// 内部 helper（测试直接调用，注入 fake store 和 fake statusUpdater）
func saveChunks(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks, store chunkStore, statusUpdater docStatusUpdater) error { ... }
```

> **设计决策**：不通过重命名公开入口制造兼容包装。公开 API 保持不变，内部委派到 helper。测试中 `UpdateDocumentsStatus` 必须通过注入替换，不得仍调用真实实现。

### docStatusUpdater 接口（1D-1）

```go
// server/internal/logic/knowledge/chunks.go 同包内定义
type docStatusUpdater interface {
    UpdateDocumentsStatus(ctx context.Context, documentsId int64, status int) error
}

// 生产实现：daoDocStatusUpdater 委派到包级 UpdateDocumentsStatus
```

### docSearcher 接口（1D-1 core 编排层）

```go
// server/core/rag.go 中定义（与现有 syncIndexer/asyncIndexer 同模式）
type docSearcher interface {
    SearchDocumentsByIDs(ctx context.Context, knowledgeName string, docIDs []string, size int) ([]*schema.Document, error)
}

// Rag struct 新增字段：docSearch docSearcher
// New() 中初始化：rag.docSearch = conf（*config.Config 实现了该方法）
// indexAsyncByDocsID 中：x.conf.SearchDocumentsByIDs(...) → x.docSearch.SearchDocumentsByIDs(...)
```

**完整依赖覆盖表（core 编排层）**：

| 依赖 | 注入方式 | 字段/参数 |
|------|----------|----------|
| 检索文档 | Rag struct 字段（接口） | `docSearch docSearcher` |
| 保存 chunks | 内部 helper 接收 chunkStore 参数 | `saveChunks(ctx, docID, chunks, store)` |
| 调用后续索引 | 已有 Rag struct 字段 | `idxerAsync compose.Runnable` |
| 更新文档状态 | 内部 helper 接收 docStatusUpdater 参数 | 与 chunkStore 同层 |
| 重试等待 | 已有 Rag struct 字段 | `asyncWait func()` → 1D-3 改为 `func(ctx)` |

### QA model 工厂（1D-2）

```go
// BuildIndexerAsync 新增参数
func BuildIndexerAsync(ctx context.Context, conf *config.Config, qaModelFactory func(context.Context) (model.BaseChatModel, error)) (...)

// qa() 通过闭包捕获 factory（构建时固定，运行期只读）
```

### markFailed 注入（1D-3）

```go
// Rag struct 新增字段（构建时设置，运行期只读）
type Rag struct {
    // ...existing...
    markFailed func(ctx context.Context, docID int64, err error) // 失败标记
}

// New() 中设置默认实现
rag.markFailed = defaultMarkFailed
```

---

## 6. 批次 1D-1：F09 Chunk 写入错误传播

> **状态**：已通过独立复核（基线 `644642712bcb04e820e345598a98d99c78277175`，2026-10-17，源码及提交日志复核，未独立重跑测试）——在 ff0b872 实施基础上新增 indexAsyncByDocsID 返回集合完整性校验（非 nil/非预期 ID/重复 ID）与两项验收测试（knowledge 双 sentinel+update 分支、core 事件序列与非法返回三阶段未调用）；重试固定延迟与统一 markFailed 仍归 1D-3；不进入 1D-2/1D-3。

### 6.1 修改文件清单

| 文件 | 修改范围 | 变更描述 |
|---|---|---|
| `server/internal/logic/knowledge/chunks.go` | 主要重写 `SaveChunksData` | chunkStore/docStatusUpdater 接口；Scan 错误区分；聚合错误返回；update 路径不写 status；内部 helper 接收依赖 |
| `server/core/indexer.go` L139-196 | `indexAsyncByDocsID` | sonic.Marshal 失败聚合；SaveChunksData 失败 return；ID 集合核对；仅成功标 Active |
| `server/core/rag.go` | `Rag` struct、`New()` | 新增 `docSearcher` 接口定义 + `docSearch` 字段 + `New()` 中初始化 `rag.docSearch = conf` |
| `server/core/indexer_test.go` | 扩展已有测试 + 新增编排测试 | 通过 asyncIndexer 接口触发真实 indexAsyncByDocsID 路径 |
| `server/internal/logic/knowledge/chunks_test.go`（新建） | — | chunkStore/docStatusUpdater fake 驱动的行为测试 |

### 6.2 SaveChunksData 完整重写方案

#### Scan 失败 vs 未找到

```go
// 修改前（chunks.go L22-25）
var existing entity.KnowledgeChunks
err := dao.KnowledgeChunks.Ctx(ctx).Where("chunk_id", chunk.ChunkId).Scan(&existing)
if err == nil && existing.Id > 0 {
    // update 路径
} else {
    // insert 路径（DB error 也走这里 — BUG）
}

// 修改后
existing, err := store.FindByChunkID(ctx, chunk.ChunkId)
if err != nil && !errors.Is(err, sql.ErrNoRows) {
    // DB error：聚合到 errs，不尝试 Insert
    errs = append(errs, fmt.Errorf("find chunk %s: %w", chunk.ChunkId, err))
    continue
}
if err == nil && existing != nil && existing.Id > 0 {
    // 已存在 → update 路径
} else {
    // sql.ErrNoRows → insert 路径
}
```

#### update 路径不写 status（修复回归风险）

```go
// 修改前（chunks.go L29-34）
Data(g.Map{
    "knowledge_doc_id": chunk.KnowledgeDocId,
    "content":          chunk.Content,
    "ext":              chunk.Ext,
    "status":           chunk.Status,  // ← BUG：永远为 0，退回 Pending
})

// 修改后：移除 status 字段
Data(g.Map{
    "knowledge_doc_id": chunk.KnowledgeDocId,
    "content":          chunk.Content,
    "ext":              chunk.Ext,
    // status 不更新：避免零值覆盖已有状态
})
```

#### 聚合错误与边界行为

```go
// 内部 helper（公开入口 SaveChunksData 委派到此）
func saveChunks(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks, store chunkStore, statusUpdater docStatusUpdater) error {
    // 边界：空 chunks 直接返回 nil，不尝试状态更新
    if len(chunks) == 0 {
        return nil
    }

    // 边界：预取消（进入时 ctx 已 Done）快速返回，不尝试状态更新
    if ctx.Err() != nil {
        return ctx.Err()
    }

    var errs []error
    status := int(v1.StatusIndexing)

    for _, chunk := range chunks {
        // 循环中取消：停止处理后续 chunk
        if ctx.Err() != nil {
            errs = append(errs, ctx.Err())
            status = int(v1.StatusFailed)
            break
        }

        // FindByChunkID...
        // Insert 或 Update...
        // 失败时 errs = append(errs, fmt.Errorf("chunk %s: %w", chunk.ChunkId, err))
        // 并设 status = int(v1.StatusFailed)
    }

    // 状态写入（通过注入的 statusUpdater，使用 errors.Join 保留状态写入错误）
    if statusErr := statusUpdater.UpdateDocumentsStatus(ctx, documentsId, status); statusErr != nil {
        errs = append(errs, fmt.Errorf("update doc status %d: %w", documentsId, statusErr))
    }

    return errors.Join(errs...)
}
```

**取消时状态写入**：循环中取消后，尝试用**当前 ctx**写入 Failed 状态。不保证成功（ctx 已取消，DB driver 可能拒绝）。如果写入也失败，聚合到 errs 中返回。

**声明**：不保证用已取消 context 能写入 Failed。1D-3 将引入独立 cleanupCtx 解决此问题。

### 6.3 indexAsyncByDocsID 错误传播改造

#### sonic.Marshal 失败不再 continue

```go
// 修改前（indexer.go L166-170）
ext, err := sonic.Marshal(doc.MetaData)
if err != nil {
    g.Log().Errorf(ctx, "sonic.Marshal failed, err=%v", err)
    continue  // ← BUG：chunk 静默丢失，最终仍标 Active
}

// 修改后
ext, err := sonic.Marshal(doc.MetaData)
if err != nil {
    g.Log().Errorf(ctx, "sonic.Marshal failed for doc %s, err=%v", doc.ID, err)
    marshalErrs = append(marshalErrs, fmt.Errorf("marshal doc %s metadata: %w", doc.ID, err))
    continue  // 继续处理其他 doc，但最终不标 Active
}
```

如果 `marshalErrs` 非空，在所有 chunk 处理完成后 return 聚合错误，不调用 SaveChunksData/IndexAsync/UpdateDocumentsStatus(Active)。

#### 检索重试后按 ID 集合核对缺失

```go
// 修改前（indexer.go L149）
if len(searchResp) < len(req.DocsIDs) && req.try > 0 { ... }

// 修改后
expected := make(map[string]struct{}, len(req.DocsIDs))
for _, id := range req.DocsIDs {
    expected[id] = struct{}{}
}
actual := make(map[string]struct{}, len(searchResp))
for _, doc := range searchResp {
    actual[doc.ID] = struct{}{}
}
missing := make([]string, 0)
for id := range expected {
    if _, ok := actual[id]; !ok {
        missing = append(missing, id)
    }
}

if len(missing) > 0 && req.try > 0 {
    req.try--
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(time.Second):
    }
    return x.indexAsyncByDocsID(ctx, req)
}

// 重试耗尽仍有缺失
if len(missing) > 0 {
    return nil, fmt.Errorf("documents missing after retries: %v", missing)
}
```

**limit=1000 边界**：`SearchDocumentsByIDs(ctx, knowledgeName, docsIDs, 1000)` — 若 `len(req.DocsIDs) > 1000`，单次检索无法覆盖全部。此时禁止静默截断后标 Active；应返回 error 含超限信息。

#### SaveChunksData 失败时 return

```go
// 修改前（indexer.go L180-183）
if err = knowledge.SaveChunksData(ctx, req.DocumentsId, chunks); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID insert chunks failed, err=%v", err)
    // 不 return — BUG
}

// 修改后（公开签名不变，但失败时 return）
if err = knowledge.SaveChunksData(ctx, req.DocumentsId, chunks); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID insert chunks failed, err=%v", err)
    return nil, err  // 传播错误，不继续 IndexAsync
}
```

#### Active 的必要条件

仅当以下条件**全部满足**时才可标 Active：

| # | 条件 | 检查位置 |
|---|---|---|
| 1 | 所需文档完整（无缺失） | ID 集合核对通过 |
| 2 | 每个文档的元数据编码成功 | `len(marshalErrs) == 0` |
| 3 | chunk 保存成功 | `SaveChunksData` 返回 nil |
| 4 | 后续索引成功 | `IndexAsync` 返回 nil |
| 5 | Active 状态写入成功 | `UpdateDocumentsStatus` 返回 nil |

```go
// 修改后（indexer.go L194）
if err = knowledge.UpdateDocumentsStatus(ctx, req.DocumentsId, int(v1.StatusActive)); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID UpdateDocumentsStatus Active failed, err=%v", err)
    return nil, err
}
```

### 6.4 1D-1 编排测试设计

**关键要求**：core 编排测试必须调用实际 `indexAsyncByDocsID` 的生产编排，不能仅让 `fakeAsyncIndexer` 返回错误就声称验证了 SaveChunksData 失败会阻止后续调用。

**测试触达方式**：

```go
// 构建测试用 Rag，注入 fakeDocSearcher（实现 SearchDocumentsByIDs）和 fake chunkStore
// 使用 prodAsyncIndexer 指向真实 indexAsyncByDocsID
testRag := &Rag{
    docSearch:  &fakeDocSearcher{result: ..., err: nil},
    idxerAsync: &fakeRunnable{...},
    // ... 其他字段
}
testRag.asyncIndexer = &prodAsyncIndexer{rag: testRag}

// 通过 asyncIndexer.indexAsyncByDocsID 触发真实方法
err := testRag.asyncIndexer.indexAsyncByDocsID(ctx, "kb1", 42, []string{"id1","id2"})
```

**测试矩阵见 §9.1**。

### 6.5 1D-1 边界声明

1D-1 只保证错误传播和不误标 Active。统一的 Failed 标记（markFailed）归属 1D-3，1D-1 不提前宣称已完成 Failed 标记。

在 1D-1 中：
- `SaveChunksData` 失败 → 返回 error → `indexAsyncByDocsID` return error → 后台 goroutine 记日志（1D-1）
- 后台 goroutine 调用 `markFailed` 写 Failed 状态 → **1D-3 实现**

> **分界**：1D-1 保证错误正确传播到后台 goroutine；1D-3 负责后台 goroutine 根据错误调用 markFailed 写入 Failed 状态。

---

## 7. 批次 1D-2：F08 QA 有限并发和失败状态

> **状态**：已通过独立复核（基线 `d7618e5a092f0b29e6441e98e32d86075b199172`，源码及提交日志复核，未独立重跑测试）——基线 `575949abfde25ff0e5ffc68ed0211b92430f54a6`。本轮只修正三项：（一）提取包内共享 `buildAsyncGraph` 作为唯一 Graph 装配（QA 节点/分支/并发上限/边/Compile 仅一处并传播构图错误），`BuildIndexerAsync` 与 graph_test 共用，测试注入实现 `indexer.Indexer` 的 fake（删除复制的 buildTestGraph）；（二）`documentToFields` 为唯一完整转换 helper（内部序列化 getExtData、Extra 写新 map 不写回输入、失败返回可追踪错误），ES 回调仅委派；（三）`qaModelCache` 实例级锁/缓存，`GetQAModel` 委派 `qaDefaultCache`，删除包级 qaModel/qaModelMu/qaModelReady，测试用独立实例验证重试/缓存/并发只建一次。验证：build/vet/gofmt exit 0，indexer+common 64 pass，`go test -p 1 ./...` 13 包 ok；indexer/common 的 -race 因 Windows `0xc0000139` 无法启动（运行异常、race 未完成、根因待确认）。保留 A-4 检索行为待验证与历史 race 未补证边界；不实施 1D-3。

### 7.1 修改文件清单

| 文件 | 修改范围 | 变更描述 |
|---|---|---|
| `server/core/indexer/qa.go` | `qa`、`getQAContent` | errgroup 有限并发；首错取消；ctx 检查；nil/重复防护 |
| `server/core/indexer/async.go` | `BuildIndexerAsync` | 接收 qaModelFactory 参数；QA 节点构建时绑定 |
| `server/core/indexer/indexer_async.go` L54-57 | `DocumentToFields` | 条件性写入 QA 字段（仅当值非空时） |
| `server/core/config/config.go` | `Config` struct | 新增 `QAEnabled *bool` + `QAMaxConcurrent int` |
| `server/core/rag.go` | `New()` | 预热 qaModel；传递 qaModelFactory 给 BuildIndexerAsync |
| `server/internal/logic/rag/retriever.go` | `RagConfig` + `defaultRagFactory.Create` | 级联 QA 配置字段 |
| `server/manifest/config/config_demo.yaml` | — | 新增 qa.enabled / qa.maxConcurrent 示例 |
| `server/manifest/config/config_qd_demo.yaml` | — | 同上 |
| `server/core/indexer/qa_test.go`（新建） | — | 离线行为测试 |
| `server/go.mod` / `server/go.sum` | — | `go mod tidy` 补充 x/sync 为直接依赖 |

### 7.2 errgroup 依赖声明

`errgroup.SetLimit` 需要 `golang.org/x/sync` 包。

- 当前 go.mod 中 x/sync **未列为直接依赖**
- 模块图已选定 v0.15.0（传递依赖）
- 首次 import 后需 `go mod tidy` 补充 go.mod/go.sum
- **显式声明**：新增直接 require 但版本沿用已选定的 v0.15.0，不引入新版本

### 7.3 配置装配路径

```yaml
# config_demo.yaml 新增
qa:
  enabled: true        # 可选，缺失时默认 true（向后兼容）
  maxConcurrent: 4     # 可选，缺失时默认 4，范围 1-32
```

**配置缺失默认行为**：

| 配置状态 | 行为 |
|---|---|
| 字段缺失（nil） | 默认启用 QA（`QAEnabled == nil` → true），向后兼容 |
| 显式 `qa_enabled: false` | 关闭 QA |
| `qaMaxConcurrent` 缺失或 ≤0 | 默认 4 |
| `qaMaxConcurrent` > 32 | 钳位到 32 |

**级联修改路径**：
1. `core/config/config.go` — Config struct 新增 `QAEnabled *bool` + `QAMaxConcurrent int`
2. `internal/logic/rag/retriever.go` — RagConfig 新增对应字段 + `defaultRagFactory.Create` 传递
3. `manifest/config/config_demo.yaml` + `config_qd_demo.yaml` — 示例配置
4. `readRagConfig()` — 读取 `qa.enabled` 和 `qa.maxConcurrent`（缺失不报错）

### 7.4 GetQAModel 并发安全修复（1D-2 已实现）

**问题（历史）**：`common.qaModel` 曾为无锁懒加载缓存，并发调用存在 race。

**现行方案（已实现，非 sync.Once）**：采用**实例级 `qaModelCache`**（见 `server/core/common/chat_model.go`）：结构体自持 `mu sync.Mutex`+`model`+`ready`+注入的 `readCfg`/`build`；`get(ctx, cfg)` 失败不发布缓存（允许重试）、仅初始化成功才置 `ready`；`GetQAModel` 委派生产默认实例 `qaDefaultCache`。早期“对 GetQAModel 加 `sync.Once`”仅为历史方案，**不再作为现行决策**（sync.Once 无法失败重试且会发布 (nil,nil)）。

**条件预热**：预热与开关不影响启动；缺 QA 配置不阻止启动（QAEnabled=false 或功能禁用时正常）。

**测试注入**：`qaModelCache` 可由测试创建独立实例并注入 fake reader/builder（见 `chat_model_test.go`）；后台模型经 `BuildIndexerAsync`/`buildAsyncGraph` 传入的 `qaModelFactory` 注入 fake model（**无 `WithQAConfig` 这样的包级函数替换**，QA 开关/并发经 `conf.QAEnabled`/`conf.QAMaxConcurrent` 传递）。

### 7.5 qa() 完整重写方案

```go
func qa(ctx context.Context, docs []*schema.Document) (output []*schema.Document, err error) {
    // 1. 提取 knowledgeName（已有逻辑）
    var knowledgeName string
    if value, ok := ctx.Value(coretypes.KnowledgeName).(string); ok {
        knowledgeName = value
    } else {
        return nil, fmt.Errorf("必须提供知识库名称")
    }

    // 2. 提取 QA 配置
    enabled, maxConcurrent := coretypes.GetQAConfig(ctx)
    if !enabled {
        return docs, nil  // QA 关闭：不生成新内容，已有 FieldQAContent 保持不变
    }

    // 3. 空 docs 快速返回
    if len(docs) == 0 {
        return docs, nil
    }

    // 4. 去重检查：防止同一 doc 指针出现多次导致并发写同一 map
    seen := make(map[*schema.Document]struct{}, len(docs))
    for _, d := range docs {
        if _, dup := seen[d]; dup {
            return nil, fmt.Errorf("qa: duplicate doc pointer %s in input", d.ID)
        }
        seen[d] = struct{}{}
    }

    // 5. errgroup 有限并发
    eg, egCtx := errgroup.WithContext(ctx)
    eg.SetLimit(maxConcurrent)

    // 结果写入独立数组，避免并发写入共享 MetaData map
    results := make([]string, len(docs))

    for i, doc := range docs {
        idx := i
        d := doc  // capture
        eg.Go(func() error {
            // nil 防护（闭包最前面，在任何 d.ID 访问之前）
            if d == nil || d.MetaData == nil {
                return fmt.Errorf("qa: nil doc or nil MetaData")
            }
            // 已有非空 QAContent 的 doc 跳过生成（不覆盖）
            if existing, ok := d.MetaData[coretypes.FieldQAContent].(string); ok && existing != "" {
                results[idx] = existing
                return nil
            }
            // ctx 取消检查
            if egCtx.Err() != nil {
                return egCtx.Err()
            }
            qaContent, err := getQAContent(egCtx, d, knowledgeName)
            if err != nil {
                return fmt.Errorf("qa failed for doc %s: %w", d.ID, err)
            }
            results[idx] = qaContent
            return nil
        })
    }

    // 提交循环中每次 Go() 调用前检查 egCtx，避免无意义地启动立即返回的 goroutine
    // （注：errgroup.Go() 在 ctx 取消后仍会启动 goroutine，Go() 内无 ctx 检查）

    if err := eg.Wait(); err != nil {
        return nil, err  // 首错取消，返回 error
    }

    // Wait 完成后统一写回 MetaData（避免并发写入共享 map）
    for i, doc := range docs {
        if results[i] != "" {
            doc.MetaData[coretypes.FieldQAContent] = results[i]
        }
    }

    return docs, nil
}
```

> **MetaData 并发写入防护（已决策）**：每个 goroutine 的结果写入独立的 `results[i]`，在所有 goroutine 完成后（Wait 之后）统一写回到各 `doc.MetaData`。这避免了不同 `*schema.Document` 共享同一 MetaData map（例如浅拷贝）时的并发写入 race。

### 7.6 并发限制语义

明确：`maxConcurrent` 是**单次 `qa()` 调用**的并发上限（即单次 Index 请求内 chunk QA 的并行度），不是服务级全局限流。

理由：每次 `qa()` 调用创建独立的 errgroup 实例，`eg.SetLimit(maxConcurrent)` 仅限制该实例内的并发 goroutine 数。

### 7.7 QA 关闭语义

| 场景 | 行为 |
|---|---|
| QA 关闭 + doc 无 FieldQAContent | `qa()` 直接返回 `docs, nil`；DocumentToFields 跳过 QA 字段写入 |
| QA 关闭 + doc 已有 FieldQAContent（重跑场景） | 字段保持不变，不会自动消失；DocumentToFields 条件性写入（值非空则写入） |
| QA 开启 + 生成成功 | 正常写入 MetaData |
| QA 开启 + 生成失败 | errgroup 首错取消，返回 error，Graph 中断 |

### 7.8 DocumentToFields 条件性写入

```go
// 修改前（indexer_async.go L54-57）
coretypes.FieldQAContent: {
    Value:    doc.MetaData[coretypes.FieldQAContent],
    EmbedKey: coretypes.FieldQAContentVector,
},

// 修改后：条件性写入（仅当值非空时）
fields := map[string]es8.FieldValue{
    coretypes.FieldContent: {Value: doc.Content, EmbedKey: coretypes.FieldContentVector},
    coretypes.FieldExtra:   {Value: doc.MetaData[coretypes.FieldExtra]},
    coretypes.KnowledgeName: {Value: knowledgeName},
}
if qaVal, ok := doc.MetaData[coretypes.FieldQAContent].(string); ok && qaVal != "" {
    fields[coretypes.FieldQAContent] = es8.FieldValue{Value: qaVal, EmbedKey: coretypes.FieldQAContentVector}
}
return fields, nil
```

### 7.9 Retriever 兼容性声明

`rag.go` L70-73 的 `qaRtrvr` 依赖 `FieldQAContentVector` 字段。

- QA 关闭时 ES 中 `FieldQAContentVector` 字段可能不存在或为空
- `qaRtrvr` 对该字段的 kNN 检索行为**待验证**（可能返回空结果，也可能返回错误，取决于 ES 版本和索引 mapping）
- 1D-2 实施时需确认实际行为
- **显式记录**：QA 关闭时 `qaRtrvr` 路径的实际行为待验证，不影响主检索路径（`rtrvr`）
- 主检索路径使用 `FieldContentVector`，与 QA 无关

### 7.10 首错取消语义

使用 `errgroup.WithContext`：
1. 首个非 nil error 取消 derived ctx（`egCtx`）
2. 已启动的 goroutine 通过 `egCtx.Done()` 感知取消
3. `cm.Generate(egCtx, msgs)` 接收该 ctx，LLM HTTP 调用应响应取消（取决于 provider 实现）
4. `Wait()` 等待所有已启动任务退出后返回首个错误
5. `errgroup.Go()` 在 ctx 取消后**仍会启动**新 goroutine（源码确认：Go() 内无 ctx.Err() 检查）。取消传播依赖每个任务函数入口检查 `egCtx.Err() != nil` 并提前返回。提交循环也应在每次 `Go()` 调用前检查 `egCtx.Err()`，避免无意义地启动立即返回的 goroutine

### 7.11 eino Graph 错误中断语义

**已验证**（eino v0.6.0 graph_run.go L440）：
- Lambda 节点返回非 nil error 时 Graph 立即中断
- 后续 Indexer 节点不执行
- `runnable.Invoke` 返回包装后的 `NodeRunError`
- 测试断言用 `errors.Is/As` 提取原始错误

**离线真实 Graph 测试**：使用 `BuildIndexerAsync` 构建真实 Graph（注入 fake QA model factory 和 fake indexer conf），验证 QA 失败时 Indexer 节点不被调用。

### 7.12 Qdrant 路径已知限制

`indexer_async.go` L67-84 Qdrant indexer 走独立路径（`NewQdrantIndexer`），其 DocumentToFields 逻辑独立于 ES 路径。

**本批仅处理 ES 路径**，Qdrant 保留现状。后续若需统一，在 Stage 2+ 处理。

---

## 8. 批次 1D-3：F07 goroutine 入库止损与生命周期

> **状态**：A+B 实施完成，待独立复核（2026-10-18）；C 待实施

### 8.1 修改文件清单

| 文件 | 修改范围 | 变更描述 |
|---|---|---|
| `server/core/indexer.go` | `Index()` 同步前准入、后台 goroutine、`indexAsyncByDocsID()` | 同步前预留容量+登记+所有权转移；bgCtx/syncCtx 传递；markFailed；重试 ctx 检查 |
| `server/core/rag.go` | `Rag` struct、`New()`、新增 `Shutdown()` | bgWG/bgSem/bgRootCtx/bgRootCancel/mu/closed/shutdownOnce/done/markFailed 字段 |
| `server/core/indexer_test.go` | 已有 8 处 asyncWait + 新增测试 | asyncWait 签名变更；止损/准入/shutdown 测试 |
| `server/internal/cmd/cmd.go` | `s.Run()` 之后 | 仅 Shutdown 成功后逆序 Cleanup；超时保留依赖、返回可识别错误；不忽略 Cleanup 错误；避免 dao 重复关闭 |

### 8.2 asyncIndexer 接口传递 bgCtx

```go
// 修改前（indexer.go L44-47）
type asyncIndexer interface {
    indexAsyncByDocsID(knowledgeName string, documentsID int64, docsIDs []string) error
}

// 修改后
type asyncIndexer interface {
    indexAsyncByDocsID(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error
}
```

**prodAsyncIndexer 修改**：

```go
// 修改前（indexer.go L64-73）
func (p *prodAsyncIndexer) indexAsyncByDocsID(knowledgeName string, documentsID int64, docsIDs []string) error {
    ctxN := gctx.New()  // ← BUG：脱离外层超时和 shutdown
    _, err := p.rag.indexAsyncByDocsID(ctxN, &IndexAsyncByDocsIDReq{...})
    return err
}

// 修改后：接收并使用传入的 bgCtx
func (p *prodAsyncIndexer) indexAsyncByDocsID(ctx context.Context, knowledgeName string, documentsID int64, docsIDs []string) error {
    _, err := p.rag.indexAsyncByDocsID(ctx, &IndexAsyncByDocsIDReq{
        DocsIDs:       docsIDs,
        KnowledgeName: knowledgeName,
        DocumentsId:   documentsID,
        try:           esTryFindDoc,
    })
    return err
}
```

**删除**生产适配器内部重新创建 `gctx.New()` 的设计。

### 8.3 后台 context 语义

后台任务在 HTTP 返回后继续执行属于**既有异步产品语义**（fire-and-forget indexing）。

**服务级后台 root context**：`Rag` 新增 `bgRootCtx context.Context` + `bgRootCancel context.CancelFunc`（在 `New()` 中创建：`context.WithCancel(context.Background())`）。每个任务的 timeout ctx 从 bgRootCtx 派生：`bgCtx, bgTaskCancel := context.WithTimeout(x.bgRootCtx, x.taskTimeout)`。Shutdown 调用 `x.bgRootCancel()` → 自动取消所有派生的 bgCtx → 所有任务感知取消。这是真实的父子 context 链。goroutine 内 `defer bgTaskCancel()` 释放资源。

### 8.4 任务准入策略（同步索引前预留后台容量）

**固定准入契约**：准入发生在**同步索引之前**。在同一把锁下依次：检查 `closed` → 非阻塞获取后台容量 → 登记本次已准入工作（`bgWG.Add(1)`），三者原子完成；只有准入成功才执行同步索引。这样后台满载或正在 Shutdown 时，本次 `Index` **不产生任何同步索引副作用**。

```go
x.mu.Lock()
if x.closed {
    x.mu.Unlock()
    return nil, ErrShuttingDown            // 可识别错误；不调用同步索引、不启动后台
}
select {
case x.bgSem <- struct{}{}:                 // 非阻塞获取容量成功
    x.bgWG.Add(1)                          // 登记本次已准入工作（同步+后台共用一次计数）
    x.mu.Unlock()
default:
    x.mu.Unlock()
    return nil, ErrBackgroundFull          // 满载：可识别错误；不启动同步索引、不启动后台
}

// 已准入：同步阶段使用从 bgRootCtx 派生的 syncCtx，使 Shutdown 取消可被同步阶段观察
syncCtx, syncCancel := context.WithCancel(x.bgRootCtx)
ids, err := x.syncIndexer.invoke(syncCtx, req.KnowledgeName, document.Source{URI: req.URI})
syncCancel()
if err != nil {
    // 同步失败：释放容量与登记，不启动后台
    <-x.bgSem
    x.bgWG.Done()
    return nil, err
}

// 同步成功：把容量与登记的唯一所有权转移给后台 goroutine（只释放一次）
go func() {
    defer x.bgWG.Done()
    defer func() { <-x.bgSem }()
    ... 后台体（8.10）...
}()
return ids, nil
```

**所有权“只释放一次”**：容量 slot 与 `bgWG` 计数要么由**同步失败路径**释放（后台不启动），要么在同步成功后作为唯一所有权**转移给后台 goroutine** 由其 defer 释放一次；两条路径互斥，绝不重复释放。

**拒绝语义（准确）**：
- 准入只保证**本次 `Index` 不执行同步索引、不启动后台**，并返回可识别错误（`ErrShuttingDown` / `ErrBackgroundFull`），不再返回“ids + nil”。
- 拒绝**不承诺撤销上游已创建的文档记录**：文档记录由 controller 在调用 `Index` 之前以 `StatusPending` 创建（`server/internal/controller/rag/rag_v1_indexer.go:81`），拒绝不会回滚或删除它。
- **修正历史表述**：`Rag.Index` 同步路径**不写入文档状态**；`StatusIndexing` 由 `SaveChunksData` 在**后台异步阶段**（`indexAsyncByDocsID` 步骤5，`server/internal/logic/knowledge/chunks.go:90/130`）写入，正是准入要门控的阶段。因此被拒绝/准入失败的文档停留在 controller 预设的 **Pending**，而非“同步阶段设置的 Indexing”。删除“SaveChunksData 已在同步阶段设置 Indexing”这类无源码支持的表述。

**同步阶段如何观察服务停止**：已准入的同步阶段计入 `bgWG`，`Shutdown` 的 `bgWG.Wait()` 必然等待它完成，不会漏等；同步阶段使用从 `bgRootCtx` 派生的 `syncCtx`，`bgRootCancel()` 后 in-flight 同步索引可感知取消（其对取消的实际响应取决于下游 Loader/Indexer 是否尊重 ctx，属未验证项，见 8.15）。**不引入排队 goroutine，也不引入持久化任务系统**。

**信号量配置**：`bgSem chan struct{}`，容量 = `MaxBackgroundTasks`（配置化，默认 8，见 8.14）。

### 8.5 Shutdown 实施

**明确纳入 1D-3**，不作为待确认事项。

```go
// Rag struct 新增字段
type Rag struct {
    // ...existing...
    mu           sync.Mutex           // 保护 closed 标记 + bgWG.Add 在同一临界区
    closed       bool                 // 关闭标记
    bgWG         sync.WaitGroup
    bgSem        chan struct{}
    bgRootCtx    context.Context      // 服务级后台 root context
    bgRootCancel context.CancelFunc   // 取消所有派生的后台任务
    shutdownOnce sync.Once            // 保护“停止+取消”动作只执行一次
    done         chan struct{}         // bgWG.Wait() 返回后 close
    taskTimeout  time.Duration        // 默认 5min
    markFailed   func(ctx context.Context, docID int64, err error)
}
```

**Shutdown 方法**：

```go
func (x *Rag) Shutdown(ctx context.Context) error {
    x.shutdownOnce.Do(func() {
        x.mu.Lock()
        x.closed = true
        x.mu.Unlock()
        x.bgRootCancel()          // 取消所有派生 ctx（同步阶段 + 后台阶段）
        go func() { x.bgWG.Wait(); close(x.done) }()
    })
    // 每个调用都等待同一个 done，或自身 ctx 超时
    select {
    case <-x.done:
        return nil                 // 全部已准入工作（含仍在同步阶段的）收敛完成
    case <-ctx.Done():
        return fmt.Errorf("rag shutdown: tasks not converged within deadline: %w", ctx.Err())
    }
}
```

**超时策略（修正后）**：
- `Shutdown` 只有在返回 `nil`（`<-x.done`）后才表示可安全逆序清理依赖资源。
- **超时**（返回 error）时：未退出任务仍可能使用 `dao`/ES/Qdrant，调用方**必须保留这些依赖、不清空服务引用、不报告正常清理成功**，向 `cmd` 返回可识别错误并记录“未收敛”。
- 进程最终退出时由**操作系统回收**连接/句柄，**不称为“优雅关闭完成”**。
- 后续再次调用 `Shutdown` 会**继续等待同一个 `done`**（Once 只保证 stop/cancel/wait 动作执行一次，但每个调用都各自 select `done`），任务真正结束后后续调用可返回 `nil` 并完成清理。
- **不声称已强制终止 goroutine**（Go 无法强杀 goroutine）。

> **语义修正**：首次 `Shutdown` 超时后，后续调用不能仅因 Once 已执行就返回 nil — 它们仍然等待 done 或自己的 ctx 超时。

### 8.6 Add/Wait 竞态防护

`mu` 保护的临界区从“启动后台前”前移到“**同步索引前**”：

- **准入流程**：`mu.Lock() → 若 closed 拒绝 → 非阻塞取 bgSem → bgWG.Add(1) 登记已准入 → mu.Unlock()`，随后才执行同步索引
- **Shutdown 流程**：`mu.Lock() → closed=true → mu.Unlock() → bgRootCancel() → go(bgWG.Wait(); close done)`
- 这保证 `closed` 置位后不再有新的 Add，且 `bgWG.Wait()` 覆盖“已准入但仍在同步阶段”的调用（不漏等）
- `bgWG.Done()` / `<-bgSem` 只在唯一所有者（同步失败路径 或 后台 goroutine）处执行**一次**

### 8.7 cmd.go 接线（仅 Shutdown 成功后才逆序清理）

**GoFrame v2.9.0 停止语义（源码证据）**：`Server.Run()`（锁定版本 `github.com/gogf/gf/v2 v2.9.0`，`net/ghttp/ghttp_server.go` L445-460）在 `Start()` 后 `go handleProcessSignal()`，随后 **阻塞于 `<-s.closeChan`**（L456）；被唤醒后调用 `_ = s.Shutdown()`（L459）再 **正常 return**（不 `os.Exit`）。因此 `s.Run()` 返回后的代码确实执行。closeChan 由信号/graceful 处理关闭，其 **具体信号集合与等待在途连接的超时顺序未逐一核实**（见 8.15）。

```go
s.Run()

shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
defer cancel()
var cleanupErrs []error
if ragSvr := ragic.GetRagSvr(); ragSvr != nil {
    if err := ragSvr.Shutdown(shutdownCtx); err != nil {
        // 超时未收敛：保留任务仍依赖的资源，不清空服务引用、不做逆序 Cleanup
        g.Log().Errorf(ctx, "Rag.Shutdown incomplete, skip resource cleanup: %v", err)
        return fmt.Errorf("rag shutdown not converged, skipping graceful cleanup: %w", err)
    }
}
// 仅在 Shutdown 成功后执行逆序清理；Cleanup 错误不得忽略
for i := len(steps) - 1; i >= 0; i-- {
    if steps[i].Cleanup != nil {
        if cerr := steps[i].Cleanup(); cerr != nil {
            g.Log().Errorf(ctx, "cleanup step %d failed: %v", i, cerr)
            cleanupErrs = append(cleanupErrs, cerr)
        }
    }
}
return errors.Join(cleanupErrs...)
```

**调用边界（避免 Reset 绕过运行期收敛）**：
- **正常退出**（HTTP 已服务过）：必须经 `Shutdown()` 收敛后才逆序 Cleanup；`Shutdown` 超时则**不清理**、返回可识别错误。
- **初始化失败清理**：服务尚未开始、无已准入任务时，走 `Reset()`/`Close()` 级联清理（1A 既有契约），无需也不应调用 `Shutdown()`。
- **不得用直接 `Reset()` 绕过运行期收敛**：`Reset()` 只清空服务引用、不等待 `bgWG`；运行期退出必须走 `Shutdown()`，否则与未退出任务竞争。
- **`Cleanup` 返回的 error 不得忽略**（聚合返回）。
- **避免 `dao` 被重复关闭**：`dao.Close()` 需幂等（已关闭再调用返回 nil），且 `Shutdown` 超时路径不得调用 `dao.Close()`；清理链中 `dao.Close()` 至多一次。
- `shutdownBudget` 仅为调用预算（如 10s），不保证底层一定按时返回。

**关闭顺序**：`s.Run()` 返回（GoFrame 已 graceful 关闭 HTTP）→ `Rag.Shutdown` 收敛后台 → （仅成功时）逆序 Cleanup → 关闭依赖资源（dao.Close 等）

### 8.8 可取消的初始延迟和重试等待

```go
// 修改前（rag.go L40）
asyncWait func()

// 修改后
asyncWait func(ctx context.Context)
```

**生产实现**：

```go
func(ctx context.Context) {
    select {
    case <-time.After(time.Second):
    case <-ctx.Done():
    }
}
```

**递归重试中的 Sleep**（indexer.go L152）同样替换：

```go
// 修改前
time.Sleep(time.Second)

// 修改后
select {
case <-ctx.Done():
    return nil, ctx.Err()
case <-time.After(time.Second):
}
```

**测试影响**：`indexer_test.go` 中 8 处 `asyncWait` 注入需同步修改签名（L112、L153、L196、L251、L291、L325、L366、L407）。

### 8.9 Failed 状态写入的 context

失败/panic/超时后写 Failed 状态，**不能使用已取消的 bgCtx**（DB 操作可能失败）。

```go
// markFailed 默认实现
func defaultMarkFailed(ctx context.Context, docID int64, err error) {
    // ctx 此处应为 cleanupCtx，不是 bgCtx
    g.Log().Errorf(ctx, "background index failed for document %d: %v", docID, err)
    if statusErr := knowledge.UpdateDocumentsStatus(ctx, docID, int(v1.StatusFailed)); statusErr != nil {
        g.Log().Errorf(ctx, "markFailed UpdateDocumentsStatus error: %v", statusErr)
    }
}
```

**goroutine 内使用独立 cleanupCtx**：

```go
// 在需要写 Failed 时
cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cleanupCancel()
x.markFailed(cleanupCtx, documentsID, localErr)
```

**核实**：`UpdateDocumentsStatus(ctx, id, status)` 内部 `dao.KnowledgeDocuments.Ctx(ctx)` 传递 ctx 给 DB driver — 使用独立 cleanupCtx 可确保不受 bgCtx 取消影响。

**如果 cleanupCtx 内写入也失败**：仅记录日志，不再重试。

### 8.10 recover 覆盖与所有权

```go
go func() {
    defer x.bgWG.Done()
    defer func() { <-x.bgSem }()  // 释放信号量 slot

    // recover 覆盖整个后台任务函数（已在 1B-3 实现，本批增强）
    defer func() {
        if e := recover(); e != nil {
            cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cleanupCancel()
            x.markFailed(cleanupCtx, documentsID, fmt.Errorf("panic: %v", e))
        }
    }()

    // 等待 ES 同步
    if waitFn != nil {
        waitFn(bgCtx)
    } else {
        select {
        case <-time.After(time.Second):
        case <-bgCtx.Done():
        }
    }
    if bgCtx.Err() != nil {
        cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cleanupCancel()
        x.markFailed(cleanupCtx, documentsID, bgCtx.Err())
        return
    }

    // 执行异步索引
    localErr := asyncIdx.indexAsyncByDocsID(bgCtx, knowledgeName, documentsID, docsIDs)
    if localErr != nil {
        cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cleanupCancel()
        x.markFailed(cleanupCtx, documentsID, localErr)
    }
}()
```

**所有权总结**：

| 角色 | 实体 |
|---|---|
| 预留/登记 | `Rag.Index` 同步前：mu 下 closed 检查 + 非阻塞取 bgSem + `bgWG.Add(1)` |
| 启动者 | `Rag.Index` 同步成功后才 `go func` |
| 取消者 | `Rag.Shutdown` 调用 bgRootCancel（同步阶段+后台阶段都取消） |
| 等待者 | `Rag.Shutdown` 调用 bgWG.Wait（覆盖已准入的同步阶段与后台） |
| slot/Done 释放者 | **唯一所有者**：同步失败路径释放一次，或转移后由后台 goroutine defer 释放一次（互斥，不重复） |
| Failed 写入者 | 后台 goroutine 自身（使用独立 cleanupCtx） |

### 8.11 清理顺序

**约束**：先停止并收敛后台任务，再关闭任务依赖的资源。

| 顺序 | 资源 | 操作 | 说明 |
|---|---|---|---|
| 1 | 后台任务 | `Rag.Shutdown(ctx)` | 停止准入 + 取消 bgRootCtx + 等待 bgWG |
| 2 | 逆序 Cleanup | `steps[i].Cleanup()` 逆序执行 | 已有 steps 链逆序关闭 |
| 3 | MySQL | `dao.Close()` | 关闭数据库连接 |
| 4 | ES client | 随进程退出释放 | 当前无显式 Close（已知限制） |
| 5 | Qdrant client | 随进程退出释放 | 当前无显式 Close（已知限制） |

**说明**：ES/Qdrant client 当前无显式 Close 接口，随进程退出自然释放。Stage 4 可引入完整 graceful shutdown。

### 8.12 清理 Context

markFailed 使用独立且有期限的 `cleanupCtx`，不受 bgCtx 取消影响：

```go
cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cleanupCancel()
x.markFailed(cleanupCtx, documentsID, localErr)
```

- **超时时长**：5 秒仅为本次 markFailed 调用的**时间预算**，不保证底层 DB/驱动一定按时返回，也不等于“足以完成 DB 写入”
- **来源**：`context.Background()` 派生，独立于 bgRootCtx 和请求 ctx
- **错误处理**：cleanupCtx 内写入失败仅记录日志，不重试
- **保留原始错误信息**：`markFailed` 接收的 `err` 参数包含原始错误链（panic/超时/业务错误），日志中记录完整信息

### 8.13 分批实施计划

所有 1D-3 字段均不存在，需全新引入；asyncIndexer 接口签名变更确定；cmd.go 当前无清理逻辑。**按“可独立验收”重划批次：准入、登记、所有权转移与 Shutdown 核心必须同批落地并可测，避免 A 批测试 B 批才实现的行为。**

#### 批次 1D-3-A：准入契约 + 所有权转移 + Shutdown 核心（同一可验证批次）

**修改文件**：

| 文件 | 变更 |
|---|---|
| `server/core/rag.go` | Rag struct 新增 bgRootCtx/bgRootCancel/bgWG/bgSem/mu/closed/shutdownOnce/done/taskTimeout/markFailed；New() 创建 bgRootCtx、初始化 bgSem=make(cap=MaxBackgroundTasks)、done、taskTimeout、markFailed 默认；定义 `ErrShuttingDown`/`ErrBackgroundFull` |
| `server/core/indexer.go` | Index() 改为**同步索引前**预留容量+登记（closed/sem/Add 同锁）；同步失败释放容量+登记；同步成功转移所有权给后台（只释放一次）；asyncIndexer 接口新增 ctx 参数、asyncWait 签名 `func(ctx)`；Shutdown()（closed→bgRootCancel→go(bgWG.Wait;close done)，select done/ctx） |
| `server/core/indexer.go` | prodAsyncIndexer 使用传入 ctx 替代 gctx.New() |
| `server/core/indexer_test.go` | 8 处 asyncWait 注入签名同步；新增 A 批测试（见 9.3 A 组） |

**真实调用链**：`Rag.Index`（同步前预留/登记 → 同步索引 → 转移所有权/`go` 后台）→ `Rag.Shutdown`（closed+取消+等待 done）。

**验收条件**：build/vet exit 0；A 组测试（准入拒绝返回可识别错误且无同步副作用、放行、所有权只释放一次、Shutdown 等待同步+后台、幂等、后续等待同一 done）通过；`go test -race ./core -count=1` exit 0。

**协调回滚**：A 批引入接口/签名变更，回滚需同步恢复 asyncIndexer/asyncWait 接口、Rag 字段与所有 fake；A 是 B/C 的前置。

#### 批次 1D-3-B：失败 / panic / 取消 与 markFailed

**修改文件**：

| 文件 | 变更 |
|---|---|
| `server/core/indexer.go` | 后台体：defer Done/释放 sem/defer recover+markFailed；bgCtx 超时或取消 → markFailed(cleanupCtx,...)；递归重试 Sleep 改 `select ctx.Done/time.After` |
| `server/core/rag.go` | markFailed 默认实现 defaultMarkFailed（独立 cleanupCtx，5s 为调用预算） |
| `server/core/indexer_test.go` | 新增 B 组测试（见 9.3 B 组） |

**真实调用链**：后台 goroutine → indexAsyncByDocsID(bgCtx) → 失败/panic → markFailed(cleanupCtx)。

**验收条件**：B 组测试通过；`-race ./core -count=10 -run TestIndex_` 无 DATA RACE。

**协调回滚**：B 依赖 A（字段/接口）；B 可独立回滚（不影响 A 的准入/Shutdown），但回滚 A 必须同时回滚 B。

#### 批次 1D-3-C：cmd 接线与 Shutdown 收敛协调

**修改文件**：

| 文件 | 变更 |
|---|---|
| `server/internal/cmd/cmd.go` | s.Run() 返回后：Shutdown → 仅成功时逆序 Cleanup（Cleanup 错误聚合返回）；超时则保留依赖、返回可识别错误；避免 dao 重复关闭 |
| `server/core/indexer_test.go` | 新增 C 组 Shutdown 超时协调测试（见 9.3 C 组） |

**真实调用链**：`cmd.s.Run()` 返回 → `Rag.Shutdown(shutdownCtx)` → 逆序 `steps[i].Cleanup()`/`dao.Close()`。

**验收条件**：C 组测试（超时不关闭依赖、后台结束后才逆序关闭、后续等待成功、原始错误与清理错误可识别）通过；`go build ./...`、`go vet ./...`、`go test -p 1 ./... -count=1` exit 0。

**协调回滚**：C 仅接线+测试，可独立回滚；回滚 C 不影响 A/B。回滚 A/B 前须先回滚依赖它们的 C。

#### 协调回滚链（按实际依赖，方向明确）

- **撤销前置批次前，先撤销依赖它的后续批次**：撤销 A 需先撤销 B、C；撤销 B 需先撤销 C（若 C 依赖 B 的 markFailed 行为）。
- **撤销后续批次不要求无条件撤销前置批次**：撤销 C 可独立进行，保留 A/B；撤销 B 可独立进行，保留 A。
- 每批独立提交，用 `git revert` 生成反向提交；revert 时检查同文件冲突并手动解决。

### 8.14 配置选择（固定）与已知限制

#### 固定配置：采用配置化（与 QAEnabled/QAMaxConcurrent 完全同模式）

**配置项、默认值与合法范围**：

| 配置键（gcfg） | 结构体字段 | 缺失默认 | 非法/越界处理 |
|---|---|---|---|
| `rag.maxBackgroundTasks` | `Config.MaxBackgroundTasks int` | 8 | 缺失或 `<=0` → 8；`>256` → 钳位 256 |
| `rag.backgroundTaskTimeoutMinutes` | `Config.BackgroundTaskTimeout time.Duration` | 5 分钟 | 缺失或 `<=0` → 5min；`>60` 分钟 → 钳位 60min |

**读取错误处理**：配置读取失败（如类型不可转换）与 `readQAConfig` 一致，**返回 error 向上传播**，不静默忽略；非法值（越界/非正）按上表钳位/回退默认并记录 WARN。

**配置装配路径（与现有 QA 配置一一对应）**：
1. `server/core/config/config.go` — `Config` 新增 `MaxBackgroundTasks int`、`BackgroundTaskTimeout time.Duration`；**`Config.Copy()`（config.go L35）必须同步复制这两个新字段**。
2. `server/internal/logic/rag/retriever.go` — `RagConfig` 新增同名字段；新增 `readLifecycleConfig(ctx, configGetter)`（镜像 `readQAConfig`）读取 `rag.*` 键，缺失回退默认、读取失败返回 error；`InitWith`（retriever.go L166）调用后填入 `RagConfig`；`defaultRagFactory.Create`（retriever.go L46）将 `cfg.MaxBackgroundTasks`/`BackgroundTaskTimeout` 传入 `config.Config`。
3. `manifest/config/config_demo.yaml` + `config_qd_demo.yaml` — 新增 `rag.maxBackgroundTasks`、`rag.backgroundTaskTimeoutMinutes` 示例（含注释）。
4. `Rag.New()` — 读取 `conf.MaxBackgroundTasks` 初始化 `bgSem=make(cap)`；`conf.BackgroundTaskTimeout` 赋 `taskTimeout`。

**对应测试**（均在**批次 1D-3-A**，因 A 引入容量/超时）：`readLifecycleConfig` 缺失/默认/越界钳位/读取错误（镜像 `TestReadQAConfig_*`）；`Config.Copy` 保留新字段；`defaultRagFactory.Create` 传递。无“待决策”项。

> 清理 context 的 5 秒仅为 `markFailed` 调用的**时间预算**，不保证驱动一定按时返回，也不声称“足够完成 DB 写入”（见 8.12）。

#### 已知限制

1. **不新增持久化任务系统**：归属 Stage 4，本批不引入
2. **重试/初始固定延迟**：仍为 1 秒固定延迟，但改为可取消 `select { case <-ctx.Done(); case <-time.After(time.Second) }`；不引入指数退避（可取消生命周期属 1D-3）
3. **不扩大至 Stage 4**：不实现任务恢复/重跑机制
4. **不用不可等待退出的 goroutine 包装阻塞操作**：每个已准入工作通过 bgWG 追踪，确保 Shutdown 可等待
5. **Qdrant 异步 indexer 路径**：保留现状，1D-3 不涉及
6. **拒绝/满载时文档状态**：因准入在同步索引**之前**，被拒绝时 SaveChunksData 根本未执行，文档停留在 controller 预设的 **Pending**（不是 Indexing）；拒绝不撤销上游创建的文档记录
7. **ES/Qdrant client 无显式 Close**：随进程退出释放，已知限制

### 8.15 未核实框架语义

以下项**未按假设开始实现**，如实列出：

| # | 未核实项 | 说明 | 影响 |
|---|---|---|---|
| 1 | GoFrame 停止信号与 graceful 超时细节 | **已核源码证据**：`Server.Run()`（gf v2.9.0，net/ghttp/ghttp_server.go L445-460）阻塞 `<-s.closeChan`（L456）→ `s.Shutdown()`（L459）→ 正常 return。但 closeChan 由哪些信号触发、GracefulShutdownTimeout（ghttp_server_graceful.go L22 取 `config.GracefulTimeout`）等待在途连接的确切超时/顺序**未逐一核实** | cmd.go 清理代码执行时机仍按“s.Run() 返回后执行”依赖 |
| 2 | 已准入同步阶段对 bgRootCancel 的响应 | 同步阶段 syncCtx 从 bgRootCtx 派生，但 Loader/Transformer/Indexer 是否逐层尊重 ctx 取消未验证 | Shutdown 对 in-flight 同步索引的收敛时间不确定（bgWG 仍会等待其自然结束） |
| 3 | DB driver 对 context 取消的响应 | `UpdateDocumentsStatus(cleanupCtx, ...)` 中 cleanupCtx 超时后 driver 是否能中断正在执行的 SQL 未验证 | markFailed 在 cleanupCtx 超时后可能失败（仅记日志） |
| 4 | ES client 对 context 取消的响应 | indexAsyncByDocsID 中 bgCtx 取消后 ES client 行为未验证 | 后台任务可能在 bgCtx 取消后仍阻塞于 ES 调用 |
| 5 | chat-history SaveMessage 无 context | 第三方包不支持 context timeout，保持现状 | History 保存无法施加有界 timeout |
| 6 | Windows race 检测 | core/indexer、core/common 的 race 因 `0xc0000139` 未完成（运行异常、根因待确认） | race 证据不完整 |

---

## 9. 离线行为测试矩阵

### 9.1 批次 1D-1 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestSaveChunksData_AllSuccess` | 全部写入成功时返回 nil，标记 Indexing | fake chunkStore: FindByChunkID 返回 ErrNoRows, Insert 返回 nil | err==nil; UpdateDocumentsStatus 调用参数为 StatusIndexing | 否 |
| `TestSaveChunksData_PartialFailure` | 部分失败时返回聚合 error | fake chunkStore: Insert 第 2 次返回 error | err!=nil 含两个错误信息（errors.Join）; status 为 Failed | 否 |
| `TestSaveChunksData_ScanDBError_NotTreatedAsInsert` | Scan 返回非 ErrNoRows 时走错误路径 | fake chunkStore: FindByChunkID 返回 connection error | err 含 find chunk; Insert 未被调用 | 否 |
| `TestSaveChunksData_UpdatePath_NoStatusOverwrite` | update 路径不写入 status 字段 | fake chunkStore: FindByChunkID 返回已有记录; Update 记录参数 | Update data map 不含 "status" key | 否 |
| `TestSaveChunksData_EmptyChunks` | 空 chunks 直接返回 nil | 无需 fake | err==nil; UpdateDocumentsStatus 未调用 | 否 |
| `TestSaveChunksData_PreCancelled` | 进入时 ctx 已 Done，快速返回 | 传入已取消 ctx | err==context.Canceled; 不执行 DB 操作 | 否 |
| `TestSaveChunksData_MidLoopCancel` | 循环中取消：停止后续 chunk | cancel 在第 2 次 Insert 后触发 | 仅处理前 2 个 chunk; err 含 context.Canceled | 否 |
| `TestSaveChunksData_StatusWriteError_Joined` | UpdateDocumentsStatus 失败也聚合 | fake chunkStore 全部成功; mock UpdateDocumentsStatus 返回 error | err!=nil 含 status 更新失败 | 否 |
| `TestIndexAsyncByDocsID_MarshalError_NoActive` | sonic.Marshal 失败时不标 Active | 构造含不可序列化 MetaData 的 doc; fakeDocSearcher | 返回 error 含 doc ID; UpdateDocumentsStatus(Active) 未调用 | 否 |
| `TestIndexAsyncByDocsID_MissingDocs_ReturnsError` | 重试耗尽仍有缺失文档时返回 error | fakeDocSearcher: SearchDocumentsByIDs 返回部分结果 | error 含 missing IDs; 不继续 SaveChunksData | 否 |
| `TestIndexAsyncByDocsID_SaveChunksError_Stops` | SaveChunksData 失败时不继续 IndexAsync | fake chunkStore 返回 error | SaveChunksData err 传播; IndexAsync 未调用; Active 未标 | 否 |
| `TestIndexAsyncByDocsID_FullSuccess_MarksActive` | 全部条件满足时标 Active | fakeDocSearcher/store/idxerAsync 全部成功 | UpdateDocumentsStatus(Active) 被调用; err==nil | 否 |
| `TestIndexAsyncByDocsID_Over1000Docs_Error` | DocsIDs > 1000 时返回超限错误 | 构造 1001 个 IDs | error 含 limit 信息; 不调用 Search | 否 |

**编排测试说明**：`TestIndexAsyncByDocsID_*` 系列通过构建 `prodAsyncIndexer{rag: testRag}` 触达真实 `indexAsyncByDocsID` 代码路径。注入边界为：
- `testRag.docSearch`：实现 `SearchDocumentsByIDs` 的 fakeDocSearcher
- `chunkStore`：传给内部 `saveChunks` helper 的 fake store
- `testRag.idxerAsync`：fake `compose.Runnable`（验证 IndexAsync 是否被调用）

> **测试证据边界**：预取消 fake 测试（传入已取消 ctx，断言快速返回）证明的是**应用层快速返回行为**（循环前检查 ctx.Err()）。它不证明数据库 driver 能中断正在执行的 SQL 语句。后者需真实驱动集成验证（如 MySQL + context.WithTimeout），属于未验证项，不影响本轮文档交付。

### 9.2 批次 1D-2 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestQA_AllSuccess` | 全部 QA 生成成功 | qaModelFactory 返回 fake model（Generate 返回固定内容） | 每个 doc.MetaData 含 QAContent; err==nil | 是（errgroup） |
| `TestQA_PartialFailure_ReturnsError` | 任一失败则整体失败 | fake model 对特定 doc 返回 error | err!=nil 含失败 doc ID; 返回 nil docs | 是 |
| `TestQA_ConcurrencyLimit` | 并发不超过上限 | fake model 阻塞在 channel; atomic counter 采样最大并行度 | 同时运行的 goroutine 数 ≤ maxConcurrent | 是 |
| `TestQA_Disabled_ReturnsDocsUnchanged` | QA 关闭时直接返回 | ctx 中 QAEnabled=false | docs 不变; err==nil; qaModelFactory 未调用 | 否 |
| `TestQA_Disabled_ExistingContent_Preserved` | QA 关闭时已有内容保持 | doc.MetaData 预设 QAContent; QAEnabled=false | FieldQAContent 值不变 | 否 |
| `TestQA_ContextCancel_Stops` | ctx 取消时停止 | 取消 ctx 后 fake model 检查 ctx.Done | err 含 context.Canceled | 是 |
| `TestQA_EmptyDocs` | 空 docs 直接返回 | 无需 fake | docs==empty; err==nil | 否 |
| `TestQA_NilDoc_ReturnsError` | nil doc 防护 | docs 含 nil 元素 | err 含 nil doc | 否 |
| `TestQA_DuplicatePointer_ReturnsError` | 重复指针去重 | docs 含同一指针两次 | err 含 duplicate | 否 |
| `TestQA_ExistingContent_Skips` | 已有 QAContent 的 doc 跳过生成 | doc.MetaData 预设非空 QAContent | qaModelFactory 未对该 doc 调用 Generate | 是 |
| `TestQA_GraphIntegration_ErrorBreaksGraph` | 真实 Graph 验证 QA 失败中断 | BuildIndexerAsync + fake model 返回 error + fake indexer 计数 | Indexer 节点未被调用; errors.As 提取原始 error | 否 |

**并发测试规则**：
- 使用 buffered channel + atomic counter 收集并行度采样
- `t.Cleanup` 释放所有阻塞 channel
- 超时 5s 后 `t.Fatal`（不静默跳过）
- 不使用固定 sleep 代替同步
- 不以 fake 自身测试代替生产逻辑验证

### 9.3 批次 1D-3 测试（按批次分组，不测未实现行为）

**A 组（准入契约 + 所有权转移 + Shutdown 核心）**：

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestIndex_AdmissionClosed_Rejects` | closed 时拒绝 | Shutdown 后 Index | 返回 `ErrShuttingDown`；fakeSyncIndexer 未被调用；无后台启动 | 否 |
| `TestIndex_AdmissionFull_Rejects` | 满载时拒绝 | bgSem 容量=1，第 1 个阻塞同步 | 第 2 个 Index 返回 `ErrBackgroundFull`；fakeSyncIndexer 未被调用（无同步索引副作用） | 是 |
| `TestIndex_SyncFail_ReleasesOnce` | 同步失败释放容量+登记 | fakeSyncIndexer 返回 sentinel | 返回该 sentinel；容量与 bgWG 只释放一次（后续 Index 仍可准入） | 是 |
| `TestIndex_SyncSuccess_TransfersOwnershipOnce` | 所有权只释放一次 | fake asyncIndexer 阻塞后释放 | 后台结束后 slot/Done 只释放一次（无负计数/panic） | 是 |
| `TestIndex_Shutdown_WaitsAdmittedSyncAndBG` | Shutdown 等待已准入同步+后台 | 准入后同步阶段阻塞；后台阻塞 | Shutdown 未收敛前不返回；释放后才返回 nil | 是 |
| `TestIndex_Shutdown_Timeout_KeepsWaiting` | 超时后后续等待同一 done | fake asyncIndexer 永久阻塞；首次 Shutdown 短 ctx | 首次返回可识别 error；释放后台后二次 Shutdown 返回 nil | 是 |
| `TestIndex_Shutdown_Idempotent` | stop/cancel 动作幂等 | 调用两次 Shutdown | Once 只执行一次 stop/cancel；两次均 select 同一 done，不 panic | 否 |
| `TestIndex_AsyncWaitReceivesCtx` | asyncWait 接收 ctx | asyncWait 注入记录 ctx | ctx != nil 且可取消 | 是 |

**B 组（失败/panic/取消 + markFailed + 重试可取消）**：

| 测试名称 | 目标 | 注入方式 | 断言要点 |
|---|---|---|---|
| `TestIndex_BackgroundFailure_MarksFailed` | 后台失败标 Failed | fakeAsyncIndexer 返回 error; fake markFailed 记录 | markFailed 被调用，docID 正确 |
| `TestIndex_BackgroundPanic_MarksFailed` | panic 后标 Failed | fakeAsyncIndexer panic | markFailed 被调用；不崩溃 |
| `TestIndex_ContextTimeout_MarksFailed` | bgCtx 超时标 Failed | taskTimeout=50ms; fake 阻塞 | markFailed 被调用，err 含 DeadlineExceeded |
| `TestIndex_Success_NoMarkFailed` | 成功不标 Failed | fake 返回 nil | markFailed 未被调用 |
| `TestIndex_RetryRespectsCtx` | 递归重试检查 ctx | 传入可取消 ctx; fakeDocSearcher 缺数 | 取消后返回 ctx.Err()，不继续重试 |
| `TestIndex_CleanupCtx_IndependentOfBgCtx` | markFailed 用独立 cleanupCtx | bgCtx 已取消后 markFailed | markFailed 收到的 ctx 未取消 |

**C 组（cmd 接线与 Shutdown 超时收敛协调，离线）**：

| 测试名称 | 目标 | 注入/驱动 | 断言要点 |
|---|---|---|---|
| `TestShutdown_Timeout_KeepsDepsOpen` | Shutdown 超时时依赖未关闭 | 后台永久阻塞 + 假 dao.Close 记录 | Shutdown 返回 error 时 `dao.Close`/Cleanup **未调用** |
| `TestShutdown_ReverseCloseOnlyAfterConverge` | 后台结束后才逆序关闭 | 释放后台→再等 done | Cleanup 仅在 Shutdown 返回 nil 后逆序执行 |
| `TestShutdown_SubsequentWaitSucceeds` | 后续等待可成功 | 首次超时、后台结束后二次调用 | 二次返回 nil，此时才允许 Cleanup |
| `TestShutdown_ErrorsIdentifiable` | 原始错误与清理错误可识别 | Shutdown 返回 timeout sentinel；Cleanup 返回另一 sentinel | `errors.Is` 可分别识别未收敛错误与清理错误；超时路径不报正常清理成功 |

**并发测试规则**（同 9.2）：channel 固定事件顺序 + 超时 + t.Cleanup；不以 fake 自身代替生产逻辑验证；不连接真实模型/DB/ES。

---

## 10. 验证命令与退出条件

### 每批完成后

**1D-1**：
```bash
cd server
go build ./...
go vet ./...
go test ./internal/logic/knowledge/... -count=1 -v
go test -race ./internal/logic/knowledge/... -count=1
go test ./core/... -count=1 -v
go test -race ./core -count=1
```

**1D-2**：
```bash
cd server
go mod tidy
go build ./...
go vet ./...
go test ./core/indexer/... -count=1 -v
go test -race ./core/indexer/... -count=1
go test ./core/... -count=1 -v
go test -race ./core -count=1
```

**1D-3**：
```bash
cd server
go build ./...
go vet ./...
go test ./core -count=1 -v
go test -race ./core -count=1
go test -race ./core -count=10 -run TestIndex_
```

### 全量验证（1D 全部完成后）

```bash
cd server
go build ./...
go vet ./...
go test -p 1 ./... -count=1
go test -race ./core/... -count=1
go test -race ./internal/logic/knowledge/... -count=1
go test -race ./core -count=10 -run TestIndex_
```

### 退出条件

- `go build` 退出码 0
- `go vet` 退出码 0
- 所有新增测试通过（`-v` 输出确认）
- 已有测试不受影响（或同步更新后通过）
- race detector 覆盖范围内退出码 0
- 离线测试不触发真实连接（无 DB/ES/模型）
- 压测（`-count=10`）无 DATA RACE、无 FAIL

### race 检测范围

| 包 | 范围 |
|---|---|
| `./core` | 完整 race（Index 后台 goroutine） |
| `./core/indexer/...` | 完整 race（QA errgroup） |
| `./internal/logic/knowledge/...` | 完整 race（SaveChunksData） |
| `./core/retriever`、`./core/rerank` | **保留现状（历史未补证）** |

---

## 11. 回滚与验证边界

### 11.1 回滚策略

每批独立提交，回滚使用 `git revert <commit>` 生成反向提交：

| 批次 | 回滚操作 | 依赖链 |
|---|---|---|
| 1D-1 | `git revert <1D-1-commit>` | 无后续依赖时可独立回滚 |
| 1D-2 | `git revert <1D-2-commit>` | 依赖 1D-1；若回滚 1D-1 需先回滚 1D-2 |
| 1D-3 | `git revert <1D-3-commit>` | 依赖 1D-1+1D-2；回滚前序需同步回滚后续 |

**批次间依赖链**：`1D-3 → 1D-2 → 1D-1`

- 回滚 1D-3：独立，不影响 1D-1/1D-2
- 回滚 1D-2：需同步回滚 1D-3（1D-3 依赖 QA 错误传播）
- 回滚 1D-1：需同步回滚 1D-2 + 1D-3

**冲突处理**：revert 时检查冲突（后续批次可能修改同一文件），必要时手动解决。

### 11.2 数据副作用声明

代码回滚**不撤销**已写入的数据：

| 副作用类型 | 回滚后行为 |
|---|---|
| chunks 已写入 DB | 保持原样，不回滚 |
| 文档状态已写为 Failed | 保持 Failed，不自动恢复 |
| 文档状态已写为 Active | 保持 Active |
| ES 中向量已部分索引 | 保持原样，不清理 |

**声明**：
- "无 schema 变更"不等于"无数据副作用"
- 回滚计划仅覆盖代码变更
- 数据修复需独立评估（手动 SQL 或 Stage 4 任务恢复）

---

## 12. 阻塞项与决策分类

### 12.1 已验证事实（有源码证据）

| # | 事实 | 验证状态 |
|---|---|---|
| 1 | GoFrame Scan 返回 sql.ErrNoRows | 已验证 |
| 2 | eino Lambda error 中断 Graph | 已验证 |
| 3 | errgroup.SetLimit 可用（x/sync v0.15.0 在模块图中） | 已验证 |
| 4 | common.qaModel 无锁懒加载缓存存在 race（历史问题） | 已确认；已由实例级 `qaModelCache`（mu+ready，见 7.4）修复 |
| 5 | asyncWait 签名 func()，8 处注入点 | 已确认 |
| 6 | cmd.go 无 Shutdown hook | 已确认 |
| 7 | DocumentToFields 无条件写 QA 字段 | 已确认（1D-2 已改为非空才写，由统一 helper `documentToFields` 实现） |
| 8 | `errgroup.Go()` 在 ctx 取消后**仍启动**新 goroutine（errgroup.go L86-92 无 ctx 检查），取消传播依赖任务函数自行检查 egCtx | 已验证（源码） |
| 9 | `Rag.conf` 是 `*config.Config` 具体 struct 指针（非接口），`SearchDocumentsByIDs` 是其方法 | 已确认 |
| 10 | GoFrame v2.9.0 `Server.Run()`（`net/ghttp/ghttp_server.go` L445-460）阻塞 `<-s.closeChan`（L456）→ `s.Shutdown()`（L459）→ 正常 return（不 os.Exit），s.Run() 返回后清理代码可执行 | Run return 已核源码；closeChan 触发的信号集合与 GracefulTimeout 等待在途连接顺序未逐一核实 |
| 11 | QA 关闭时 ES 中 `FieldQAContentVector` 字段可能不存在或为空，`qaRtrvr` 对该字段的 kNN 检索行为**待验证** | 待验证 |

### 12.2 实施前必须解决的问题（阻塞项）

| # | 问题 | 影响批次 | 解决方案 |
|---|---|---|---|
| B-1 | GetQAModel 并发 race | 1D-2 | 已由实例级 `qaModelCache`（mu+ready，失败不发布缓存）修复（已实现）；不再采用 sync.Once 作为现行方案 |
| B-2 | asyncWait 签名变更需同步修改 8 处测试注入 | 1D-3 | indexer_test.go 中 `func()` → `func(ctx context.Context)` |
| B-3 | x/sync 需 go mod tidy 提升为直接依赖 | 1D-2 | 1D-2 首次 import errgroup 后立即 tidy |
| B-4 | SaveChunksData 公开签名不变，内部 helper 接收依赖 | 1D-1 | 公开入口委派到 `saveChunks(ctx, docID, chunks, store, statusUpdater)` |
| A-4 | QA 关闭时 `qaRtrvr` 对缺失 `FieldQAContentVector` 字段的 kNN 检索行为 | 1D-2 | **待验证**：1D-2 实施时需确认实际行为（可能返回空结果，也可能返回错误，取决于 ES 版本和索引 mapping） |

### 12.3 后续阶段边界

| # | 事项 | 归属阶段 |
|---|---|---|
| 1 | 持久化任务恢复（进程重启后重跑 Failed 文档） | Stage 4 |
| 2 | 完整 graceful shutdown（信号处理、有序资源释放） | Stage 4 |
| 3 | QA 重试逻辑（模型暂时不可用时重试） | Stage 2+ |
| 4 | Qdrant 异步 indexer 路径统一 | Stage 2+ |
| 5 | chunk 级 Failed 状态 / 新状态枚举 | Stage 4+ |
| 6 | ES 残留向量清理 | Stage 4 |
| 7 | SaveChunksData 部分成功回滚 | Stage 4 |
| 8 | 模型超时配置化 | Stage 2 |
| 9 | QA "尽力完成"降级模式 | 待产品确认 |

### 12.4 历史遗留

- core/retriever、core/rerank 子包 race 仍未补证
- 本阶段不要求运行全量 race 覆盖这些子包
- 保留现状，不引入新的 race 风险

---

## 13. 范围限制（负面清单）

本阶段 **不做** 以下事项：

1. 不引入持久化任务系统（Stage 4）
2. 不实现任务恢复/重跑机制
3. 不重写会话模型（Stage 5）
4. 不顺带重构已验收的 Retriever 同步路径
5. 不顺带重构已验收的 SSE 生命周期
6. 不连接真实模型或执行付费调用
7. 不引入 `PartialActive` 等新状态枚举
8. 不清理 ES/Qdrant 中的残留向量
9. 不修改数据库 schema（不新增字段）
10. 不实现 QA 重试逻辑
11. 不处理 History context / Flush 边界（保留现状）
12. 不补充 core/retriever、core/rerank 的 race 证据
13. 不修改 `Index()` 的公开 API 签名
14. 不修改前端代码
15. 不实现 QA "尽力完成"降级模式
16. 不使用可变包级函数变量作为注入方式
17. 不引入新的外部依赖版本（x/sync 沿用已有 v0.15.0）
18. 不处理 Qdrant 异步 indexer 路径

---

## 14. 测试编写规范

### 通用规则

1. **测试必须调用实际生产逻辑**：不以 fake 自身测试代替生产路径验证
2. **并发测试使用 channel 固定事件顺序**：有超时和 cleanup
3. **不以固定 sleep 代替同步**：使用 channel signal / WaitGroup / atomic
4. **不以总次数统计代替正确性验证**：验证具体参数和调用顺序
5. **每个后台测试 t.Cleanup 释放阻塞并等待完成**（超时 t.Fatalf，不静默）
6. **race 检测**：所有新增测试必须在 `-race` 下通过

### 注入边界约定

| 批次 | 注入方式 | 说明 |
|---|---|---|
| 1D-1 | `chunkStore` + `docStatusUpdater` 接口参数 | 内部 helper `saveChunks` 接收接口；测试传 fake |
| 1D-1 | `testRag.docSearch` fakeDocSearcher | 实现 SearchDocumentsByIDs；控制检索结果 |
| 1D-1 | `testRag.idxerAsync` fake Runnable | 验证 IndexAsync 是否被调用 |
| 1D-2 | `qaModelFactory` 构建参数 | BuildIndexerAsync 接收；返回 fake model |
| 1D-2 | `conf.QAEnabled`/`conf.QAMaxConcurrent` + `qaModelFactory` 构建参数 | QA 开关/并发经 config 传递；模型经工厂注入（**无 `WithQAConfig`**） |
| 1D-3 | `Rag.markFailed` 字段 | 构建时注入 fake（记录调用） |
| 1D-3 | `Rag.asyncWait` 字段 | 签名变为 `func(ctx context.Context)` |
| 1D-3 | `Rag.taskTimeout` 字段 | 控制后台超时时长 |
| 1D-3 | `Rag.bgSem` 容量 | 控制并发上限 |
| 1D-3 | `Rag.mu` + `Rag.closed` | 保护准入/关闭临界区 |

### 包内测试可见性

遵循项目约定：测试属同包（`package core` / `package knowledge` / `package indexer`），可访问私有字段。不使用 `//go:build offline` 标签。
