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

---

## 2. F 编号核对表

### F09 — Chunk 写入错误传播

| 项目 | 内容 |
|---|---|
| **文件** | `server/internal/logic/knowledge/chunks.go` |
| **函数** | `SaveChunksData(ctx, documentsId, chunks) error` |
| **调用链** | `Index()` → goroutine → `prodAsyncIndexer.indexAsyncByDocsID()` → `Rag.indexAsyncByDocsID()` → `knowledge.SaveChunksData()` |
| **缺陷 1** | `chunks.go:52` — 函数始终 `return nil`，即使循环内有 insert/update 失败 |
| **缺陷 2** | `chunks.go:51` — `UpdateDocumentsStatus(ctx, documentsId, status)` 返回值被丢弃 |
| **缺陷 3** | `indexer.go:180-183` — `SaveChunksData` 返回的 error 仅记日志，不 return，后续继续执行 |
| **缺陷 4** | `indexer.go:194` — 无条件 `UpdateDocumentsStatus(ctx, req.DocumentsId, int(v1.StatusActive))`，覆盖 SaveChunksData 内设置的 Failed |
| **缺陷 5** | `chunks.go:33` — update 路径写入 `chunk.Status`，但调用方构造的 chunk 从未赋非零 Status（死代码） |
| **触发条件** | 任意 chunk insert/update 失败（DB 连接断开、唯一约束冲突、磁盘满） |
| **错误应传播到** | `Rag.indexAsyncByDocsID()` 返回 error → 后台 goroutine 标记文档 Failed |
| **失败边界** | 部分 chunk 可能已写入 DB；ES 中向量可能已部分索引（不清理残留） |
| **与 1B/1C 依赖** | 无直接依赖；1B-3 的 `asyncIndexer` 注入边界使测试可绕过真实 DB |

### F08 — QA 有限并发和失败状态

| 项目 | 内容 |
|---|---|
| **文件** | `server/core/indexer/qa.go` |
| **函数** | `qa(ctx, docs) ([]*schema.Document, error)` |
| **调用链** | `Rag.IndexAsync()` → `idxerAsync.Invoke()` → eino Graph → Lambda Node `qa()` → `getQAContent()` → `common.GetQAModel()` → `cm.Generate()` |
| **缺陷 1** | `qa.go:22-36` — 裸 `sync.WaitGroup`，每个 doc 一个 goroutine，无并发上限 |
| **缺陷 2** | `qa.go:28-31` — `getQAContent` 失败仅 `g.Log().Errorf` + return，不传播 |
| **缺陷 3** | `qa.go:37` — 顶层始终 `return docs, nil`，调用方无法感知 QA 失败 |
| **缺陷 4** | 无 QA 配置开关，无法关闭 QA 生成 |
| **缺陷 5** | 无 `ctx.Err()` 检查，取消后 goroutine 继续调用模型 |
| **缺陷 6** | `indexer_async.go:54-57` — 无条件写 `FieldQAContent` 到 ES，QA 失败时写入空值 |
| **触发条件** | 模型调用失败（超时、限流、网络错误）；大量 chunk 同时生成 QA 导致 OOM/限流 |
| **错误应传播到** | `qa()` 返回 error → eino Graph 中断 → `IndexAsync()` 返回 error → `indexAsyncByDocsID()` 标记 Failed |
| **失败边界** | 部分 doc 的 MetaData 可能已写入 QAContent（成功的 goroutine）；不清理已写入的 partial QA |
| **与 1B/1C 依赖** | 无直接依赖；QA lambda 通过 eino Graph 挂载，修改 `qa()` 签名需同步修改 `async.go:23` 的 `compose.InvokableLambda(qa)` |

### F07 — goroutine 入库止损

| 项目 | 内容 |
|---|---|
| **文件** | `server/core/indexer.go` |
| **函数** | `Rag.Index()` 内 goroutine、`Rag.indexAsyncByDocsID()` |
| **调用链** | Controller → `Rag.Index()` → goroutine → `prodAsyncIndexer.indexAsyncByDocsID()` → `Rag.indexAsyncByDocsID()` → `SaveChunksData` / `IndexAsync` / `UpdateDocumentsStatus` |
| **缺陷 1** | `indexer.go:100` — goroutine 使用 `gctx.New()` 脱离调用方 ctx，无法被取消 |
| **缺陷 2** | `indexer.go:102-106` — recover 只打日志，不标记文档 Failed |
| **缺陷 3** | `indexer.go:149-154` — 递归重试 `time.Sleep(time.Second)` 不检查 ctx，取消后继续重试 |
| **缺陷 4** | 无并发上限，每次 `Index()` 调用都启新 goroutine |
| **缺陷 5** | 无 shutdown 联动，进程退出时后台 goroutine 被强杀 |
| **缺陷 6** | `indexer.go:194` — 成功路径无条件标 Active（与 F09 缺陷 4 重叠） |
| **触发条件** | 进程重启/关闭时后台任务丢失；ctx 取消后 goroutine 继续执行；panic 后文档卡在 Indexing 状态 |
| **错误应传播到** | 后台 goroutine 内：失败时标记 `StatusFailed`；不向调用方传播（Index 已返回） |
| **失败边界** | ES 中可能已有部分向量（不清理）；DB 中可能已有部分 chunk（不回滚） |
| **与 1B/1C 依赖** | 1B-3 已建立 `asyncWait` 注入机制；1B-3 的 recover 已前置覆盖整个后台函数 |

---

## 3. 批次划分与实施顺序

### 批次总览

```
1D-1 (F09) ──→ 1D-2 (F08) ──→ 1D-3 (F07)
  chunks.go       qa.go           indexer.go
  错误传播        有限并发         止损标记
```

### 1D-1（F09）：Chunk 写入错误传播

**目标**：`SaveChunksData` 正确返回聚合错误；`indexAsyncByDocsID` 传播错误而非吞没；成功路径才标 Active。

**交付物**：
- 修改 `chunks.go`、`indexer.go`
- 新增/修改测试
- race 检测通过

**理由**：最底层修复，F08 和 F07 的错误传播都依赖 SaveChunksData 正确返回 error。

### 1D-2（F08）：QA 有限并发和失败状态

**目标**：`qa()` 使用有界并发；失败返回 error；支持配置关闭 QA；ctx 取消时停止。

**交付物**：
- 修改 `qa.go`、`async.go`、`config.go`（或新增 QA 配置字段）
- 新增测试
- race 检测通过

**依赖**：1D-1 完成后，QA 失败可通过 `IndexAsync` → `indexAsyncByDocsID` 正确传播。

### 1D-3（F07）：goroutine 入库止损

**目标**：后台 goroutine 使用可取消 ctx；失败/panic 标记 Failed；递归重试检查 ctx；可选 shutdown 联动。

**交付物**：
- 修改 `indexer.go`
- 新增/修改测试
- race 检测通过

**依赖**：1D-1 的 SaveChunksData 错误传播 + 1D-2 的 QA 错误传播，使后台 goroutine 能感知下游失败并标记 Failed。

### 首批推荐：1D-1（F09）

**理由**：
1. 修改范围最小（2 个文件，核心变更约 20 行）
2. 不涉及并发模型变更
3. 为后续批次提供错误传播基础
4. 测试简单（mock DB 层即可验证）

---

## 4. 每批详细设计

### 4.1 批次 1D-1：F09 Chunk 写入错误传播

#### 4.1.1 修改文件清单

| 文件 | 修改函数 | 变更描述 |
|---|---|---|
| `server/internal/logic/knowledge/chunks.go` | `SaveChunksData` | 聚合错误并返回；UpdateDocumentsStatus 错误传播 |
| `server/core/indexer.go` | `indexAsyncByDocsID` | SaveChunksData 失败时 return error；仅成功时标 Active |
| `server/internal/logic/knowledge/chunks_test.go`（新建） | — | 离线行为测试 |

#### 4.1.2 最小注入边界

`SaveChunksData` 是包级函数，直接操作 `dao.KnowledgeChunks`。为测试注入：

**方案**：在 `chunks.go` 同包内引入包级函数变量：

```go
// 可注入的 DB 操作边界（测试用）
var (
    chunkInsertFn = defaultChunkInsert
    chunkUpdateFn = defaultChunkUpdate
    updateDocStatusFn = UpdateDocumentsStatus
)
```

生产代码调用 `chunkInsertFn`/`chunkUpdateFn`；测试替换为 fake。

**约束**：
- 不改变 `SaveChunksData` 的公开签名
- 函数变量仅包内可见（小写开头）
- 与 1B-3 的 `syncIndexer`/`asyncIndexer` 注入边界无冲突（不同包）

#### 4.1.3 输入输出与错误契约

**`SaveChunksData` 签名不变**：

```go
func SaveChunksData(ctx context.Context, documentsId int64, chunks []entity.KnowledgeChunks) error
```

**新语义**：
- 全部成功：调用 `updateDocStatusFn(ctx, documentsId, StatusIndexing)`，返回 nil
- 部分/全部失败：调用 `updateDocStatusFn(ctx, documentsId, StatusFailed)`，返回聚合 error
- 聚合 error 格式：`fmt.Errorf("save chunks partial failure: %d/%d failed, first error: %w", failCount, total, firstErr)`
- `updateDocStatusFn` 自身失败：追加到聚合 error（`errors.Join`）

**`indexAsyncByDocsID` 变更**：

```go
// 修改前（indexer.go:180-183）
if err = knowledge.SaveChunksData(ctx, req.DocumentsId, chunks); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID insert chunks failed, err=%v", err)
}

// 修改后
if err = knowledge.SaveChunksData(ctx, req.DocumentsId, chunks); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID insert chunks failed, err=%v", err)
    return nil, err  // ← 传播错误，不继续执行 IndexAsync
}
```

```go
// 修改前（indexer.go:194）
knowledge.UpdateDocumentsStatus(ctx, req.DocumentsId, int(v1.StatusActive))

// 修改后
if err = knowledge.UpdateDocumentsStatus(ctx, req.DocumentsId, int(v1.StatusActive)); err != nil {
    g.Log().Errorf(ctx, "indexAsyncByDocsID UpdateDocumentsStatus Active failed, err=%v", err)
    return nil, err
}
```

**取消契约**：`SaveChunksData` 内循环应在每次迭代前检查 `ctx.Err()`，取消时提前返回。

#### 4.1.4 并发任务所有权

本批次不涉及新的 goroutine。`SaveChunksData` 是同步函数，由后台 goroutine（1D-3 管辖）调用。

#### 4.1.5 部分写入与部分成功处理

| 场景 | 策略 |
|---|---|
| 全部 chunk 写入成功 | 标记 StatusIndexing，返回 nil |
| 部分 chunk 写入失败 | 标记 StatusFailed，返回聚合 error（含失败数和首个错误） |
| 全部 chunk 写入失败 | 标记 StatusFailed，返回聚合 error |
| UpdateDocumentsStatus 自身失败 | 返回 error（不影响已写入的 chunk） |
| ctx 取消 | 提前返回 ctx.Err()，已写入的 chunk 保留（不回滚） |

**已知限制**：
- 不引入 `PartialActive` 状态
- 不清理已写入 DB 的部分 chunk（留待 Stage 4 任务恢复）
- 不清理 ES 中残留向量

---

### 4.2 批次 1D-2：F08 QA 有限并发和失败状态

#### 4.2.1 修改文件清单

| 文件 | 修改函数 | 变更描述 |
|---|---|---|
| `server/core/indexer/qa.go` | `qa`、`getQAContent` | errgroup 有限并发；失败传播；ctx 检查 |
| `server/core/indexer/async.go` | `BuildIndexerAsync` | QA 节点可选跳过 |
| `server/core/config/config.go` | `Config` struct | 新增 QA 相关配置字段 |
| `server/core/indexer/qa_test.go`（新建） | — | 离线行为测试 |

#### 4.2.2 最小注入边界

**QA 模型注入**：

`getQAContent` 内部调用 `common.GetQAModel(ctx, nil)` 获取模型。为测试注入：

```go
// qa.go 包级函数变量（测试注入点）
var getQAModelFn = common.GetQAModel
```

测试替换 `getQAModelFn` 为返回 fake `model.BaseChatModel` 的函数。

**QA 并发配置注入**：

```go
// Config 新增字段
type Config struct {
    // ...existing fields...
    QAEnabled       bool `json:"qaEnabled"`       // 默认 true
    QAMaxConcurrent int  `json:"qaMaxConcurrent"` // 默认 4，范围 1-32
}
```

`qa()` 函数签名变更以接收配置：

```go
// 修改前
func qa(ctx context.Context, docs []*schema.Document) (output []*schema.Document, err error)

// 修改后（保持 eino Lambda 兼容签名）
func qa(ctx context.Context, docs []*schema.Document) (output []*schema.Document, err error)
```

配置通过 ctx 传递（与 `coretypes.KnowledgeName` 同机制）：

```go
// core/types/types.go 新增
type qaConfigKey struct{}

func WithQAConfig(ctx context.Context, enabled bool, maxConcurrent int) context.Context
func GetQAConfig(ctx context.Context) (enabled bool, maxConcurrent int)
```

**约束**：
- `qa()` 保持 `func(context.Context, []*schema.Document) ([]*schema.Document, error)` 签名（eino InvokableLambda 要求）
- 与 1B-3 注入边界无冲突（不同包、不同机制）

#### 4.2.3 输入输出与错误契约

**`qa()` 新语义**：

```go
func qa(ctx context.Context, docs []*schema.Document) ([]*schema.Document, error) {
    // 1. 提取 knowledgeName（已有逻辑）
    // 2. 提取 QA 配置
    enabled, maxConcurrent := coretypes.GetQAConfig(ctx)
    if !enabled {
        return docs, nil  // QA 关闭时直接返回原始 docs
    }

    // 3. errgroup 有限并发
    eg, egCtx := errgroup.WithContext(ctx)
    eg.SetLimit(maxConcurrent)

    for _, doc := range docs {
        d := doc  // capture
        eg.Go(func() error {
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
        return nil, err  // 任一失败则整体失败
    }
    return docs, nil
}
```

**错误语义**：
- QA 关闭：返回 `(docs, nil)`，docs 无 QAContent 字段
- 全部成功：返回 `(docs, nil)`，每个 doc.MetaData 含 QAContent
- 任一失败：返回 `(nil, error)`，error 包含首个失败的 doc ID 和原因
- ctx 取消：返回 `(nil, ctx.Err())`

**`indexer_async.go` 变更**：

当 `qa()` 返回 error 时，eino Graph 中断，`IndexAsync` 返回 error，不会执行到 `newAsyncIndexer` 的 DocumentToFields。无需修改 `indexer_async.go` 的字段写入逻辑。

但需处理 QA 关闭时的情况：QA 关闭 → `qa()` 返回 docs 但无 QAContent → `DocumentToFields` 写入空 QAContent → ES 中空向量字段。

**决策**：QA 关闭时，`DocumentToFields` 应跳过 QAContent 字段：

```go
// indexer_async.go DocumentToFields 内
fields := map[string]es8.FieldValue{
    coretypes.FieldContent: {Value: doc.Content, EmbedKey: coretypes.FieldContentVector},
    coretypes.FieldExtra:   {Value: doc.MetaData[coretypes.FieldExtra]},
    coretypes.KnowledgeName: {Value: knowledgeName},
}
// 仅当 QAContent 非空时写入
if qaVal, ok := doc.MetaData[coretypes.FieldQAContent].(string); ok && qaVal != "" {
    fields[coretypes.FieldQAContent] = es8.FieldValue{Value: qaVal, EmbedKey: coretypes.FieldQAContentVector}
}
return fields, nil
```

#### 4.2.4 并发任务所有权

| 角色 | 实体 |
|---|---|
| 启动者 | `qa()` 函数通过 `errgroup.Go()` 启动 |
| 取消者 | `errgroup.WithContext` 派生的 `egCtx`：任一 goroutine 失败时自动取消 |
| 等待者 | `qa()` 内 `eg.Wait()` 阻塞等待全部完成 |
| 上层取消 | 调用方 ctx 取消 → egCtx 取消 → 所有 goroutine 感知 |

**关闭顺序**：
1. `eg.Wait()` 返回（所有 goroutine 退出）
2. `qa()` 返回 error 给 eino Graph
3. Graph 中断，`IndexAsync` 返回 error

**不使用** `sync.WaitGroup`（已被 errgroup 替代）。

#### 4.2.5 部分写入与部分成功处理

| 场景 | 策略 |
|---|---|
| QA 全部成功 | docs 全部含 QAContent，正常流入 Indexer 节点 |
| QA 部分失败 | errgroup 取消剩余，返回 error，Graph 中断，不写入 ES |
| QA 全部失败 | 同上 |
| QA 关闭 | docs 无 QAContent，正常流入 Indexer，ES 不写 QA 向量字段 |
| ctx 取消 | 返回 ctx.Err()，Graph 中断 |

**已知限制**：
- errgroup 默认策略：首个错误即取消全部。不支持"尽力完成"模式（降级策略留待产品确认）
- 已成功的 goroutine 写入的 `doc.MetaData[FieldQAContent]` 保留在内存中（不回滚），但因 Graph 中断不会写入 ES
- 不实现 QA 重试（失败即标记文档 Failed）

---

### 4.3 批次 1D-3：F07 goroutine 入库止损

#### 4.3.1 修改文件清单

| 文件 | 修改函数 | 变更描述 |
|---|---|---|
| `server/core/indexer.go` | `Index()` 内 goroutine | 使用可取消 ctx；失败标记 Failed；recover 标记 Failed |
| `server/core/indexer.go` | `indexAsyncByDocsID()` | 递归重试检查 ctx |
| `server/core/indexer.go` | `Rag` struct | 可选：新增 backgroundWG / semaphore |
| `server/core/indexer_test.go` | — | 新增止损行为测试 |

#### 4.3.2 最小注入边界

**已有注入边界（1B-3）**：
- `syncIndexer` — 同步索引 fake
- `asyncIndexer` — 异步索引 fake
- `asyncWait` — 等待函数 fake

**新增注入边界**：

```go
// Rag struct 新增字段
type Rag struct {
    // ...existing...
    bgSem     chan struct{}                    // 后台并发信号量（容量=上限）
    bgWG      sync.WaitGroup                  // 等待所有后台任务完成（shutdown 用）
    markFailed func(ctx context.Context, docID int64, err error) // 可注入的失败标记函数
}
```

`markFailed` 默认实现：

```go
func defaultMarkFailed(ctx context.Context, docID int64, err error) {
    g.Log().Errorf(ctx, "background index failed for document %d: %v", docID, err)
    if statusErr := knowledge.UpdateDocumentsStatus(ctx, docID, int(v1.StatusFailed)); statusErr != nil {
        g.Log().Errorf(ctx, "markFailed UpdateDocumentsStatus error: %v", statusErr)
    }
}
```

测试注入 `markFailed` 为记录调用的 fake，验证失败时确实标记。

**并发上限配置**：

```go
// Config 新增
type Config struct {
    // ...existing...
    IndexMaxBackground int `json:"indexMaxBackground"` // 默认 8
}
```

或通过 `Rag` 初始化时设置 `bgSem` 容量。

#### 4.3.3 输入输出与错误契约

**`Index()` 签名不变**：

```go
func (x *Rag) Index(ctx context.Context, req *IndexReq) (ids []string, err error)
```

**后台 goroutine 新行为**：

```go
go func() {
    defer x.bgWG.Done()

    // 获取信号量（尊重并发上限）
    select {
    case x.bgSem <- struct{}{}:
        defer func() { <-x.bgSem }()
    case <-bgCtx.Done():
        x.markFailed(bgCtx, documentsID, bgCtx.Err())
        return
    }

    // recover 覆盖整个后台函数
    defer func() {
        if e := recover(); e != nil {
            g.Log().Errorf(bgCtx, "recover indexAsyncByDocsID panic: %v", e)
            x.markFailed(bgCtx, documentsID, fmt.Errorf("panic: %v", e))
        }
    }()

    // 等待 ES 同步
    if waitFn != nil {
        waitFn()
    } else {
        select {
        case <-time.After(time.Second):
        case <-bgCtx.Done():
            x.markFailed(bgCtx, documentsID, bgCtx.Err())
            return
        }
    }

    // 执行异步索引
    localErr := asyncIdx.indexAsyncByDocsID(knowledgeName, documentsID, docsIDs)
    if localErr != nil {
        x.markFailed(bgCtx, documentsID, localErr)
    }
}()
```

**ctx 策略**：

```go
// 从调用方 ctx 派生，但不随 HTTP 请求结束立即取消
// 使用 context.WithoutCancel 保留 values 但脱离取消链（Go 1.21+）
// 或使用独立 gctx.New() + 超时限制
bgCtx, bgCancel := context.WithTimeout(gctx.New(), 5*time.Minute)
defer bgCancel()  // 在 goroutine 内 defer
```

**决策**：后台任务不应随 HTTP 请求取消（用户提交索引后关闭页面，索引应继续）。使用 `gctx.New()` + 超时上限（5 分钟），但增加 shutdown 联动。

**`indexAsyncByDocsID` 递归重试变更**：

```go
// 修改前（indexer.go:149-154）
if len(searchResp) < len(req.DocsIDs) && req.try > 0 {
    req.try--
    time.Sleep(time.Second)
    return x.indexAsyncByDocsID(ctx, req)
}

// 修改后
if len(searchResp) < len(req.DocsIDs) && req.try > 0 {
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-time.After(time.Second):
    }
    req.try--
    return x.indexAsyncByDocsID(ctx, req)
}
```

#### 4.3.4 并发任务所有权

| 角色 | 实体 |
|---|---|
| 启动者 | `Rag.Index()` 启动 goroutine |
| 取消者 | `bgCtx` 超时（5 分钟）；进程 shutdown 时 `bgCancel()` |
| 等待者 | `Rag.bgWG`（shutdown 时 Wait） |
| 并发限制 | `Rag.bgSem`（带缓冲 channel，容量=IndexMaxBackground） |

**关闭顺序**：
1. 进程收到 shutdown 信号
2. 调用 `Rag.Shutdown(ctx)` → 关闭所有活跃 bgCtx（通过 cancel 函数集合或关闭 done channel）
3. `bgWG.Wait()` 等待所有后台任务退出（带超时）
4. 超时后强制退出（日志记录未完成任务）

**Shutdown 方法**（新增）：

```go
func (x *Rag) Shutdown(ctx context.Context) error {
    // 关闭 done channel 通知所有后台任务
    close(x.shutdownCh)
    // 等待完成
    done := make(chan struct{})
    go func() { x.bgWG.Wait(); close(done) }()
    select {
    case <-done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

#### 4.3.5 部分写入与部分成功处理

| 场景 | 策略 |
|---|---|
| 后台任务成功 | `indexAsyncByDocsID` 内标 Active（1D-1 已修复） |
| 后台任务失败（error） | `markFailed` 标 Failed + 记日志 |
| 后台任务 panic | recover → `markFailed` 标 Failed + 记日志 |
| ctx 超时（5 分钟） | `markFailed` 标 Failed |
| 信号量满（并发上限） | 阻塞等待；shutdown 时 `markFailed` 标 Failed |
| 进程 shutdown | 等待 bgWG；超时后未完成的任务文档保持 Indexing 状态（已知限制） |

**已知限制**：
- 不实现任务恢复（Stage 4）
- 不清理 ES 残留向量
- 进程强杀时文档可能卡在 Indexing 状态（需运维手动重置或 Stage 4 启动时扫描）
- `markFailed` 自身可能失败（DB 不可达），仅记日志

---

## 5. 离线行为测试矩阵

### 5.1 批次 1D-1 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestSaveChunksData_AllSuccess` | 全部写入成功时返回 nil，标记 Indexing | fake insert/update 返回 nil | err==nil; updateDocStatus 调用参数为 StatusIndexing | 否 |
| `TestSaveChunksData_PartialFailure` | 部分失败时返回聚合 error，标记 Failed | fake insert 第 2 次返回 error | err!=nil 含失败数; updateDocStatus 参数为 StatusFailed | 否 |
| `TestSaveChunksData_AllFailure` | 全部失败时返回 error | fake insert 全部返回 error | err!=nil; updateDocStatus 参数为 StatusFailed | 否 |
| `TestSaveChunksData_UpdateStatusFailure` | UpdateDocumentsStatus 自身失败 | fake updateDocStatusFn 返回 error | err!=nil 含 status 更新失败信息 | 否 |
| `TestSaveChunksData_ContextCancel` | ctx 取消时提前返回 | 传入已取消的 ctx | err==ctx.Err(); 不执行后续 insert | 否 |
| `TestSaveChunksData_EmptyChunks` | 空 chunks 直接返回 nil | 无需 fake | err==nil; 不调用 updateDocStatus | 否 |
| `TestIndexAsyncByDocsID_SaveChunksError_Propagates` | SaveChunksData 失败时 indexAsyncByDocsID 返回 error | 通过 asyncIndexer 注入或包级 fake | 返回 error; 不调用 UpdateDocumentsStatus(Active) | 否 |
| `TestIndexAsyncByDocsID_Success_MarksActive` | 全部成功时标 Active | fake 全部成功 | UpdateDocumentsStatus 调用参数为 StatusActive | 否 |

### 5.2 批次 1D-2 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestQA_AllSuccess` | 全部 QA 生成成功 | fake getQAModelFn 返回 fake model（Generate 返回固定内容） | 每个 doc.MetaData 含 QAContent; err==nil | 是（errgroup） |
| `TestQA_PartialFailure_ReturnsError` | 任一失败则整体失败 | fake model 对特定 doc 返回 error | err!=nil 含失败 doc ID; 返回 nil docs | 是 |
| `TestQA_ConcurrencyLimit` | 并发不超过上限 | fake model 阻塞在 channel; 观察最大并行数 | 同时运行的 goroutine 数 ≤ maxConcurrent | 是 |
| `TestQA_Disabled_ReturnsDocsUnchanged` | QA 关闭时直接返回 | ctx 中 QAEnabled=false | docs 不变; err==nil; getQAModelFn 未调用 | 否 |
| `TestQA_ContextCancel_Stops` | ctx 取消时停止 | 取消 ctx 后 fake model 阻塞 | err==ctx.Err() 或含 context.Canceled | 是 |
| `TestQA_EmptyDocs` | 空 docs 直接返回 | 无需 fake | docs==empty; err==nil | 否 |
| `TestQA_ExistingQAContent_Skips` | 已有 QAContent 的 doc 跳过生成 | doc.MetaData 预设 QAContent | getQAModelFn 未对该 doc 调用 | 是 |

**并发测试规则**：
- 使用 buffered channel 收集并行度采样
- `t.Cleanup` 释放所有阻塞 channel
- 超时 5s 后 `t.Fatal`（不静默跳过）
- 不使用固定 sleep 代替同步

### 5.3 批次 1D-3 测试

| 测试名称 | 目标 | 注入方式 | 断言要点 | 并发 |
|---|---|---|---|---|
| `TestIndex_BackgroundFailure_MarksFailed` | 后台失败标记 Failed | fakeAsyncIndexer 返回 error; fake markFailed 记录调用 | markFailed 被调用，docID 正确 | 是 |
| `TestIndex_BackgroundPanic_MarksFailed` | panic 后标记 Failed | fakeAsyncIndexer panic | markFailed 被调用; 不崩溃 | 是 |
| `TestIndex_ContextTimeout_MarksFailed` | 后台 ctx 超时标记 Failed | 注入短超时; fakeAsyncIndexer 阻塞 | markFailed 被调用，err 含 timeout | 是 |
| `TestIndex_ConcurrencyLimit` | 后台并发不超过上限 | bgSem 容量=2; 启动 5 个 Index | 同时运行的后台任务 ≤ 2 | 是 |
| `TestIndex_Shutdown_WaitsBackground` | Shutdown 等待后台完成 | fakeAsyncIndexer 阻塞后释放 | Shutdown 返回 nil; 后台任务全部完成 | 是 |
| `TestIndex_Shutdown_Timeout` | Shutdown 超时返回 error | fakeAsyncIndexer 永久阻塞 | Shutdown 返回 ctx.Err() | 是 |
| `TestIndexAsyncByDocsID_RetryRespectsCtx` | 递归重试检查 ctx | 传入可取消 ctx; 第一次返回不足结果 | 取消后返回 ctx.Err(); 不继续重试 | 是 |
| `TestIndex_Success_NoMarkFailed` | 成功路径不调用 markFailed | fakeAsyncIndexer 返回 nil | markFailed 未被调用 | 是 |

**并发测试规则**（同 5.2）：
- channel 固定事件顺序
- 超时 + t.Cleanup
- 不以 fake 自身测试代替生产逻辑验证
- 不连接真实模型

---

## 6. 验证命令与退出条件

### 每批完成后

**1D-1**：
```bash
cd server
go build ./...
go vet ./...
go test ./internal/logic/knowledge/... -count=1 -v
go test -race ./internal/logic/knowledge/... -count=1
go test ./core/... -count=1 -v          # 确保 indexer_test 不受影响
go test -race ./core -count=1
```

**1D-2**：
```bash
cd server
go build ./...
go vet ./...
go test ./core/indexer/... -count=1 -v
go test -race ./core/indexer/... -count=1
go test ./core/... -count=1 -v          # 确保上层不受影响
go test -race ./core -count=1
```

**1D-3**：
```bash
cd server
go build ./...
go vet ./...
go test ./core -count=1 -v
go test -race ./core -count=1
go test -race ./core -count=10 -run TestIndex_   # 压测并发路径
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
| `./core/retriever`、`./core/rerank` | 保留现状（历史未补证） |

---

## 7. 回滚范围

### 每批回滚策略

| 批次 | 回滚粒度 | 回滚操作 | 不可回滚副作用 |
|---|---|---|---|
| 1D-1 | 文件级 | `git checkout -- chunks.go indexer.go`; 删除 `chunks_test.go` | 无（纯代码变更） |
| 1D-2 | 文件级 | `git checkout -- qa.go async.go config.go indexer_async.go`; 删除 `qa_test.go` | 无 |
| 1D-3 | 文件级 | `git checkout -- indexer.go rag.go`; 恢复 `indexer_test.go` | 无 |

### 批次间依赖回滚

- 回滚 1D-3 不影响 1D-1/1D-2（独立文件）
- 回滚 1D-2 不影响 1D-1（独立文件）
- 回滚 1D-1 需同步回滚 1D-3 中对 `indexAsyncByDocsID` 的错误传播依赖

### 不可回滚的副作用

- 无数据库 schema 变更
- 无持久化状态变更
- 所有变更均为代码级，git revert 即可完整回滚

---

## 8. 阻塞项与未验证项

### 当前已知阻塞项

| 编号 | 描述 | 影响批次 | 状态 |
|---|---|---|---|
| B-1 | `common.GetQAModel` 使用包级缓存 `qaModel`，测试注入需确认并发安全性 | 1D-2 | 待验证 |
| B-2 | eino `compose.InvokableLambda` 对 error 返回的处理语义（是否中断 Graph） | 1D-2 | 待验证 |
| B-3 | `Rag.Shutdown` 的调用时机（需 cmd.go 注册 graceful shutdown hook） | 1D-3 | 待确认是否本阶段实施 |

### 需要澄清的决策点

| 编号 | 决策 | 选项 | 推荐 |
|---|---|---|---|
| D-1 | QA 并发上限默认值 | 4 / 8 / 可配置 | 可配置，默认 4 |
| D-2 | QA 部分失败策略 | 首个失败即取消全部 / 尽力完成 | 首个失败即取消（errgroup 默认） |
| D-3 | 后台 ctx 策略 | `gctx.New()+超时` / `context.WithoutCancel(调用方ctx)` | `gctx.New()+5min超时` |
| D-4 | Shutdown 是否纳入 1D-3 | 是 / 推迟到 Stage 4 | 基础版纳入（bgWG+shutdownCh），完整 graceful 推迟 |
| D-5 | `markFailed` 是否需要额外记录失败原因到 DB | 仅 status / status+error_message 字段 | 仅 status（schema 不变） |
| D-6 | SaveChunksData 部分成功时是否回滚已写入 chunk | 回滚 / 保留 | 保留（不回滚，留待 Stage 4） |

### 未验证的假设

| 编号 | 假设 | 验证方式 |
|---|---|---|
| A-1 | eino Graph Lambda 节点返回 error 时中断后续节点 | 查阅 eino 源码或编写集成测试 |
| A-2 | `errgroup.SetLimit` 在 Go 1.20+ 可用 | 检查 go.mod 的 Go 版本 |
| A-3 | `dao.KnowledgeChunks.Ctx(ctx)` 在 ctx 取消时正确中断 DB 操作 | GoFrame 文档/源码确认 |
| A-4 | QA 关闭时 Retriever 的 QA 向量检索路径不受影响（空字段 vs 无字段） | 确认 ES/Qdrant 对缺失字段的处理 |

---

## 9. 范围限制（负面清单）

本阶段 **不做** 以下事项：

1. ❌ 不引入持久化任务系统（Stage 4）
2. ❌ 不实现任务恢复/重跑机制
3. ❌ 不重写会话模型（Stage 5）
4. ❌ 不顺带重构已验收的 Retriever 同步路径
5. ❌ 不顺带重构已验收的 SSE 生命周期
6. ❌ 不连接真实模型或执行付费调用
7. ❌ 不引入 `PartialActive` 等新状态枚举
8. ❌ 不清理 ES/Qdrant 中的残留向量
9. ❌ 不修改数据库 schema（不新增字段）
10. ❌ 不实现 QA 重试逻辑
11. ❌ 不实现完整的 graceful shutdown（仅基础 bgWG + shutdownCh）
12. ❌ 不处理 History context / Flush 边界（保留现状）
13. ❌ 不补充 core/retriever、core/rerank 的 race 证据
14. ❌ 不修改 `Index()` 的公开 API 签名
15. ❌ 不修改前端代码
16. ❌ 不实现 QA "尽力完成"降级模式（待产品确认后再议）
