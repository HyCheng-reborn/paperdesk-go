# 项目进度记录

## 当前状态

**日期**：2026-10-18
**阶段**：Stage 1-Pre、1A、1B（含 1B-1/1B-2/1B-3）、1C（含 1C-1/1C-2）、1D-1、1D-2 均已通过复核（1D-2 基线 d7618e5），Stage 1D-3-A 定点修正完成待独立复核（B/C 待实施），Stage 1 整体尚未完成
**复核方式**：源码及提交日志复核，复核方未独立重跑测试
**验收基线**：Stage 1A `3b8f7a0efc27595d1b50aa2addb2523ed20a2ef0`；Stage 1C-2 `26e55ee2ea43daf703ed72da60425ff5b7980cba`；Stage 1D-1 `644642712bcb04e820e345598a98d99c78277175`
**说明**：`7fb5b77` 为 Stage 1C-2 验收记录及生命周期 race 补证提交（纯文档/证据变更），不替换业务验收基线 `26e55ee`

## Stage 0：事实基线与只读核对

**状态**：静态部分完成

| 核实项 | 状态 | 结论 |
|---|---|---|
| V01 Eino 依赖兼容性 | 已完成 | go build/vet 通过，依赖解析无冲突 |
| V02 chat-history 接口 | 已完成 | 能力严重不足，Stage 5 需替换 |
| V03 MySQL SKIP LOCKED | 未执行 | 归属 Stage 4 |
| V04 百炼 endpoint | 未执行 | 归属 Stage 2 |
| V05 ES 版本兼容（静态） | 存在风险 | 客户端 8.16.0 vs 服务端 8.11.3 |
| V05 ES 版本兼容（运行） | 未执行 | 归属 Stage 1 或 0C |
| V06 GoFrame DAO 清单 | 已完成 | 12 生成 + 10 手写 = 22 文件 |
| V07 快照与 commit 差异 | 待补证 | ZIP 未找到 |

**详细报告**：[design/stage0-baseline.md](design/stage0-baseline.md)
**证据目录**：[verification/stage0/](verification/stage0/)

## Stage 1：初始化副作用清理 + 并发安全修复

**状态**：1-Pre/1A/1B/1C/1D-1/1D-2 已通过复核，1D-3-A 定点修正完成待独立复核（B/C 待实施）

### 批次计划

| 批次 | 内容 | F 编号 | 依赖 | 状态 |
|---|---|---|---|---|
| 1-Pre | F06 File/URL nil 访问修复 | F06 | 无 | **✅ 已完成** |
| 1A | init 副作用清理 + 离线测试隔离 | F12 | 无 | **✅ 已通过复核（基线 3b8f7a0）** |
| 1B-1 | F05 Rerank HTTP ctx 绑定与校验 | F05 | 1A | **✅ 已完成** |
| 1B-2 | F01 Retriever 并发安全 | F01 | 1A | **✅ 已通过独立源码与提交日志复核** |
| 1B-3 | F02 Indexer 异步闭包 | F02 | 1A | **✅ 已通过独立源码与提交日志复核** |
| 1C | Chat context + 流式 EOF + 错误语义 | F03/F04 | 1A | **1C-1 ✅ 已通过；1C-2 ✅ 已通过源码及提交日志复核（未独立重跑测试）** |
| 1D | Chunk 写入错误传播 + QA 并发 + goroutine 止损 | F07/F08/F09 | 1A, 1B | **1D-1 ✅ 已通过独立复核（基线 6446427）；1D-2 ✅ 已通过独立复核（基线 d7618e5）；1D-3-A ✅ 定点修正完成待独立复核；B/C 待实施** |

### Stage 1-Pre 完成记录

**日期**：2026-10-14
**基线**：3b8f7a0（Stage 1A 复核通过）
**状态**：✅ 已完成

**修改文件**：
- `server/internal/controller/rag/rag_v1_indexer.go` — 新增 `validateIndexerInput()` 参数校验；修复 `req.File.Filename` nil 访问；URL 模式下用 `path.Base` 提取文件名
- `server/internal/controller/rag/rag_v1_indexer_test.go` — 新增 11 个测试覆盖四类输入 + PaperDesk 模式 + 无副作用验证

**四类输入行为**：

| 输入情况 | 默认模式 | PaperDesk 模式 |
|---|---|---|
| File 和 URL 均缺失 | 返回参数错误 | 返回参数错误 |
| 仅 File | 正常处理 | 正常处理 |
| 仅 URL | 正常处理 | 返回错误 |
| File + URL | 正常处理 | 返回错误 |

**验证结果**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./internal/controller/rag/... -count=1 -v` — 退出码 0，11 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- 非法请求不触发文件保存、SQL 写入、后台任务

**PaperDesk 模式策略**：当前 `isPaperDeskMode()` 返回 false（临时策略），待 PaperDesk profile 配置实现后接入。

**证据目录**：[verification/stage1pre/](verification/stage1pre/)

### Stage 1B-1 完成记录

**日期**：2026-10-14
**基线**：`d4d8259d78036662ac9853e6b7fac721c8445569`（Stage 1B plan）
**状态**：✅ 已完成

**修改文件**：
- `server/core/rerank/rerank.go` — 重构为 Rerank 结构体；HTTPDoer 接口注入；ctx 绑定 + 30s 有界超时；MaxBytesReader 10MB 响应限制；状态码校验；JSON/结果/索引/分数全面校验
- `server/core/retriever.go` — 更新调用方适配新 API
- `server/core/rerank/rerank_test.go` — 更新在线测试适配新 API
- `server/core/rerank/rerank_http_test.go` — 新增 25 个离线 httptest 测试

**修复内容**：

| 修复项 | 说明 |
|---|---|
| ctx 绑定 | HTTP 请求使用 `http.NewRequestWithContext` 绑定调用方 ctx |
| 有界超时 | `context.WithTimeout(ctx, 30s)`，调用方更早 deadline 优先生效 |
| 响应大小限制 | `http.MaxBytesReader(nil, resp.Body, 10MB+1)` 防止内存溢出 |
| 状态码校验 | 非 200 返回明确错误信息 |
| JSON 校验 | 解析失败返回 `invalid JSON` 错误 |
| 结果校验 | 检查 nil result、missing index、index 越界、重复 index、missing score、NaN/Inf score |
| HTTPDoer 注入 | 接口注入支持测试 mock，默认使用 `http.DefaultClient` |
| 输入校验 | 空 docs、nil document、非法 topK、已取消 ctx 均在 HTTP 调用前检查 |
| 无部分修改 | 结果校验失败时不修改输入 docs 的 score |

**验证结果**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/rerank/... -count=1 -v` — 退出码 0，25 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./core/rerank/...` — Windows 环境 race detector DLL 问题（非代码问题）

**证据目录**：[verification/stage1b1/](verification/stage1b1/)

### Stage 1B-2 测试收尾记录

**日期**：2026-10-14
**基线**：`d3e3e07ebbc7b73569e8afeb1b0f677a5d98517a`（1B-2 收尾起点）
**复核通过基线（1B-2 完成提交）**：`98770d33ee18c280bbb8c771acdeb44749182ef8`
**状态**：✅ 测试收尾完成，已通过独立源码与提交日志复核

**范围约束**：仅测试收尾，保持已修正的 `Retrieve` 编排逻辑不变，不重新设计并发方案，不实施 1B-3。

**修改文件**：
- `server/core/retriever.go` — 将本批公开字段 `ExcludeIDs` 恢复为原有私有字段 `excludeIDs`，同步 `Retrieve`/`retrieveDoOnce`/`retrieve` 内的引用（深拷贝、ES/Qdrant 过滤、BM25 传参）
- `server/core/retriever_test.go` — 定点重写协调测试、补回取消/超时覆盖、加强切片隔离断言、清理名不副实测试（package core 内测试，可直接访问私有字段，无需公开接口）

**测试收尾内容**：

| 项目 | 说明 |
|---|---|
| 字段范围恢复 | `ExcludeIDs` → `excludeIDs`，测试属 package core，无需公开接口 |
| WorkerFails_OthersCanceled | 第一轮 worker 等待释放失败信号；第二轮 worker 启动后阻塞等待取消；第三轮 Generate 发出"已进入"信号后等待取消；两者均进入后释放第一轮返回 sentinel；断言取消被观察、已启动 worker 在返回前完成、`errors.Is` 识别 sentinel |
| Round1WorkerBlocked_Round2RewriteFails | 第二轮 Generate 等待第一轮 worker 启动后返回 sentinel；worker 先观察取消再等待独立释放；未释放时 Retrieve 不返回，释放后返回原始错误；用 `errors.Is` 检查，不用字符串匹配 |
| CancelWinsOverSuccess | 使用 `workerStarted` 信号触发取消，删除固定 sleep；下游取消后返回成功仍判取消 |
| InFlightCancel（补回） | 进行中取消，断言 `context.Canceled` 且已启动 worker 全部结束（用完成信号，不用 goroutine 计数） |
| Timeout（补回） | 超时 deadline，断言 `context.DeadlineExceeded` |
| 切片隔离 | worker 修改 `excludeIDs[0]` 前先断言为原始值；最后检查输入的独立快照保持不变；保留同一 Rag 并发不同 query 测试 |
| 测试清理 | 删除名不副实的 `NoSharedFieldWrite`，改名为 `InjectedDepsRemainUnchanged`，仅验证已注入依赖保持不变，不声称覆盖 nil 默认路径 |
| 泄漏防护 | 所有 channel 等待有超时；`t.Cleanup` 保证断言失败时仍释放阻塞任务并等待结束 |

**验证结果**：
- `gofmt` — 目标文件已格式化，`gofmt -l` 无输出
- `go test ./core -count=1 -v` — 退出码 0，core 包 19 个 Retrieve 测试全部通过，0 失败
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- 协调测试定点压测 `go test -race ./core -count=10 -run <6 个协调/隔离测试>` — 60 PASS，0 FAIL，无 DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0

**race 证据范围说明**：本轮 race 仅在 `core` 包通过并给出证据。`core/retriever`、`core/rerank` 此前 Windows race 检测异常仍未补证，普通测试通过不代表其 race 通过，二者 race 结论保持待验证。

**证据目录**：[verification/stage1b2/](verification/stage1b2/)

### Stage 1B-3 定点收尾记录

**日期**：2026-10-14
**基线**：`f26b3a90b97b26bf2220b677dbddd689b0cb4002`（Stage 1B-3 实现提交；父链含 1B-2 通过基线 `98770d3`）
**状态**：✅ 定点收尾完成，已通过独立源码与提交日志复核
**范围约束**：保留已实现的后台局部错误与输入快照，不实施 Stage 1C/1D，不引入持久化任务系统。

**修改文件**：
- `server/core/indexer.go` — 新增 `syncIndexer`/`asyncIndexer` 接口及生产实现；`Index` 异步闭包使用局部错误变量、固定后台输入快照；定点收尾：等待注入优先/否则一秒、recover 前置覆盖全函数、documentsID 全程 int64 删除字符串往返
- `server/core/rag.go` — `Rag` 结构体新增 `syncIndexer`、`asyncIndexer`、`asyncWait` 字段；`New()` 初始化注入边界
- `server/core/indexer_test.go` — 移除 `//go:build offline` 改默认运行；9 个测试注入 `asyncWait` 与 int64；修正完成边界与 cleanup 超时报告；新增 AsyncWait/BackgroundPanic/并发隔离测试

**修复内容**：

| 修复项 | 说明 |
|---|---|
| 异步闭包竞态 | goroutine 使用局部 `localErr`，不再写外层命名返回变量 `err` |
| 后台输入快照 | goroutine 启动前复制 `knowledgeName`、`documentsID`、`docsIDs`；goroutine 不读取 `req` 或共享 `ids` 切片 |
| 同步失败不启动后台 | 同步索引失败直接返回 error，不启动后台任务 |
| 同步成功不等待后台 | 同步成功返回 `ids`，后台独立执行 QA/embedding |
| 误导注释删除 | 删除“崩溃场景几乎不会出现、可以忽略”，改为“当前内存 goroutine 无持久化恢复，仍由后续 Stage 4 解决” |
| 可注入边界 | `syncIndexer`/`asyncIndexer` 接口支持测试 fake；`asyncWait` 可注入等待机制 |

**测试覆盖**：

| 测试 | 说明 |
|---|---|
| TestIndex_SyncFailure_NoAsync | 同步失败不启动后台任务 |
| TestIndex_SyncSuccess_AsyncStillBlocked | 同步成功时后台仍阻塞，Index 已返回 |
| TestIndex_AsyncFailure_SyncReturnUnchanged | 后台失败不改变同步返回的 ids/error |
| TestIndex_ModifyReqAfterReturn_AsyncGetsSnapshot | 修改调用方 req 后，后台仍收到原始标量快照 |
| TestIndex_ModifyReturnedIDs_AsyncDocsIDsUnchanged | 修改返回 ids 元素后，后台 DocsIDs 不变 |
| TestIndex_AsyncModifiesOwnDocsIDs_CallerIDsUnchanged | 后台修改自己的 DocsIDs 后，调用方返回 ids 不变 |
| TestIndex_AsyncWaitInvoked_BeforeBackground | 注入等待被调用且先于后台执行（替代固定 sleep） |
| TestIndex_BackgroundPanic_Recovered | 等待阶段 panic 被 recover 覆盖，不影响 Index 返回 |
| TestIndex_ConcurrentCalls_Isolation | 同一 Rag 并发不同 URI/KB/DocId，逐项验证一一对应、恰好一次、无串用 |

**验证结果**（server/，默认无标签；GOFLAGS 为空）：
- `go test ./core -count=1 -v` — 退出码 0，28 个测试全部通过（9 Indexer + 19 Retrieve），0 失败；默认 core 日志同时含 Indexer 与 Retrieve
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- `go test -race ./core -count=25 -run TestIndex_` — 退出码 0，无 FAIL / DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0
- `gofmt -l` 目标文件无输出

**race 证据范围说明**：race 仅在 `core` 包（`go test -race ./core`）通过并给出证据。`core/retriever`、`core/rerank` 此前 race 检测异常仍未补证，普通测试通过不代表其 race 通过。

**证据命令记录**：`verification/stage1b3/` 每个日志头部记录实际命令、GOFLAGS 与退出码；旧 `-tags offline` 的 test-core.log、race.log 已删除，避免将带标签执行记成无标签通过。

**证据目录**：[verification/stage1b3/](verification/stage1b3/)

### Stage 1B-3 独立复核记录

**日期**：2026-10-14
**复核基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`（Stage 1B 完成后的 HEAD）
**复核方式**：源码与提交日志复核，未独立重跑 Go 测试

**复核结论**：
- F02 异步闭包竞态修复正确：goroutine 使用局部 `localErr`，不再写外层命名返回变量
- 后台输入快照机制正确：goroutine 启动前复制标量，不读取共享 `req` 或 `ids` 切片
- 同步失败不启动后台、同步成功不等待后台的行为正确
- `asyncWait` 注入机制设计合理，测试覆盖充分（9 个 Indexer 测试）
- documentsID 全程 int64，删除字符串往返转换

**Stage 1B 整体状态**：三个批次（1B-1 F05、1B-2 F01、1B-3 F02）均已通过复核，可标记完成。

**race 证据说明**：`core/retriever`、`core/rerank` 两个子包此前 Windows race 检测异常仍未补证，普通测试通过不代表其 race 通过，二者 race 结论保持待验证。

### Stage 1C-1 完成记录

**日期**：2026-10-14
**基线**：`ae5d5b0cd2dca625d7834a780dc43e9399c550ae`（Stage 1C plan HEAD）
**状态**：✅ 已完成，已通过独立源码复核（另见下方"Stage 1C-1 定点收尾记录"）

**修改文件**：
- `server/core/common/stream.go` — 引入 `sseWriter` 封装写入错误检测；SteamResponse 区分 EOF/错误；错误时不发 DONE；返回 error；nil chunk 返回 error；SSE error 数据安全编码
- `server/internal/controller/rag/rag_v1_chat_stream.go` — 建流失败返回 error；%w 错误链保留
- `server/core/common/stream_test.go` — 新增 17 个离线测试
- `design/stage1c-plan.md` — 修正计划：API 核实、context 策略、Eino 语义

**修复内容**：

| 修复项 | 说明 |
|---|---|
| 正常 EOF 才发 DONE | 只有 `io.EOF` 才发送 `[DONE]`，错误时不发 |
| 非 EOF 错误返回 error | 带错误链的 `fmt.Errorf("stream read failed: %w", err)` |
| 建流失败返回 error | 不再返回 `&ChatStreamRes{}, nil` 空成功 |
| 写入错误检测 | 通过 `RawWriter()` 直接写入，检测底层写入失败 |
| 写入失败后停止 | `writeFailed` 标志，后续写入返回 `errWriteFailed` |
| nil chunk 返回 error | 不 panic，返回 `"nil chunk received"` |
| SSE error 安全编码 | 换行符替换为空格，防止破坏事件帧 |
| documents 编码失败 | 检查 `sonic.Marshal` 错误并返回（MetaData 含不可编码值时可触发，已测试） |
| content 编码 | content 实际写入字段是 `StreamData.Content`（string），编码不会自然失败，未制造无意义测试（已知边界） |
| 日志使用请求 ctx | 移除 `context.Background()` 日志，错误返回调用方 |
| SSE 已开始后错误 | 中间件检测 BytesWritten>0 和 Content-Type，不追加 JSON |

**验证结果**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/common/... ./internal/controller/rag/... -count=1 -v` — 退出码 0，17 个新增测试 + 11 个已有测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./internal/controller/rag/...` — 退出码 0
- `go test -race ./core/common/...` — Windows 环境 race detector DLL 问题（非代码问题，已有测试也失败）

**证据目录**：[verification/stage1c1/](verification/stage1c1/)

### Stage 1C-1 定点收尾记录

**日期**：2026-10-15
**基线**：`11e021a3cca840b2225c6228be6a19aaa6a03f55`（1C-1 fix 提交）
**状态**：✅ 定点收尾完成，已通过独立源码复核
**复核备注**：复核方未独立重跑测试
**范围约束**：不实施 1C-2，不改前端协议，不重构共享 SSE 编排。

**修改文件**：
- `server/core/common/stream.go` — 恢复前端兼容帧格式：documents `documents:<JSON>\n\n`、content `data:<JSON>\n\n`、DONE `data:[DONE]\n\n`；error 保留 `event: error` + `data:` 原格式；保留统一写入 writeFrame、短写检查、failed 状态与 errors.Join 双错误链
- `server/core/common/stream_test.go` — 新增 `TestRealSSEWriter_FrontendCompatFrames`：断言 documents: 行、双换行边界、从该行切片提取的 JSON 含 document，并断言不出现 event: documents 帧
- `server/internal/controller/rag/rag_new.go` + `rag_v1_chat_stream.go` — 为 ChatStream 增加实例级、运行期只读的注入边界（chatRetrieve/chatAnswerStream/chatSSEWrite），nil 回退生产默认
- `server/internal/controller/rag/rag_v1_chat_stream_test.go` — 新增 `TestChatStream_AnswerStreamError`：调用实际 ChatStream，检索返回离线文档、建流返回 sentinel；断言 err 非 nil、errors.Is 识别 sentinel、不返回空成功、不进入 SSE

**验证边界（如实保留）**：
- 保留真实 `MiddlewareHandlerResponse` + `SteamResponse` 集成测试（真实 GoFrame 服务器）
- 刷新错误当前只能经由无返回值 Flusher 调用，“部分刷新错误无法检测”保持为已知边界，不宣称已全面验证
- content 编码字段实际是 `StreamData.Content`（string），编码不自然失败，未制造无意义测试

**验证结果（server/，GOFLAGS 空）**：
- `go build ./...` / `go vet ./...` — exit 0
- `go test ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，41 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0（本次新增代码无 race）
- `go test -race ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 1：`core/common` 触发 Windows 预存在 `0xc0000139`（race detector DLL 入口点）问题，非代码数据竞争，根因待确认，该包 race 视为未完成

**race 范围**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。普通测试通过不写成 race 通过。

**证据命令记录**：`verification/stage1c1/` 每个 `-final` 日志头部记录实际命令、GOFLAGS 与退出码。

**证据目录**：[verification/stage1c1/](verification/stage1c1/)

### Stage 1C-2 定点修正记录（关闭前台分支 + 真实 SSE 编排集成）

**日期**：2026-10-15
**基线**：`96c7f90355bc74895b21d27aa95560957c2f62a4`（Stage 1C-2 last）
**状态**：✅ 已通过独立源码及提交日志复核（复核方未独立重跑测试；验收基线 `26e55ee2ea43daf703ed72da60425ff5b7980cba`）
**范围约束**：只完成两项修正；不实施 Stage 1D，不重构整体流架构，不改前端协议、History 接口或数据库模型。

**一、补齐成功路径的流关闭（server/internal/logic/chat/chat.go）**
- `cleanupOK` 成功路径新增关闭前台 `srs[0]`：通知成功 → 等待后台完成（消费 EOF、保存、关闭 `srs[1]`）→ 关闭前台 `srs[0]` → cancel。
- 依据 Eino v0.6.0：`Copy(2)` 仅在两个子分支都关闭时才关闭上游 source；此前成功路径不关 `srs[0]` 导致上游不释放。失败路径先 cancel 的顺序、sync.Once 幂等、不在成功通知前取消模型均保持不变。
- 回归测试 `TestGetAnswerStream_SuccessCleanup_ClosesForegroundBranch`：成功完成后对返回 reader `Recv()` 断言 `schema.ErrRecvAfterClosed`（直接可观察的前台关闭信号，非“保存数量正确”、非“ctx 已取消”），并验证重复 cleanup 安全、保存契约不变。

**二、集成测试经过真实 SSE 编排（stream.go + controller 测试）**
- `common.StreamToResponseWriter(ctx, http.ResponseWriter, reader, docs)` 薄入口：复用 realSSEWriter + streamResponse + sonic.ConfigDefault，不复制流循环；`SteamResponse` 设置 GoFrame 头后调用它，保留对外行为与帧格式。
- `TestController_ChatIntegration_WriteFailure` 重写：实际 ChatStream → 实际 Chat.GetAnswerStream/streamCoordinator → 共享生产编排 → 底层 ResponseWriter.Write 返回 sentinel；断言 err 非 nil 且 errors.Is 识别写入 sentinel、模型观察取消、模型完成信号确认 goroutine 退出、controller 清理完成、未保存 assistant（不手动 cancel 模拟失败）。
- 新增 `TestController_ChatIntegration_NormalComplete`：同路径模型正常 EOF，ChatStream 返回前完整 assistant 已保存一次，按 role/convID/content 精确断言，输出含预期内容与 `data:[DONE]\n\n`。
- `fakeSaver` 记录 role/convID/content（非仅总次数）；新增 common 级 `TestStreamToResponseWriter_ProductionPath` 验证薄入口复用编排并传递真实 Write 错误。

**保留边界**：History 保存不支持 context timeout；部分刷新错误无法检测（无返回值 Flusher）；历史 race 未完成。保留已有后台 EOF 信号、共享 Chat 并发内容归属及 deadline 测试，未重复重写。

**验证结果（server/，GOFLAGS 空）**：
- `gofmt -l` 目标文件无输出；`go build ./...`、`go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，86 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/...`、`go test -race ./internal/controller/rag/... ./internal/cmd/...` — exit 0
- `go test -race -count=5 -run <chat+controller lifecycle> ./internal/logic/chat/... ./internal/controller/rag/...` — 早前日志命令使用占位符、未记录实际正则，不作为有效证据
- 生命周期重复 race（准确正则、已补证）：`go test -race -count=5 -v -run "TestGetAnswerStream_|TestChatStream_|TestController_ChatIntegration_" ./internal/logic/chat/... ./internal/controller/rag/...` — exit 0，命中 23 个生命周期用例（含两包同名 `TestChatStream_ModelReceivesDeadlineAndValue`）×5=115 全 PASS，0 FAIL，无 DATA RACE，无 "no tests to run"；证据 `verification/stage1c2/race-lifecycle-chatctrl-explicit-r3.log`
- `go test -race ./core/common/...`（含其 lifecycle -count=5）— exit 1：Windows `0xc0000139` 运行异常，race 未完成、根因待确认

**race 范围**：`core/common` race 因 `0xc0000139` 运行异常未完成（新入口 `StreamToResponseWriter` 的 common 级 race 未能验证）；chat/controller/cmd race 通过；`core/retriever`、`core/rerank` 亦仍未补证。普通测试通过不写成 race 通过。

**证据目录**：[verification/stage1c2/](verification/stage1c2/)（`*-r2.log`；生命周期补证 `race-lifecycle-chatctrl-explicit-r3.log`）

**验收复核结论**：Stage 1C-2 已通过源码及提交日志复核，复核方未独立重跑测试；验收基线 `26e55ee2ea43daf703ed72da60425ff5b7980cba`。`7fb5b77` 为 Stage 1C-2 验收记录及生命周期 race 补证提交（纯文档/证据变更），不替换业务验收基线 `26e55ee`。Stage 1-Pre/1A/1B/1C 均已通过复核，Stage 1 整体尚未完成（1D 待实施）。

### Stage 1D-1 定点修正记录（检索返回完整性校验 + 两项验收测试）

**日期**：2026-10-16
**基线**：`ff0b87230e307ced2746e6b84fc11e99192cd572`（Stage 1D-1 实施提交）
**状态**：✅ 已通过独立复核（基线 `644642712bcb04e820e345598a98d99c78277175`，源码及提交日志复核，未独立重跑测试）
**范围约束**：只处理两项事项；不进入 1D-2/1D-3，不重构整体索引架构。

**一、补齐检索返回集合检查（server/core/indexer.go）**
- `indexAsyncByDocsID` 在保存与后续索引之前新增返回集合完整性校验：返回文档不能为 nil、返回 ID 必须属于预期集合、重复返回 ID 明确报错（避免重复保存/重复索引）。
- 保留缺失 ID 的重试与耗尽错误（缺失/重试在完整性校验之前处理）；非法返回一律在 saveChunksFn/idxerAsync/updateDocStatus 之前返回错误。
- 不扩大到跨请求索引幂等或数据库唯一约束设计。

**二、补齐两项已有验收测试**
- knowledge（chunks_test.go）：`TestSaveChunks_ChunkAndStatusErrorsBothJoined` 一次调用让 insert 与状态更新返回不同 sentinel，`errors.Is` 同时识别两者且含 chunk ID 与 doc ID；`TestSaveChunks_UpdateFailure_Propagated` 覆盖 update 分支（与 insert 不同）失败传播。
- core（indexer_test.go）：`TestIndexAsyncByDocsID_FullSuccess` 记录事件序列并精确断言 search→saveChunks→IndexAsync→Active，校验保存的 chunk ID/内容/docID 及传入 IndexAsync 的文档；`TestIndexAsyncByDocsID_MarshalError_NoActive` 增加 idxerAsync 未调用断言。新增非法返回编排测试 `TestIndexAsyncByDocsID_UnexpectedExtraDoc`（[a]→[a,unrelated]）、`TestIndexAsyncByDocsID_DuplicateDocID`（[a]→[a,a]）、`TestIndexAsyncByDocsID_NilDoc`（含 nil），各断言返回错误且三阶段均未调用。

**本轮边界（准确记录）**：
- 重试等待仍为原有固定延迟（`time.Sleep`），可取消生命周期归属 1D-3，本批未改。
- 统一 markFailed 仍归属 1D-3。
- 预取消 fake 测试仅证明应用层入口检查，不证明 driver 能中断执行中的 SQL。
- core/common、core/retriever、core/rerank 的 race 仍待补证（core/common 为 Windows `0xc0000139` 运行异常）。

**测试数量（按日志实际计数）**：core 全包 39（9 TestIndex 生命周期 + 11 TestIndexAsyncByDocsID 编排 + 19 TestRetrieve）、knowledge 10（TestSaveChunks）、两包合计 49 PASS；本批新增测试 5（knowledge 2 + core 编排 3），另强化既有 2（FullSuccess 事件序列、MarshalError idxerAsync 断言）。

**验证结果（server/，GOFLAGS 空；`verification/stage1d1/*-fix.log`）**：
- `gofmt -l` 无输出；`go build ./...`、`go vet ./...` — exit 0
- `go test ./internal/logic/knowledge/... ./core -count=1 -v` — exit 0，49 pass / 0 fail / 0 skip
- `go test -race ./internal/logic/knowledge/... ./core -count=1` — exit 0
- `go test -race -count=10 -v -run "TestSaveChunks_|TestIndexAsyncByDocsID_" ./internal/logic/knowledge/... ./core` — exit 0，命中 21 用例 ×10=210 PASS，0 FAIL，无 DATA RACE，无 "no tests to run"
- `go test -p 1 ./... -count=1` — exit 0

**证据目录**：[verification/stage1d1/](verification/stage1d1/)（`*-fix.log`，未覆盖历史证据）

### Stage 1D-2 实施记录（QA 有限并发和失败状态）

**日期**：2026-10-17
**基线**：`644642712bcb04e820e345598a98d99c78277175`
**状态**：✅ 已通过独立复核（基线 d7618e5，源码及提交日志复核，未独立重跑测试）
**范围约束**：仅 1D-2（F08）；不实施 1D-3，不修改已验收的 1D-1/1C/1B/1A/1-Pre。

**修改文件**：
- `server/core/indexer/qa.go` — 裸 `sync.WaitGroup` 替换为 `errgroup.WithContext` + `SetLimit(maxConcurrent)`；首错取消；nil doc/nil MetaData 防护；重复指针检测；结果写入独立 `results` 数组，Wait 后统一写回 `doc.MetaData`
- `server/core/indexer/async.go` — `BuildIndexerAsync` 接收 `qaModelFactory` 参数；QA model 构建时注入，运行期只读
- `server/core/indexer/indexer_async.go` — `DocumentToFields` 条件写入 `FieldQAContent`/`FieldQAContentVector`（仅当值非空时）
- `server/core/config/config.go` — `Config` 新增 `QAEnabled *bool` + `QAMaxConcurrent int`
- `server/core/rag.go` — `New()` 仅当 QA 启用时预热 `qaModel`；传递 `qaModelFactory` 给 `BuildIndexerAsync`
- `server/internal/logic/rag/retriever.go` — `RagConfig` 级联 QA 配置字段；`defaultRagFactory.Create` 传递
- `server/core/common/chat_model.go` — `GetQAModel` 使用 `sync.Once` 保护懒加载，消除无锁缓存 race
- `server/manifest/config/config_demo.yaml` + `config_qd_demo.yaml` — 新增 `qa.enabled` / `qa.maxConcurrent` 示例
- `server/core/indexer/qa_test.go`（新建）— 11 个离线 QA 测试
- `server/go.mod` / `server/go.sum` — `go mod tidy` 补充 `golang.org/x/sync` v0.15.0 为直接依赖

**测试覆盖（11 个）**：AllSuccess / PartialFailure / ConcurrencyLimit / Disabled / ExistingContent_Preserved / ContextCancel / EmptyDocs / NilDoc / DuplicatePointer / ExistingContent_Skips / GraphIntegration_ErrorBreaksGraph

**验证结果（server/，GOFLAGS 空）**：
- `gofmt -l` 无输出；`go build ./...`、`go vet ./...` — exit 0
- `go test ./core/indexer/... -count=1 -v` — exit 0，11 pass / 0 fail
- `go test ./core -count=1 -v` — exit 0，39 pass / 0 fail（11 QA + 9 Indexer + 19 Retrieve）
- `go test -race ./core/indexer/... -count=1` — exit 0
- `go test -race ./core -count=1` — exit 0
- `go test -race -count=10 -v -run "TestQA_" ./core/indexer/...` — exit 0，无 DATA RACE
- `go test -p 1 ./... -count=1` — exit 0
- `go mod tidy` — 无残留

**保留边界**：QA 关闭时 `qaRtrvr` 对 `FieldQAContentVector` 缺失的 kNN 行为待验证（不影响主检索路径 `rtrvr`）；Qdrant 异步 indexer 路径保留现状；`core/retriever`、`core/rerank` race 仍待补证。

**证据目录**：[verification/stage1d2/](verification/stage1d2/)

### Stage 1D-2 定点修复记录

**日期**：2026-10-17
**基线**：`b24330d6eec11df006ba09d7664f7a2955df5bef`
**状态**：✅ 定点修复完成，已通过独立复核（基线 d7618e5）

**修改文件**：
- `server/core/common/chat_model.go` — 提取 initQAModel 实例级初始化器；defaultQAConfigReader 使用 Get+error 替代 MustGet；defer 释放锁
- `server/core/indexer/qa.go` — 预取消检查（factory 和 Generate 均不调用）；nil factory 检查；Generate 返回 nil 时返回明确错误
- `server/core/indexer/indexer_async.go` — 提取 documentToFields 纯函数，不修改输入 doc
- `server/internal/logic/rag/retriever.go` — readQAConfig 接收 configGetter 接口，生产与测试共用同一解析逻辑
- `server/core/common/chat_model_test.go` — 重写，6 个测试覆盖初始化器重试、错误链、缓存、并发
- `server/core/indexer/qa_test.go` — 修复无锁计数器（atomic.Int32）、共享 map 确定性回答、失败等待 channel 同步；新增 nil factory、Generate nil 测试
- `server/core/indexer/graph_test.go`（新建）— 4 个 Graph 集成测试
- `server/core/indexer/indexer_async_test.go`（新建）— 7 个 DocumentToFields 测试
- `server/internal/logic/rag/retriever_test.go` — 重写，6 个配置解析测试

**验证结果（server/，GOFLAGS 空）**：
- `gofmt -l` 无输出；`go build ./...`、`go vet ./...` — exit 0
- `go test ./core/indexer/... -count=1 -v` — exit 0，35 pass
- `go test ./core -count=1 -v` — exit 0，38 pass
- `go test -race ./core -count=1` — exit 0
- `go test -race -count=10` 压测通过
- `go test -p 1 ./... -count=1` — exit 0

**已知边界**：indexer 包 race 检测因 Windows DLL 问题失败；QA Retriever kNN 行为待验证（A-4）

**证据目录**：[verification/stage1d2/](verification/stage1d2/)（`*-fix2.log`）

### Stage 1D-2 定点修正记录（共享 helper：Graph 装配 / 字段转换 / QA 缓存）

**日期**：2026-10-17
**基线**：`575949abfde25ff0e5ffc68ed0211b92430f54a6`（Stage 1D-2 定点修复）
**状态**：✅ 已通过独立复核（基线 d7618e5，源码及提交日志复核，未独立重跑测试）
**范围约束**：只修正三项；不实施 1D-3，不改检索策略、数据库模型、整体索引架构。

**一、Graph 测试复用生产装配（server/core/indexer/async.go）**
- 提取包内共享 helper `buildAsyncGraph(ctx, idr indexer.Indexer, qaEnabled, maxConcurrent, qaModelFactory)`：QA 节点、启用/关闭分支、并发上限处理、节点连接与 Compile 只在此实现一次，并检查/传播所有构图错误。
- 生产 `BuildIndexerAsync` 创建真实 Indexer 后调用该 helper；测试 `graph_test.go` 删除独立复制的 `buildTestGraph`，改用同一 helper 注入实现 `indexer.Indexer` 接口的 `fakeIndexer`。
- 4 个 Graph 测试验证：QA Generate 返 sentinel→下游不执行且 errors.Is 识别；factory 返 sentinel→下游不执行；QA 关闭→factory/Generate 均不调用且 Indexer 正常收到文档；QA 成功→下游收到准确 QA 内容与正确 IDs。

**二、完整生产字段转换不修改输入（server/core/indexer/indexer_async.go）**
- `documentToFields` 成为唯一完整转换 helper：内部对 getExtData 结果做 sonic.Marshal，Extra 直接放入新返回 map，不写回输入 doc/MetaData；序列化失败返回可追踪错误且不返回成功字段；保留正常 Extra 语义、Content/QA 的 EmbedKey 与非空 QA 才写 QA 的规则。
- 生产 ES `DocumentToFields` 回调仅读 knowledgeName（缺失返回错误）并委派 helper，删除对 `doc.MetaData[FieldExtra]` 的写入与被忽略的 marshal 错误。
- `indexer_async_test.go` 直接测试同一 helper：正常 Extra（真实 ExtKeys 数据经序列化）、输入不变、独立返回 map、QA 缺失/空/非空、nil MetaData，以及用 ExtKeys 下的不可编码值（func）触发真实序列化失败。

**三、初始化失败重试经过实际缓存状态（server/core/common/chat_model.go）**
- 引入实例级 `qaModelCache`（锁、model、ready、注入 readCfg/build）；`GetQAModel` 委派生产默认实例 `qaDefaultCache.get`，签名与成功缓存行为不变；未新增可变包级测试替换函数；其他模型缓存机制不变。
- 测试创建独立 `qaModelCache` 实例确定性验证：首次 builder 失败→errors.Is 识别且未发布缓存；第二次成功；第三次命中缓存且 builder 总次数=2；并发只构建一次；配置读取失败不调用 builder；builder 返回 (nil,nil)→错误且允许重试。

**交付：三个生产入口如何调用共享实现**：1) `BuildIndexerAsync`→`buildAsyncGraph`（测试同用）；2) `newAsyncIndexer` ES `DocumentToFields` 回调→`documentToFields`（测试同用）；3) `GetQAModel`→`qaDefaultCache.get`（测试用独立 `qaModelCache.get`，同一实现）。

**验证结果（server/，GOFLAGS 空；`verification/stage1d2/*-fix2.log`）**：
- `gofmt -l` 无输出（gofmt-fix2.log）；`go build ./...` exit 0（build-fix2.log）；`go vet ./...` exit 0（vet-fix2.log）
- `go test ./core/indexer/... ./core/common/... -count=1 -v` exit 0，64 pass / 0 fail（test-indexer-common-fix2.log）
- `go test -p 1 ./... -count=1` exit 0，13 包 ok（test-full-fix2.log）
- `go test -race ./core/indexer/... ./core/common/... -count=1` exit 1：两包均 `0xc0000139`（race 二进制无法启动，运行异常、race 未完成、根因待确认；未反复运行），见 race-indexer-common-fix2.log；普通并发测试不作为 race 结论替代

**保留边界**：A-4 QA Retriever 缺失向量 kNN 检索行为待验证；core/common、core/retriever、core/rerank 及本批 indexer/common 的 race 仍待补证（Windows 0xc0000139）；统一 markFailed、重试可取消生命周期、固定延迟归属 1D-3。

**证据目录**：[verification/stage1d2/](verification/stage1d2/)（`*-fix2.log`）

### Stage 1C-2 完成记录

**日期**：2026-10-15
**基线**：`f89bb816d234c39fbd2b497a096299bfb3ecf98e`（Stage 1C-2 fix2）
**状态**：✅ 定点收尾完成，待独立复核
**范围约束**：保留已完成的 cleanEOF 判定、sync.Once 终态保护、coordDone 等待及成功路径“cleanup 完成后再 cancel”的顺序。

**修改文件**：
- `server/internal/logic/chat/chat.go` — 新增 `streamCoordinator` 结构体；`cleanup` 等待后台完成（`<-coordDone`）；`sync.Once` 保证幂等；后台记录 `cleanEOF`；严格保存条件：`cleanEOF && result.Success && ctx.Err() == nil`；删除未使用的 `StreamResult.Answer` 字段
- `server/internal/controller/rag/rag_v1_chat_stream.go` — 成功路径：cleanup 等待后台完成后 cancel；失败路径：先 cancel，再 cleanup 等待后台，再 cancel；移除 `cleanupDone` defer 模式
- `server/core/common/stream.go` — 读取前/后、写入前检查 `ctx.Err()`；取消后不继续输出内容或 DONE
- `server/core/common/stream_test.go` — 更新 `CancelThenEOF_NoDONE`；新增 `CancelBeforeRead`、`CancelAfterReadBeforeWrite`
- `server/internal/logic/chat/chat_test.go` — 替换固定 sleep 为 cleanup 阻塞等待；新增 `callCleanup` 助手；新增 `BackgroundEOFThenForegroundFail_NoSave`、`CleanupIdempotent`；重写 `BlockingRecvCancel`、`WriteFailure_ModelObservesCancel_NoSave`

**修复内容**：

| 修复项 | 说明 |
|---|---|
| 保存/cancel 竞态 | cleanup 等待后台协调任务完成后再 cancel，正常回答不会因 defer cancel 抢先而丢失 |
| cleanup 完成生命周期 | cleanup 发送结果 → 关闭 srs[0] → 等待 coordDone；后台 defer close(coordDone) + defer srs[1].Close() |
| cleanup 幂等 | sync.Once 保护，第一次终态决定结果，重复调用安全 |
| 严格保存条件 | cleanEOF + 前台成功 + 未取消 + 无错误；不仅凭 StreamResult.Success 保存 |
| 后台记录 cleanEOF | 区分 EOF/错误/nil chunk；只有真正 EOF 才设 cleanEOF=true |
| 删除 StreamResult.Answer | 未使用字段移除 |
| SSE 取消边界 | 读取前/后、写入前检查 ctx.Err()；阻塞 Recv 退出由生命周期机制解决 |

**测试覆盖**：

| 测试 | 说明 |
|---|---|
| TestGetAnswerStream_WriteFailure_ModelObservesCancel_NoSave | 写入失败 → cancel → 模型观察取消 → 不保存 |
| TestGetAnswerStream_BackgroundEOFThenForegroundFail_NoSave | 后台 EOF 但前台失败 → 不保存 |
| TestGetAnswerStream_NormalEOF_SaveOnce | 正常完成 → 保存一次完整 assistant |
| TestGetAnswerStream_Cancel_NoSave | 取消 → 等待 cleanup → 不保存 |
| TestGetAnswerStream_ConcurrentSessions_Isolation | 并发不同 convID → 内容归属正确 |
| TestGetAnswerStream_ModelReceivesContextValueAndDeadline | 模型收到 ctx 值和 deadline |
| TestGetAnswerStream_CleanupIdempotent | cleanup 重复调用安全，第一次决定结果 |
| TestStreamResponse_CancelBeforeRead | SSE 读取前取消 → 无事件写入 |
| TestStreamResponse_CancelAfterReadBeforeWrite | SSE 读取后写入前取消 → 无数据写入 |

**History 保存 context 边界**：`historyWrapper.SaveMessage` 不接受 context（第三方 chat-history 包接口），等待保存可能受数据库调用阻塞，不宣称有界。

**验证结果（server/，GOFLAGS 空）**：
- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，73 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... -count=5 -run "TestGetAnswerStream_"` — exit 0，无 DATA RACE
- `go test -race ./core/common/...` — exit 1：Windows 预存在 `0xc0000139`（与 1C-1 一致），根因待确认

**race 范围**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。

**证据目录**：[verification/stage1c2/](verification/stage1c2/)

### Stage 1C 计划记录

**日期**：2026-10-14
**基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`
**状态**：✅ 计划已制定，待实施

**覆盖问题**：
- F03：`GetAnswerStream` 将请求 ctx 替换为 `context.Background()`，用户取消后模型/保存协程继续工作
- F04：`SteamResponse` 读流失败后仍发 `[DONE]` 并返回 nil；Controller 建流失败可返回空成功

**批次划分**：
- 1C-1（F04 SSE 流语义修复）：推荐首批，修改集中在 `stream.go` 和 controller
- 1C-2（F03 Chat context 修复）：第二批，涉及 context 传递和后台 goroutine 生命周期

**详细计划**：[design/stage1c-plan.md](design/stage1c-plan.md)

### Stage 1-Pre 独立复核记录

**日期**：2026-10-14
**复核基线**：`db0c5971d137ddabcc10ae5fb0c4b9baa17e1b98`（Stage 1-Pre 完成后的 HEAD）
**复核方式**：源码复核，未独立重跑 Go 测试

**复核结论**：
- F06 nil 访问修复逻辑正确，`validateIndexerInput()` 在所有副作用前完成参数校验
- 四类输入行为定义清晰，默认模式与 PaperDesk 模式分支合理
- `paperDeskModeOverride` 为临时测试机制，待 PaperDesk profile 配置实现后移除
- 无副作用保证：校验失败直接返回 error，不触发文件保存、DB 插入、后台任务

**行为修正记录**：
- 默认模式下 File+URL 同时存在时的行为为"File 优先，URL 忽略"（非报错）
- PaperDesk 模式目前未在生产启用（`isPaperDeskMode()` 临时返回 false）
- `paperDeskModeOverride` 是临时测试机制，不应在生产代码中长期保留

### Stage 1A 完成记录

**日期**：2026-10-14
**状态**：✅ 已通过复核（基线 `3b8f7a0efc27595d1b50aa2addb2523ed20a2ef0`）

**原始修改**：
- `server/internal/dao/dao.go` — 移除 init()，保留 InitDB() error
- `server/internal/logic/rag/retriever.go` — init() 提取为 Init() error
- `server/internal/logic/chat/chat.go` — init() 提取为 Init() error
- `server/internal/cmd/cmd.go` — 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init()

**第一轮复核修复**：
- `server/internal/dao/dao.go` — AutoMigrate 分离为 Migrate()；新增 Close()、InitWithMigrate()、IsInitialized()；原子初始化；重复调用保护
- `server/internal/dao/db/interface.go` — Database 接口新增 Close() 方法
- `server/internal/dao/db/base.go` — Base 实现 Close()
- `server/internal/logic/rag/retriever.go` — ragSvr 默认 nil；重复调用保护；GetRagSvr 未初始化返回 nil；IsInitialized()
- `server/internal/logic/chat/chat.go` — 重复调用保护；GetChat 未初始化返回 nil；IsInitialized()
- `server/internal/cmd/cmd.go` — Fatalf 改为返回 error；失败时 dao.Close() 清理
- `server/core/rag_test.go` — 添加 //go:build integration 标签
- `server/core/common/chat_model_test.go` — 添加 //go:build integration 标签
- `design/stage1-plan.md` — F 编号修正（1C→F03/F04，1D→F07/F08/F09）

**第二轮复核修复（bd20ca2 → 本轮）**：

| 修复项 | 说明 |
|---|---|
| Migrate 分离 | cmd.go 普通启动不再调用 dao.Migrate()，避免意外改表 |
| 在线测试隔离 | rerank_test.go 和 youcom_integration_test.go 添加 `//go:build online`；rag_test.go 改为 `//go:build integration && online`；chat_model_test.go 改为独立单元测试 |
| MustGet 替换 | dao/retriever/chat 三个包的 MustGet 全部替换为 Get + error 处理，缺配置返回 error 而非 panic |
| 行为测试 | 17 个测试覆盖：预初始化状态、失败后状态不变、Reset 安全性、GetDsn 返回 error |
| 失败清理协调 | 新增 dao.Reset()、rag.Reset()、chat.Reset()；cmd.go 级联清理（chat 失败 → rag.Reset + dao.Reset） |
| 同步一致性 | 全部改用 sync.RWMutex；GetDsn 返回 (string, error)；IsInitialized/GetRagSvr/GetChat 使用 RLock |
| chat-history 边界 | chat.Init() 先检查 dao.GetDsn() 返回 error，明确标注 AutoMigrate 边界 |
| Windows 诊断 | `go test ./...` 的 fork/exec Access denied 为 Windows 临时目录权限问题，单独运行通过 |

**验证结果（第二轮）**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，17 个测试全部通过
- `go test ./core/retriever/...` — 退出码 0（单独运行）
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — core/retriever 和 core/websearch 因 Windows 临时目录权限问题 fork/exec 失败（非测试逻辑问题）
- 离线测试不触发数据库/向量服务/OpenAI 连接
- 集成测试通过 `//go:build integration` 标签隔离
- 在线测试通过 `//go:build online` 标签隔离

**第三轮复核修复（2df2421 → e48b67e）**：

| 修复项 | 说明 |
|---|---|
| chat-history AutoMigrate 适配 | 创建 historyWrapper 和 historyImpl，复用 dao 连接，绕过 NewEinoHistory 的 AutoMigrate；新增 MigrateChatHistoryTables() 显式迁移入口 |
| 可注入边界 | dao.InitDB 接受 ConfigProvider 接口；生产用 InitDBWithConfig()，测试用 fakeConfig |
| 确定性失败测试 | 使用 fakeConfig 测试配置错误、无效 DSN、nil 配置等场景，不再使用“若意外成功就 skip” |
| Reset/Close 资源释放 | rag.Reset() 改为返回 error；cmd.go 级联清理处理 Reset 错误 |
| 配置错误传播 | ConfigProvider 接口统一配置读取错误传播 |
| 在线许可检查 | rerank_test.go 和 youcom_integration_test.go 添加 ONLINE_TEST_PERMITTED=true 检查 |
| Database 接口扩展 | 新增 GormDB() 方法暴露底层 *gorm.DB，供 chat-history wrapper 使用 |

**验证结果（第三轮）**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，19 个测试全部通过
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — 除 Windows 文件锁定问题外全部通过（非测试逻辑问题）
- 新增测试：TestInitDB_ConfigError, TestInitDB_InvalidDSN, TestInitDB_NilConfig
- chat-history 不再在 Init 时 AutoMigrate，迁移通过 MigrateChatHistoryTables() 显式控制

**详细计划**：[design/stage1-plan.md](design/stage1-plan.md)

## 设计文档版本线

| 版本 | 状态 | 说明 |
|---|---|---|
| v1.0 | 已归档 | 初始方案 |
| v1.1 | 已归档 | 复核后首修 |
| v1.1.1 | 已归档 | 12 项微调 |
| v1.1.2 | **当前版本** | 9 项修正（BlobStore/旧数据映射/Stage 0 等） |

## ES 版本风险

客户端 v8.16.0 vs Compose 8.11.3 vs README 8.18.0 — 不在官方保证区间。不阻塞 Stage 1。

## 未验证项

- Eino 运行时兼容性（Stage 2）
- ES 实际连通性（Stage 1 启动验证）
- MySQL SKIP LOCKED（Stage 4）
- chat-history 替换方案（Stage 5）
- ZIP 快照逐文件对比（待获取文件）
