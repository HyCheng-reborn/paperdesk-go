# 变更记录

## 2026-10-15 Stage 1C-1 定点收尾：SSE 前端帧兼容恢复 + Controller 建流失败测试（待独立复核）

**基线**：`11e021a3cca840b2225c6228be6a19aaa6a03f55`。不实施 1C-2，不改前端协议，不重构共享 SSE 编排。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 前端帧格式恢复 | `writeDocuments` 从 `event: documents`+`data:` 恢复为 `documents:<JSON>\n\n`；content 为 `data:<JSON>\n\n`；DONE 为 `data:[DONE]\n\n`；error 保留 `event: error`+`data:` 原格式（chat.vue 按行前缀 `documents:`/`data:` 提取） |
| 保留机制 | 统一写入 `writeFrame`、短写检查（`io.ErrShortWrite`）、`failed` 状态与 `errors.Join` 双错误链不变 |
| 精确帧测试 | `TestRealSSEWriter_FrontendCompatFrames` 逐帧断言 `documents:` 行+双换行边界，并从该行切前缀后 `json.Unmarshal` 验证含 document；断言不出现 `event: documents` 帧（非仅字符串包含） |
| Controller 注入边界 | ChatStream 新增实例级、运行期只读的 `chatRetrieve`/`chatAnswerStream`/`chatSSEWrite`，nil 回退生产默认（非可变全局函数） |
| 建流失败测试 | `TestChatStream_AnswerStreamError` 调用实际 ChatStream：离线检索+建流 sentinel；断言 err 非 nil、errors.Is 识别、不返回空成功、不进入 SSE |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/common/stream.go | 恢复前端兼容帧格式；flush 错误边界注释修正 |
| server/core/common/stream_test.go | 新增 frameRecorder 与 `TestRealSSEWriter_FrontendCompatFrames` |
| server/internal/controller/rag/rag_new.go | ControllerV1 新增三个实例级只读注入字段 |
| server/internal/controller/rag/rag_v1_chat_stream.go | ChatStream 解析注入边界/回退默认；新增 `defaultAnswerStream`/`defaultSSEWrite` |
| server/internal/controller/rag/rag_v1_chat_stream_test.go | 新增 ChatStream 建流失败测试 |

### 验证边界（如实保留）

- 保留真实 `MiddlewareHandlerResponse` + `SteamResponse` 集成测试（真实 GoFrame 服务器）
- 刷新错误当前只能经由无返回值 Flusher 调用，“部分刷新错误无法检测”为已知边界，不宣称已全面验证
- content 编码字段实际是 `StreamData.Content`（string），编码不自然失败，未制造无意义测试

### 验证结果（server/，GOFLAGS 空）

- `gofmt -w` 目标文件已格式化，`gofmt -l` 无输出
- `go build ./...` / `go vet ./...` — exit 0
- `go test ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，41 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0（本次新增代码无 race）
- `go test -race ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 1：`core/common` 触发 Windows 预存在 `0xc0000139`（race detector DLL 入口点）问题，非代码数据竞争，根因待确认，该包 race 视为未完成

**race 未验证项**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。普通测试通过不写成 race 通过。

### 回滚说明

回滚 `stream.go`、`stream_test.go`、`rag_new.go`、`rag_v1_chat_stream.go` 到基线 `11e021a`，删除 `rag_v1_chat_stream_test.go`；不涉及前端协议或共享 SSE 编排重构。

---

## 2026-10-14 Stage 1C-1：F04 SSE 流语义与 Controller 错误处理

**基线**：`ae5d5b0cd2dca625d7834a780dc43e9399c550ae`（Stage 1C plan HEAD）

### 计划修正

- 核实 GoFrame v2.9.0 Writeln/Flush 无返回值，改用 `RawWriter()` 检测写入错误
- 核实中间件已保护 SSE：BytesWritten>0 和 Content-Type 检查阻止 JSON 追加
- 明确 Eino v0.6.0 StreamReader Recv/Copy/Close 语义
- 流消费保持请求 context 可取消，不用 WithoutCancel
- 不为初始化阶段 Background 扩大 ChatModelFactory 接口

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/common/stream.go | 引入 `sseWriter` 封装写入错误检测；SteamResponse 区分 EOF/错误；错误时不发 DONE；返回 error；nil chunk 返回 error；SSE error 数据安全编码 |
| server/internal/controller/rag/rag_v1_chat_stream.go | 建流失败返回 error；%w 错误链保留 |
| server/core/common/stream_test.go | 新增 17 个离线测试 |
| design/stage1c-plan.md | 修正计划：API 核实、context 策略、Eino 语义 |

### 测试覆盖（17 个新增）

| 测试 | 说明 |
|---|---|
| TestSteamResponse_NormalEOF | 多 chunk + EOF → 数据事件 + DONE |
| TestSteamResponse_EmptyStream | 空流 → 只发 DONE |
| TestSteamResponse_EmptyContentChunks | 空内容 chunk 被跳过 |
| TestSteamResponse_StreamError_NoDone | chunk 后错误 → 数据 + error 事件，无 DONE |
| TestSteamResponse_NilChunk | nil chunk → error |
| TestSteamResponse_ErrorChain | errors.Is 识别原始错误 |
| TestSteamResponse_SafeErrorEncoding | 换行符安全编码 |
| TestSteamResponse_WriteFail_Documents | documents 写入失败 |
| TestSteamResponse_WriteFail_Data | data 写入失败 |
| TestSteamResponse_WriteFail_Done | DONE 写入失败 |
| TestSteamResponse_WriteFail_ErrorEvent | error 事件写入失败 |
| TestSteamResponse_SubsequentWriteAfterFailure | 失败后后续写入返回 errWriteFailed |
| TestSteamResponse_WithDocuments | documents 事件输出 |
| TestSteamResponse_EventFormat | SSE 事件格式 |
| TestSSEError_EventFormat | error 事件格式 |
| TestMiddleware_NoJSONAppend_AfterSSEStarted | 中间件条件验证 |
| TestMiddleware_Integration_GoFrameServer | GoFrame HTTP 服务集成测试 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/common/... ./internal/controller/rag/... -count=1 -v` — 退出码 0，28 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./internal/controller/rag/...` — 退出码 0
- `go test -race ./core/common/...` — Windows 环境 race detector DLL 问题（非代码问题）

### 回滚说明

回滚 `stream.go`、`rag_v1_chat_stream.go` 到基线 `ae5d5b0`，删除 `stream_test.go`。

---

## 2026-10-14 Stage 1C 计划制定：F03/F04 Chat context 与流式语义

**基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`（Stage 1B 完成后的 HEAD）

### 计划摘要

Stage 1C 覆盖两个问题：
- **F03**：`GetAnswerStream` 将请求 ctx 替换为 `context.Background()`，用户取消后模型/保存协程继续工作
- **F04**：`SteamResponse` 读流失败后仍发 `[DONE]` 并返回 nil；Controller 建流失败可返回空成功

### 调用链追踪

完整调用链：Controller.ChatStream(ctx) → chat.GetAnswerStream(ctx) → **ctx = context.Background()** → llm.Stream(ctx, messages) → StreamReader → SteamResponse(ctx, reader, docs) → SSE 写入

### 问题清单

| 问题 | 文件 | 行号 | 描述 |
|---|---|---|---|
| F03-1 | chat.go | 202 | `ctx = context.Background()` 替换调用方 ctx |
| F03-2 | chat.go | 227 | 后台 goroutine 监听 Background ctx，永远不会取消 |
| F03-3 | chat.go | 39 | defaultChatModelFactory.Create 使用 Background |
| F04-1 | stream.go | 63 | 错误时仍发送 DONE |
| F04-2 | stream.go | 68-92 | SSE 写入不检查错误 |
| F04-3 | stream.go | 89 | writeSSEError 使用 Background 记日志 |
| F04-4 | rag_v1_chat_stream.go | 33 | 建流失败返回空成功 |
| F04-5 | chat.go | 237 | 非 EOF 错误时 append 零值 chunk |

### 批次划分

- **1C-1（F04 SSE 流语义修复）**：推荐首批，修改集中在 stream.go 和 controller
- **1C-2（F03 Chat context 修复）**：第二批，涉及 context 传递和后台 goroutine 生命周期

### 详细计划

[design/stage1c-plan.md](design/stage1c-plan.md)

---

## 2026-10-14 Stage 1B-3 独立复核通过

**复核基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`
**复核方式**：源码与提交日志复核，未独立重跑 Go 测试

**复核结论**：
- F02 异步闭包竞态修复正确
- 后台输入快照机制正确
- 同步/异步边界行为正确
- `asyncWait` 注入机制设计合理

**Stage 1B 整体状态**：三个批次均已完成，可标记完成。

**race 证据说明**：`core/retriever`、`core/rerank` 两个子包此前 race 检测异常仍未补证。

---

## 2026-10-14 Stage 1B-3：F02 Indexer 异步闭包定点收尾（待独立复核）

**基线**：`f26b3a90b97b26bf2220b677dbddd689b0cb4002`（1B-3 实现提交）。保留已实现的后台局部错误与输入快照，不实施 Stage 1C/1D，不引入持久化任务系统。本批为定点收尾：默认测试、等待注入、int64、完成边界与并发隔离。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 默认测试 | 移除 indexer_test.go 的 `//go:build offline`，9 个 Indexer 行为测试改为默认运行，无需标签 |
| 等待注入 | 后台等待优先调用注入的 `asyncWait`，未注入时回退生产默认一秒；不再先固定 Sleep 再调用注入函数 |
| recover 范围 | recover 移到 goroutine 顶部（`ctxN` 之后、等待与执行之前），覆盖整个后台函数 |
| 注入引用快照 | 启动 goroutine 前将 `x.asyncWait`、`x.asyncIndexer` 保存为局部只读引用 |
| DocumentsId int64 | `asyncIndexer` 接口、`prodAsyncIndexer`、fake 的 documentsID 参数直接用 int64，删除 Sprintf/Sscanf 与被忽略的解析错误 |
| 完成边界 | AsyncFailure 等待后台确实返回失败后再断言同步结果；启动后台的测试均在 cleanup 释放阻塞并等待完成，超时报告失败 |
| 错误检查 | 所有 Index 返回错误都检查，去掉 `ids, _` |
| 标量快照 | 修改 req 测试同时改 KnowledgeName 与 DocumentsId，验证后台收到原值 |
| 并发隔离 | 同一 Rag 并发不同 URI/KnowledgeName/DocumentsId，同步 fake 按输入返回不同 IDs，后台逐项记录参数验证一一对应、恰好一次、无串用；用 channel/完成信号，删除固定 sleep 与计数验证 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/indexer.go | 等待注入优先/回退一秒；recover 前置覆盖全函数；goroutine 启动前快照 `waitFn`/`asyncIdx`；documentsID 全程 int64，删除字符串往返 |
| server/core/indexer_test.go | 去掉 offline 标签；9 个测试注入 `asyncWait` 与 int64 参数；修正完成边界与 cleanup；新增 AsyncWait/BackgroundPanic/并发隔离测试 |

### 测试覆盖（默认运行，共 9 个）

| 测试 | 说明 |
|---|---|
| TestIndex_SyncFailure_NoAsync | 同步失败不启动后台 |
| TestIndex_SyncSuccess_AsyncStillBlocked | Index 先返回，后台阻塞期间未完成 |
| TestIndex_AsyncFailure_SyncReturnUnchanged | 等待后台真实返回失败后断言同步 ids/err 不变，且后台恰好一次 |
| TestIndex_ModifyReqAfterReturn_AsyncGetsSnapshot | 返回后同时改 KnowledgeName+DocumentsId，后台收到原始标量快照 |
| TestIndex_ModifyReturnedIDs_AsyncDocsIDsUnchanged | 改返回 ids，后台 docsIDs 为原始快照 |
| TestIndex_AsyncModifiesOwnDocsIDs_CallerIDsUnchanged | 后台改自己 docsIDs，调用方 ids 不变 |
| TestIndex_AsyncWaitInvoked_BeforeBackground | 注入等待被调用且先于后台执行（替代固定 sleep） |
| TestIndex_BackgroundPanic_Recovered | 等待阶段 panic 被 recover 覆盖，不影响 Index 返回 |
| TestIndex_ConcurrentCalls_Isolation | 同一 Rag 并发不同输入，逐项验证一一对应、恰好一次、无串用 |

### 验证结果（server/ 目录，默认无标签；GOFLAGS 为空）

- `gofmt -w` 目标文件已格式化，`gofmt -l` 无输出
- `go test ./core -count=1 -v` — 退出码 0，28 个测试全部通过（9 Indexer + 19 Retrieve），0 失败；默认 core 日志同时含 Indexer 与 Retrieve
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- `go test -race ./core -count=25 -run TestIndex_` — 退出码 0，无 FAIL / DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0

**race 证据范围**：race 仅在 `core` 包（`go test -race ./core`）通过并给出证据。`core/retriever`、`core/rerank` 两个子包此前 race 检测异常仍未补证，普通测试通过不写成 race 通过。

**证据命令记录**：`verification/stage1b3/` 下每个日志头部记录实际命令、GOFLAGS 与退出码；旧 `-tags offline` 运行结果（旧 test-core.log、race.log）已删除，避免把带标签执行记成无标签通过。

### 回滚说明

回滚 `indexer.go`、`indexer_test.go` 到基线 `f26b3a9` 即可；不涉及 Stage 1C/1D 或持久化任务系统。

---

## 2026-10-14 Stage 1B-2：F01 Retriever 并发测试收尾（已通过独立源码与提交日志复核）

**基线**：`d3e3e07ebbc7b73569e8afeb1b0f677a5d98517a`。仅测试收尾，保持已修正的 `Retrieve` 编排逻辑，不重新设计并发方案，不实施 1B-3。

**复核通过基线（1B-2 完成提交）**：`98770d33ee18c280bbb8c771acdeb44749182ef8`。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 字段范围恢复 | 公开字段 `ExcludeIDs` 恢复为原有私有字段 `excludeIDs`，同步 retriever.go 内所有引用；测试属 package core，无需公开接口 |
| WorkerFails_OthersCanceled | 定点重写：三轮 worker/Generate 事件编排，释放第一轮返回 sentinel；断言取消被观察、已启动 worker 在返回前完成、`errors.Is` 识别 sentinel |
| Round1WorkerBlocked_Round2RewriteFails | 定点重写：worker 先观察取消再等待独立释放；未释放时 Retrieve 不返回；改用 `errors.Is` 而非字符串匹配 |
| CancelWinsOverSuccess | 使用 `workerStarted` 信号触发取消，删除固定 sleep |
| InFlightCancel / Timeout | 补回进行中取消与超时覆盖，分别断言 `context.Canceled`、`context.DeadlineExceeded`；用完成信号而非 goroutine 计数 |
| 切片隔离 | worker 修改 `excludeIDs[0]` 前先断言原始值；最后校验输入独立快照不变；保留同一 Rag 并发不同 query 测试 |
| 测试清理 | 删除名不副实的 `NoSharedFieldWrite`，改名 `InjectedDepsRemainUnchanged`，仅验证已注入依赖不变，不声称覆盖 nil 默认路径 |
| 泄漏防护 | 所有 channel 等待有超时；`t.Cleanup` 保证断言失败时释放阻塞任务并等待结束 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/retriever.go | `ExcludeIDs` → `excludeIDs` 及引用同步 |
| server/core/retriever_test.go | 重写协调测试、补回取消/超时、加强切片隔离、清理测试 |
| PROJECT_PROGRESS.md | 添加 Stage 1B-2 测试收尾记录，更新批次状态 |
| CHANGES.md | 添加本记录 |

### 验证结果

- `gofmt` — 目标文件已格式化，`gofmt -l` 无输出
- `go test ./core -count=1 -v` — 退出码 0，19 个 Retrieve 测试全部通过，0 失败
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- `go test -race ./core -count=10 -run <6 个协调/隔离测试>` — 60 PASS，0 FAIL，无 DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0

**race 证据范围**：本轮 race 仅在 `core` 包通过并给出证据；`core/retriever`、`core/rerank` 此前 race 异常仍未补证，普通测试通过不写成 race 通过。

### 回滚说明

仅测试收尾 + 字段改回私有，回滚恢复 `retriever.go`、`retriever_test.go` 到基线 `d3e3e07` 即可，不涉及编排逻辑。

## 2026-10-14 Stage 1B-1：F05 Rerank HTTP ctx 绑定与校验

### 修复内容

| 修复项 | 说明 |
|---|---|
| ctx 绑定 | HTTP 请求使用 `http.NewRequestWithContext` 绑定调用方 ctx，不再使用 `context.Background()` |
| 有界超时 | `context.WithTimeout(ctx, 30s)`，调用方更早 deadline 优先生效 |
| 响应大小限制 | `http.MaxBytesReader(nil, resp.Body, 10MB+1)` 防止内存溢出 |
| 状态码校验 | 非 200 返回 `rerank API returned status %d` |
| JSON 校验 | 解析失败返回 `invalid JSON` 错误 |
| 结果校验 | 检查 nil result、missing index、index 越界、重复 index、missing score、NaN/Inf score |
| HTTPDoer 注入 | 接口注入支持测试 mock，默认使用 `http.DefaultClient` |
| 输入校验 | 空 docs、nil document、非法 topK、已取消 ctx 均在 HTTP 调用前检查 |
| 无部分修改 | 结果校验失败时不修改输入 docs 的 score |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/rerank/rerank.go | 重构为 Rerank 结构体；新增 HTTPDoer 接口、RerankConfig；ctx 绑定 + 30s 超时；MaxBytesReader；状态码/JSON/结果全面校验 |
| server/core/retriever.go | 更新调用方适配新 API：`rerank.NewRerank(cfg, nil).Rerank(ctx, ...)` |
| server/core/rerank/rerank_test.go | 更新在线测试适配新 API |
| server/core/rerank/rerank_http_test.go | 新增 25 个离线 httptest 测试 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加 Stage 1B-1 记录 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/rerank/... -count=1 -v` — 退出码 0，25 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./core/rerank/...` — Windows 环境 race detector DLL 问题（非代码问题）

### 回滚说明

修改涉及 rerank 包重构和调用方适配，回滚需同时恢复 `rerank.go`、`retriever.go`、`rerank_test.go` 并删除 `rerank_http_test.go`。

---

## 2026-10-14 Stage 1-Pre 独立复核通过

**复核基线**：`db0c5971d137ddabcc10ae5fb0c4b9baa17e1b98`
**复核方式**：源码复核，未独立重跑 Go 测试

**复核结论**：
- F06 nil 访问修复逻辑正确
- 四类输入行为定义清晰
- 无副作用保证：校验失败不触发文件保存、DB 插入、后台任务

**行为修正记录**：
- 默认模式 File+URL 同时存在时行为为"File 优先，URL 忽略"
- PaperDesk 模式目前未在生产启用
- `paperDeskModeOverride` 为临时测试机制

---

## 2026-10-14 Stage 1-Pre：F06 File/URL 参数校验

### 修复内容

| 修复项 | 说明 |
|---|---|
| F06 nil 访问修复 | `rag_v1_indexer.go:29` 在 `if req.File != nil` 块外访问 `req.File.Filename`，当仅通过 URL 提交时触发 panic。已通过前置参数校验消除 |
| 参数校验前置 | 新增 `validateIndexerInput()` 在任何副作用（文件保存、SQL 写入、索引、后台任务）之前校验 File/URL |
| 四类输入定义 | 无 File 无 URL → 参数错误；仅 File → 正常处理；仅 URL → 默认模式正常/PaperDesk 拒绝；File+URL → 默认模式正常/PaperDesk 拒绝 |
| PaperDesk 模式 | `isPaperDeskMode()` 临时返回 false，待 profile 配置实现后接入。通过 `paperDeskModeOverride` 支持测试切换 |
| URL 模式文件名 | URL 输入时用 `path.Base(req.URL)` 提取文件名，避免 nil 访问 |
| 无副作用保证 | 校验失败直接返回 error，不触发文件保存、DB 插入、后台任务 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/controller/rag/rag_v1_indexer.go | 新增 `validateIndexerInput()`、`isPaperDeskMode()`、`paperDeskModeOverride`；修复 nil 访问；URL 模式文件名提取 |
| server/internal/controller/rag/rag_v1_indexer_test.go | 新增 11 个测试：四类输入 + PaperDesk 模式 + 边界情况 + 无副作用验证 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加 Stage 1-Pre 记录 |
| design/stage1-plan.md | 标记 1-Pre 为已完成 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./internal/controller/rag/... -count=1 -v` — 退出码 0，11 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- 非法请求不触发文件保存、SQL 写入、后台任务

### 回滚说明

单文件修改 + 测试文件，回滚简单：恢复 `rag_v1_indexer.go` 原始内容，删除测试文件。

---

## 2026-10-13 Stage 1A 第二轮复核修复

### 修复内容

| 修复项 | 说明 |
|---|---|
| Migrate 无条件调用 | cmd.go 普通启动不再调用 dao.Migrate()；保留 InitWithMigrate() 兼容入口 |
| 在线测试隔离 | rerank_test.go 和 youcom_integration_test.go 添加 `//go:build online`；rag_test.go 改为 `//go:build integration && online` |
| chat_model_test.go | 移除 integration 标签，改为使用测试自有配置的单元测试 |
| MustGet 替换 | dao/retriever/chat 三个包所有 MustGet 替换为 Get + error 处理；缺配置返回 error 而非 panic |
| GetMysqlConfig panic 保护 | g.DB() 在无配置时 panic，使用 defer/recover 捕获并返回 error |
| 行为测试 | 重写 17 个测试：预初始化状态验证、Init 失败后状态不变、Reset 安全性、GetDsn 返回 error |
| 失败清理协调 | 新增 dao.Reset()、rag.Reset()、chat.Reset()；cmd.go 级联清理（chat 失败 → rag.Reset + dao.Reset） |
| 同步一致性 | 全部改用 sync.RWMutex；GetDsn 返回 (string, error)；IsInitialized/GetRagSvr/GetChat 使用 RLock |
| chat-history AutoMigrate 边界 | chat.Init() 先检查 dao.GetDsn() 返回 error，明确标注第三方包 AutoMigrate 边界 |
| Windows 临时目录诊断 | `go test ./...` 的 fork/exec Access denied 为 Windows 临时目录权限问题，单独运行通过 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | MustGet→Get；GetDsn 返回 (string, error)；新增 Reset()；RWMutex；GetMysqlConfig panic 保护 |
| server/internal/logic/rag/retriever.go | MustGet→Get；新增 Reset()；RWMutex；配置缺失返回 error |
| server/internal/logic/chat/chat.go | MustGet→Get；新增 Reset()；RWMutex；GetDsn error 处理；配置缺失返回 error |
| server/internal/cmd/cmd.go | 移除 dao.Migrate() 无条件调用；失败时级联 Reset() |
| server/internal/dao/dao_test.go | 重写为行为测试（7 个） |
| server/internal/logic/rag/retriever_test.go | 重写为行为测试（5 个） |
| server/internal/logic/chat/chat_test.go | 重写为行为测试（5 个） |
| server/core/rerank/rerank_test.go | 添加 `//go:build online` |
| server/core/websearch/youcom_integration_test.go | 添加 `//go:build online` |
| server/core/rag_test.go | 改为 `//go:build integration && online` |
| server/core/common/chat_model_test.go | 移除 integration 标签，改为独立单元测试 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加第二轮修复记录 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，17 个测试全部通过
- `go test ./core/retriever/...` — 退出码 0（单独运行）
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — core/retriever 和 core/websearch 因 Windows 临时目录权限问题 fork/exec 失败（非测试逻辑问题）
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

整批协调回滚：恢复 MustGet、移除 Reset()、恢复 Migrate() 调用、移除 build tags。

---
## 2026-10-13 Stage 1A 复核修复（第一轮）

### 修复内容

| 修复项 | 说明 |
|---|---|
| AutoMigrate 分离 | InitDB() 仅连接，新增 Migrate() 建表；新增 InitWithMigrate() 兼容入口 |
| 错误传播与清理 | cmd.go 中 Fatalf 改为返回带阶段信息的 error，失败时 dao.Close() 清理 |
| 原子初始化 | 局部变量构建完整实例后才赋值全局变量；失败时全局变量保持零值 |
| 重复初始化保护 | 重复调用 Init() 返回 error；各包新增 IsInitialized() |
| 未初始化访问保护 | GetDsn() 返回空字符串；GetRagSvr()/GetChat() 返回 nil |
| Close 函数 | dao 包新增 Close()；Database 接口新增 Close() 方法 |
| 测试隔离 | core/rag_test.go 和 chat_model_test.go 添加 //go:build integration |
| 测试增强 | 新增 GetDsn/Close/IsInitialized/Migrate 未初始化行为测试 |
| F 编号修正 | 1C 对应 F03/F04；1D 对应 F07/F08/F09 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，14 个测试全部通过
- 离线测试不触发数据库/向量服务/OpenAI 连接

---
## 2026-10-13 Stage 1A：初始化副作用清理 + 离线测试隔离

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | 移除 init()，保留 InitDB() error，添加文档注释 |
| server/internal/logic/rag/retriever.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/logic/chat/chat.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/cmd/cmd.go | 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init() |

### 新增文件

| 文件 | 说明 |
|---|---|
| server/internal/dao/dao_test.go | 验证 db 在 InitDB() 前为 nil |
| server/internal/logic/rag/retriever_test.go | 验证 ragSvr 在 Init() 前为零值，Init 签名正确 |
| server/internal/logic/chat/chat_test.go | 验证 chat 在 Init() 前为 nil，Init 签名正确 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

恢复 init() 中的调用即可。每个文件独立可回滚。

---
## 2026-10-13 Stage 0 补证 + Stage 1 计划

### 新增文件

| 文件 | 说明 |
|---|---|
| design/stage0-baseline.md | Stage 0 基线核对报告 |
| design/stage1-plan.md | Stage 1 分批实施计划 |
| verification/stage0/v01-build-evidence.md | V01 构建证据 |
| verification/stage0/v02-chat-history.md | V02 chat-history 接口清单 |
| verification/stage0/v06-goframe-dao.md | V06 GoFrame DAO 文件清单 |
| verification/stage0/init-side-effects.md | init() 副作用追踪 |
| verification/stage0/es-version-risk.md | ES 版本风险记录 |
| PROJECT_PROGRESS.md | 项目进度记录 |
| CHANGES.md | 本文件 |

### Stage 0 结论

- V01：依赖解析和编译通过（go build/vet 退出码 0）
- V02：chat-history 能力严重不足（6 项需求中 4 项不支持）
- V05：ES 版本不在官方保证区间（客户端 8.16.0 vs 服务端 8.11.3）
- V06：12 生成 + 10 手写文件已分类
- V07：待补证（ZIP 未找到）
- 3 个 init() 副作用已追踪，显式初始化方案已制定

### Stage 1 计划

- 5 个批次：1-Pre → 1A → 1B → 1C → 1D
- 推荐首批：1-Pre（F06 nil 修复）和 1A（init 副作用清理）可并行
- 覆盖 F01-F09 + F12

### 方案文档版本线

- v1.0 → v1.1 → v1.1.1 → v1.1.2（当前）
- 详见 docs/go-rag_PaperDesk_改进方案_v1.1.2.md
