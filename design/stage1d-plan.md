# Stage 1D 细化实施计划

## 1. 概述

Stage 1D 目标：修复 Indexer 后台写入路径中的错误吞没、QA 无界并发和 goroutine 入库止损缺陷，使失败状态可观测、并发可控、错误可传播。

**范围**：
- F09：`SaveChunksData` 部分写入失败仍返回 nil，调用方吞没错误并无条件标记 Active
- F08：`qa()` 为每个 Chunk 启无界协程，失败仅记日志，顶层始终返回 nil
- F07：后台 goroutine 脱离调用方 ctx，recover 不标 Failed，递归重试不检查取消

**实施顺序**：F09 → F08 → F07（底层先修，上层后修）

**前置条件**：
- Stage 1C-2 已验收（基线 `f89bb81`）
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
| 12 | `errgroup.WithContext` ctx 取消后 `Go()` 不再启动新 goroutine | x/sync 标准行为 |

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
| 1D-3 | 后台 goroutine 使用可取消 bgCtx；失败/panic/超时标记 Failed；并发准入；Shutdown 联动 | 1D-1+1D-2（需下游错误正确传播才能 markFailed） |

---

## 5. 统一依赖注入方式

### 设计原则

**禁止**：可变包级函数变量替换方案（如 `chunkInsertFn`、`chunkUpdateFn`、`getQAModelFn`）。

**替代**：
1. **接口参数注入**：`SaveChunksData` 接收 `chunkStore` 接口参数，接口定义在同包内
2. **构建时固定**：QA model 通过 `BuildIndexerAsync` 构建时传入工厂函数，运行期只读
3. **结构体字段注入**：`markFailed` 通过 `Rag` struct 字段注入，构建时设置、运行期只读（与 1B-3 的 `syncIndexer`/`asyncIndexer` 同模式）
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

`SaveChunksData` 新增 store 参数：

```go
// 修改前
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error

// 修改后（新增 store 参数，公开入口保留兼容包装）
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks, store chunkStore) error
```

> **兼容处理**：保留无 store 参数的包装函数 `SaveChunksDataDefault(ctx, docID, chunks)` 供已有调用方使用（内部传 `&daoChunkStore{}`），避免大范围改调用。

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

### 6.1 修改文件清单

| 文件 | 修改范围 | 变更描述 |
|---|---|---|
| `server/internal/logic/knowledge/chunks.go` | 主要重写 `SaveChunksData` | chunkStore 接口；Scan 错误区分；聚合错误返回；update 路径不写 status |
| `server/core/indexer.go` L139-196 | `indexAsyncByDocsID` | sonic.Marshal 失败聚合；SaveChunksData 失败 return；ID 集合核对；仅成功标 Active |
| `server/core/indexer_test.go` | 扩展已有测试 + 新增编排测试 | 通过 asyncIndexer 接口触发真实 indexAsyncByDocsID 路径 |
| `server/internal/logic/knowledge/chunks_test.go`（新建） | — | chunkStore fake 驱动的行为测试 |

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
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks, store chunkStore) error {
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

    // 状态写入（使用 errors.Join 保留状态写入错误）
    if statusErr := UpdateDocumentsStatus(ctx, documentsId, status); statusErr != nil {
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

// 修改后
if err = knowledge.SaveChunksData(ctx, req.DocumentsId, chunks, store); err != nil {
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
// 构建测试用 Rag，注入 fake conf（实现 SearchDocumentsByIDs）和 fake chunkStore
// 但不注入 asyncIndexer — 使用 prodAsyncIndexer 指向真实 indexAsyncByDocsID
testRag := &Rag{
    conf: fakeConf{searchResult: ..., searchErr: nil},
    // asyncIndexer 不设，prodAsyncIndexer.rag 指向 testRag
}
testRag.asyncIndexer = &prodAsyncIndexer{rag: testRag}

// 通过 asyncIndexer.indexAsyncByDocsID 触发真实方法
err := testRag.asyncIndexer.indexAsyncByDocsID("kb1", 42, []string{"id1","id2"})
```

**测试矩阵见 §9.1**。

---

## 7. 批次 1D-2：F08 QA 有限并发和失败状态

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

### 7.4 GetQAModel 并发安全修复

**问题**：`common.qaModel` 是无锁懒加载缓存（chat_model.go L12-16, L97-115），并发调用存在 race。

**方案**：在 `core.New()` 中预热 `common.GetQAModel(ctx, nil)`（与 L75 预热 `GetChatModel` 同模式），确保 `qaModel` 在 Rag 构造时已初始化，后续并发读取安全（只读）。

```go
// rag.go New() 中追加（L75 之后）
qaModel, err := common.GetQAModel(ctx, nil)
if err != nil {
    g.Log().Error(ctx, "GetQAModel failed, err=%v", err)
    return nil, err
}
```

**测试注入**：通过 `BuildIndexerAsync` 接收的 `qaModelFactory` 函数（构建时固定），测试传入返回 fake model 的工厂。

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

    for _, doc := range docs {
        d := doc  // capture
        eg.Go(func() error {
            // nil 防护
            if d == nil || d.MetaData == nil {
                return fmt.Errorf("qa: nil doc or nil MetaData")
            }
            if egCtx.Err() != nil {
                return egCtx.Err()
            }
            qaContent, err := getQAContent(egCtx, d, knowledgeName)
            if err != nil {
                return fmt.Errorf("qa failed for doc %s: %w", d.ID, err)
            }
            d.MetaData[coretypes.FieldQAContent] = qaContent
            return nil
        })
    }

    if err := eg.Wait(); err != nil {
        return nil, err  // 首错取消，返回 error
    }
    return docs, nil
}
```

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

- QA 关闭时该字段在 ES 中不存在/为空
- `qaRtrvr` 检索返回空结果（不报错，ES 对缺失字段返回 0 分）
- **显式记录**：QA 关闭时 `qaRtrvr` 路径退化为空结果，不影响主检索路径（`rtrvr`）
- 主检索路径使用 `FieldContentVector`，与 QA 无关

### 7.10 首错取消语义

使用 `errgroup.WithContext`：
1. 首个非 nil error 取消 derived ctx（`egCtx`）
2. 已启动的 goroutine 通过 `egCtx.Done()` 感知取消
3. `cm.Generate(egCtx, msgs)` 接收该 ctx，LLM HTTP 调用应响应取消（取决于 provider 实现）
4. 等待所有已启动任务结束后返回首个错误
5. ctx 取消后 `Go()` 不再启动新 goroutine（x/sync 标准行为）

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

### 8.1 修改文件清单

| 文件 | 修改范围 | 变更描述 |
|---|---|---|
| `server/core/indexer.go` | `Index()` goroutine、`indexAsyncByDocsID()` | bgCtx 传递；准入信号量；markFailed；重试 ctx 检查 |
| `server/core/rag.go` | `Rag` struct、`New()`、新增 `Shutdown()` | bgWG/bgSem/bgCancel/shutdownOnce/markFailed 字段 |
| `server/core/indexer_test.go` | 已有 8 处 asyncWait + 新增测试 | asyncWait 签名变更；止损/准入/shutdown 测试 |
| `server/internal/cmd/cmd.go` | `s.Run()` 之后 | 追加 Shutdown 清理 |

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

后台任务在 HTTP 返回后继续执行属于**既有异步产品语义**（fire-and-forget indexing），不应一概称为"context 丢失"。

```go
// Index() 后台 goroutine 启动前
bgCtx, bgCancel := context.WithTimeout(context.Background(), x.taskTimeout)
// taskTimeout 为可配置值（默认 5min），覆盖"等待 ES 同步 + 检索重试 + QA 生成 + 向量索引"全链路
```

- Shutdown 时 `x.bgCancel()` 取消所有进行中的后台任务
- goroutine 内 defer bgCancel() 释放资源

### 8.4 任务准入策略

```go
// 准入检查在 goroutine 启动前（同步路径）
select {
case x.bgSem <- struct{}{}:
    // 准入成功：Add + go func
    x.bgWG.Add(1)
    go func() { ... }()
default:
    // 满载：返回明确错误给调用方
    // sync 写入已完成（不可回滚），doc 标记为 Pending（等待手动重试或 Stage 4 任务引擎）
    g.Log().Warningf(ctx, "background task queue full, doc %d stays Pending", documentsID)
}
```

**满载行为**：
- 返回明确错误给调用方（HTTP 层面可返回 429 或 503）
- **不启动 goroutine 等待**（否则等待 goroutine 无限增长）
- sync 写入已完成不可回滚，doc 保持 Pending 状态

**信号量配置**：`bgSem chan struct{}`，容量 = `MaxBackgroundTasks`（默认 8，通过 Config 配置）。

### 8.5 Shutdown 实施

**明确纳入 1D-3**，不作为待确认事项。

```go
// Rag struct 新增字段
type Rag struct {
    // ...existing...
    bgWG         sync.WaitGroup
    bgSem        chan struct{}
    bgCancel     context.CancelFunc   // 取消所有后台任务的全局 cancel
    shutdownOnce sync.Once
    shutdownFlag atomic.Bool          // 准入检查用
    taskTimeout  time.Duration        // 默认 5min
    markFailed   func(ctx context.Context, docID int64, err error)
}
```

**Shutdown 方法**：

```go
func (x *Rag) Shutdown(ctx context.Context) error {
    var shutdownErr error
    x.shutdownOnce.Do(func() {
        // 1. 设 flag 停止接收新任务
        x.shutdownFlag.Store(true)

        // 2. 取消所有进行中的后台任务
        x.bgCancel()

        // 3. 等待 bgWG（受传入 ctx 超时限制）
        done := make(chan struct{})
        go func() { x.bgWG.Wait(); close(done) }()
        select {
        case <-done:
            // 正常收敛
        case <-ctx.Done():
            shutdownErr = fmt.Errorf("shutdown timeout: %w", ctx.Err())
            g.Log().Warningf(ctx, "Rag.Shutdown: background tasks did not converge in time")
        }
    })
    return shutdownErr
}
```

**不声称已强制终止 goroutine**（Go 无法强杀 goroutine）。超时后仅报告日志。

### 8.6 Add/Wait 竞态防护

- `bgWG.Add(1)` 在准入成功后、goroutine 启动前执行（**同步路径**）
- `bgWG.Done()` 在 goroutine 退出时 defer 执行
- Shutdown 设置 `shutdownFlag` 后，新的准入检查会失败（不会 Add）
- 因此 `bgWG.Wait()` 不会与 `bgWG.Add()` 竞态

### 8.7 cmd.go 接线

```go
// cmd.go：s.Run() 返回后追加清理
s.Run()

// Shutdown RAG 后台任务
shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
defer shutdownCancel()
if ragSvr := ragicit.GetRagSvr(); ragSvr != nil {
    if err := ragSvr.Shutdown(shutdownCtx); err != nil {
        g.Log().Errorf(ctx, "Rag.Shutdown error: %v", err)
    }
}

// 然后执行 steps 逆序 Cleanup
for i := len(steps) - 1; i >= 0; i-- {
    if steps[i].Cleanup != nil {
        steps[i].Cleanup()
    }
}
return nil
```

**关闭顺序**：后台任务停止 → Rag.Shutdown → 关闭依赖资源（dao.Close 等）

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
| 启动者 | `Rag.Index` 同步路径（准入 + Add + go func） |
| 取消者 | `Rag.Shutdown` 调用 bgCancel |
| 等待者 | `Rag.Shutdown` 调用 bgWG.Wait |
| slot 释放者 | goroutine 自身（defer） |
| Done 调用者 | goroutine 自身（defer） |
| Failed 写入者 | goroutine 自身（使用 cleanupCtx） |

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
| `TestIndexAsyncByDocsID_MarshalError_NoActive` | sonic.Marshal 失败时不标 Active | 构造含不可序列化 MetaData 的 doc; fake conf | 返回 error 含 doc ID; UpdateDocumentsStatus(Active) 未调用 | 否 |
| `TestIndexAsyncByDocsID_MissingDocs_ReturnsError` | 重试耗尽仍有缺失文档时返回 error | fake conf: SearchDocumentsByIDs 返回部分结果 | error 含 missing IDs; 不继续 SaveChunksData | 否 |
| `TestIndexAsyncByDocsID_SaveChunksError_Stops` | SaveChunksData 失败时不继续 IndexAsync | fake chunkStore 返回 error | SaveChunksData err 传播; IndexAsync 未调用; Active 未标 | 否 |
| `TestIndexAsyncByDocsID_FullSuccess_MarksActive` | 全部条件满足时标 Active | fake conf/store/idxerAsync 全部成功 | UpdateDocumentsStatus(Active) 被调用; err==nil | 否 |
| `TestIndexAsyncByDocsID_Over1000Docs_Error` | DocsIDs > 1000 时返回超限错误 | 构造 1001 个 IDs | error 含 limit 信息; 不调用 Search | 否 |

**编排测试说明**：`TestIndexAsyncByDocsID_*` 系列通过构建 `prodAsyncIndexer{rag: testRag}` 触达真实 `indexAsyncByDocsID` 代码路径。注入边界为：
- `testRag.conf`：实现 `SearchDocumentsByIDs` 的 fake config
- `chunkStore`：传给 `SaveChunksData` 的 fake store
- `testRag.idxerAsync`：fake `compose.Runnable`（验证 IndexAsync 是否被调用）

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

### 9.3 批次 1D-3 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestIndex_BackgroundFailure_MarksFailed` | 后台失败标记 Failed | fakeAsyncIndexer 返回 error; fake markFailed 记录调用 | markFailed 被调用，docID 正确 | 是 |
| `TestIndex_BackgroundPanic_MarksFailed` | panic 后标记 Failed | fakeAsyncIndexer panic | markFailed 被调用; 不崩溃 | 是 |
| `TestIndex_ContextTimeout_MarksFailed` | 后台 bgCtx 超时标记 Failed | taskTimeout=50ms; fakeAsyncIndexer 阻塞 | markFailed 被调用，err 含 DeadlineExceeded | 是 |
| `TestIndex_SemaphoreFull_Rejects` | 信号量满载时拒绝新任务 | bgSem 容量=1; 第 1 个阻塞; 第 2 个 Index 调用 | 第 2 个 Index 返回后，后台 goroutine 未启动; log 含 queue full | 是 |
| `TestIndex_Shutdown_WaitsBackground` | Shutdown 等待后台完成 | fakeAsyncIndexer 阻塞后释放 | Shutdown 返回 nil; 后台任务全部完成 | 是 |
| `TestIndex_Shutdown_Timeout` | Shutdown 超时返回 error | fakeAsyncIndexer 永久阻塞 | Shutdown 返回 error 含 timeout | 是 |
| `TestIndex_Shutdown_Idempotent` | Shutdown 幂等 | 调用两次 Shutdown | 第二次返回 nil（不 panic） | 否 |
| `TestIndex_Shutdown_RejectsNew` | Shutdown 后拒绝新任务 | Shutdown 完成后调用 Index | 后台 goroutine 未启动（shutdownFlag=true） | 否 |
| `TestIndexAsyncByDocsID_RetryRespectsCtx` | 递归重试检查 ctx | 传入可取消 ctx; fake conf 第一次返回不足结果 | 取消后返回 ctx.Err(); 不继续重试 | 是 |
| `TestIndex_Success_NoMarkFailed` | 成功路径不调用 markFailed | fakeAsyncIndexer 返回 nil | markFailed 未被调用 | 是 |
| `TestIndex_AsyncWaitReceivesCtx` | asyncWait 接收 bgCtx | asyncWait 注入记录 ctx | ctx != nil; ctx.Done() 可关闭 | 是 |
| `TestIndex_CleanupCtx_IndependentOfBgCtx` | markFailed 使用独立 cleanupCtx | bgCtx 已取消后 markFailed 被调用 | markFailed 接收的 ctx 未取消 | 是 |

**并发测试规则**（同 9.2）：
- channel 固定事件顺序
- 超时 + t.Cleanup
- 不以 fake 自身测试代替生产逻辑验证
- 不连接真实模型

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
| 4 | common.qaModel 无锁缓存存在 race | 已确认 |
| 5 | asyncWait 签名 func()，8 处注入点 | 已确认 |
| 6 | cmd.go 无 Shutdown hook | 已确认 |
| 7 | DocumentToFields 无条件写 QA 字段 | 已确认 |

### 12.2 实施前必须解决的问题（阻塞项）

| # | 问题 | 影响批次 | 解决方案 |
|---|---|---|---|
| B-1 | GetQAModel 并发 race | 1D-2 | 在 `core.New()` 中预热（与 GetChatModel 同模式） |
| B-2 | asyncWait 签名变更需同步修改 8 处测试注入 | 1D-3 | indexer_test.go 中 `func()` → `func(ctx context.Context)` |
| B-3 | x/sync 需 go mod tidy 提升为直接依赖 | 1D-2 | 1D-2 首次 import errgroup 后立即 tidy |
| B-4 | SaveChunksData 签名变更影响已有调用方 | 1D-1 | 保留兼容包装函数 |

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
| 1D-1 | `chunkStore` 接口参数 | SaveChunksData 接收接口；测试传 fake |
| 1D-1 | `testRag.conf` fake | 实现 SearchDocumentsByIDs；控制检索结果 |
| 1D-1 | `testRag.idxerAsync` fake Runnable | 验证 IndexAsync 是否被调用 |
| 1D-2 | `qaModelFactory` 构建参数 | BuildIndexerAsync 接收；返回 fake model |
| 1D-2 | `coretypes.WithQAConfig(ctx, ...)` | 控制 enabled/maxConcurrent |
| 1D-3 | `Rag.markFailed` 字段 | 构建时注入 fake（记录调用） |
| 1D-3 | `Rag.asyncWait` 字段 | 签名变为 `func(ctx context.Context)` |
| 1D-3 | `Rag.taskTimeout` 字段 | 控制后台超时时长 |
| 1D-3 | `Rag.bgSem` 容量 | 控制并发上限 |

### 包内测试可见性

遵循项目约定：测试属同包（`package core` / `package knowledge` / `package indexer`），可访问私有字段。不使用 `//go:build offline` 标签。
