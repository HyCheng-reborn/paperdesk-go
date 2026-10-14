# 项目进度记录

## 当前状态

**日期**：2026-10-14
**阶段**：Stage 1C 计划已制定（基线 eb27ba2），Stage 1B 三个批次均已完成（1B-1 ✅、1B-2 ✅ 已通过复核、1B-3 ✅ 已通过独立源码与提交日志复核），Stage 1-Pre 已完成，Stage 1A 待独立复核

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

**状态**：计划就绪，待实施

### 批次计划

| 批次 | 内容 | F 编号 | 依赖 | 状态 |
|---|---|---|---|---|
| 1-Pre | F06 File/URL nil 访问修复 | F06 | 无 | **✅ 已完成** |
| 1A | init 副作用清理 + 离线测试隔离 | F12 | 无 | **第三轮修复已完成，待独立复核** |
| 1B-1 | F05 Rerank HTTP ctx 绑定与校验 | F05 | 1A | **✅ 已完成** |
| 1B-2 | F01 Retriever 并发安全 | F01 | 1A | **✅ 已通过独立源码与提交日志复核** |
| 1B-3 | F02 Indexer 异步闭包 | F02 | 1A | **✅ 已通过独立源码与提交日志复核** |
| 1C | Chat context + 流式 EOF + 错误语义 | F03/F04 | 1A | **1C-1 ✅ 已完成，1C-2 待实施** |
| 1D | Chunk 写入错误传播 + QA 并发 + goroutine 止损 | F07/F08/F09 | 1A, 1B | 待实施 |

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
**状态**：✅ 定点收尾完成，待独立复核
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
**状态**：✅ 已完成

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
| documents/content 编码失败 | 检查 `sonic.Marshal` 错误并返回 |
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
**状态**：✅ 第三轮修复已完成，待独立复核

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
