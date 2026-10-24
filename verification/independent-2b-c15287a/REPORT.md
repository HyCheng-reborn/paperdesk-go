# Stage 2B 百炼父取消修复独立复核

结论：**通过。未发现剩余阻塞；在此前审查结论及既定离线边界内，Stage 2B 可验收。** 不宣布整个 Stage 2 完成，不修改项目验收状态。

目标：`c15287a71a61f2b3c0d619c071265d08c76d3555`。
增量基线：`91ab52dd3f942da00fadcdbb8963a5537a0e4c9a`。已核实两个提交对象存在、基线是目标祖先，完整差异为 18 文件（+520/-129）。本轮集中复核最后一项百炼父取消 P1 及共享包装的相邻回归，不重做已关闭的 Stage 1 或无关历史审计。

## 1. 修复判定与生产证据

原 P1：百炼输出 pipe 满、消费者停止读取，仅父 ctx 取消时，HTTP 已退出但 session 不 Done、进程 slot 残留。**已闭合。**

生产链：Registry.GetChat → registry.buildChat → 已注册 bailian.BuildChat → chatAdapter.StreamSession → Reserve/Commit 一次 → SDK.Stream(derivedCtx) → **providers.WrapBudgetStreamReader(原始 ctx, streamCancel, sr, 原 lease, nil)** → parent watcher / 输出关闭 / 实际 Recv 等待 / 幂等终结。

关键源码：
- `server/core/providers/bailian/chat.go:116-145`：保留原预算预留及建流失败 Abort；145 行把原始父 ctx 和已取得 lease 传给共享包装。
- `server/core/providers/budget_adapter.go:449-587`：原通用包装仅导出名称，生命周期核心未重新复制。父取消关闭 cancelSig、输出 reader，解除背压，并回收源；真实 Recv 仍纳入 WG。
- `server/core/providers/registry.go:225-241`、`bailian/register.go`：Registry 的百炼 builder 分派仍成立，现在返回的百炼 role 内部直接调用上述共享核心。

没有在共享包装中再次 Reserve/Commit，没有再套一层重复预算适配；原 lease 的 once 终结保持。旧百炼 leaseStreamReader/wrapLeaseStreamReader 删除，无预算与有预算均只有单一消费转发链。此次没有修改 HTTP 协议、配置、索引、go.mod/go.sum 或默认装配入口。

### 独立原复现与回归

| 独立补充用例 | 普通一轮 | race 十轮 |
|---|---|---|
| RegistryBailianParentCancelUnderBackpressure（上一轮失败原复现） | PASS | 10 PASS |
| BailianNilBudgetParentCancel（真正 Budget=nil） | PASS | 10 PASS |
| ManagedParentCancelUnderBackpressure（OpenAI） | PASS | 10 PASS |
| ManagedCloseWaitsReceiver（真实 HTTP Read 终结闸门） | PASS | 10 PASS |
| ManagedChatFailureBackpressure（真实 SDK + Chat，前台停读） | PASS | 10 PASS |
| SessionEarlyFrontendClose | PASS | 10 PASS |
| ManagedRealClose（百炼/OpenAI 两子项） | PASS | 10 PASS，各子项 10 PASS |
| ManagedNoBudgetABC（百炼 Budget=nil） | PASS | 10 PASS |

Registry 百炼原复现先观察输出 buffer 已有背压，再仅取消父 ctx；等待 HTTP handler 退出和 session.Done，断言前不追加 Close/Drain，随后确认 slot=0。上一轮同一原复现十轮失败，本轮十轮通过。新增 nil-budget 取消测试确认“不存在 lease”的生产路径也能收敛。真实 HTTP 构造、锁定 SDK、共享预算和 Chat 编排均直接复用生产代码；没有复制生产协议解析或终结逻辑。反射仅只读观察锁定 Eino pipe 缓冲，无 unsafe/字段修改。

原 Chat 背压、提前 Close、真实 Recv 等待、OpenAI 父取消、百炼/OpenAI 显式 Close、无预算完整正文等已闭合项保持通过。未发现需要再交实现者修复的正确性、预算或生命周期问题。

## 2. 干净目标独立实测

先在干净目标运行下表全部命令，完成后才恢复独立复现文件。完整命令、SHA、CWD、时间、实际正则和退出码见 run-clean.ps1、各日志；去重实际测试名见 TEST-NAMES.md。

CWD：`C:/Zhy/code/go-rag-review-1d3/server`。
环境：Go 1.27.1 windows/amd64；MSYS2 UCRT64 GCC 16.2.0、Binutils 2.47.20260726；仅进程 PATH/CC/CXX/CGO_ENABLED=1，GOFLAGS 空，GOPROXY/GOSUMDB=off，见 environment.log。没有修改系统工具链或 Git 配置。

| 独立验证 | 日志 | EXIT | 顶层 PASS / RUN（含子项） |
|---|---|---|---|
| providers/common/rerank/config/logic rag/chat 普通 -count=1 -timeout=180s -v | ordinary.log | 0 | 305 / 330 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | race-providers.log | 0 | 138 / 152 |
| controller rag/common/rerank/logic rag/chat race -count=1 -timeout=180s -v | race-wiring.log | 0 | 188 / 199 |
| providers 关键精确正则 -race -count=10 -v（含六项新百炼用例） | race-key-stress.log | 0 | 580 / 580 |
| Chat ^TestChatStream_Session.* -race -count=10 -v | race-chat-key.log | 0 | 70 / 70 |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | 0 | 非 verbose |
| go vet ./... | vet.log | 0 | — |
| go build ./... | build.log | 0 | — |

全部 race 实际执行、无 DATA RACE；没有将 no tests to run 或普通压力测试冒充 race 证据。所有验证离线，只有本机 httptest；未连接真实 DB/ES/Qdrant、模型 API 或在线探针。

## 3. 目标＋独立补充测试证据

日志 managed-review.log / race-managed-review.log：均 EXIT 0。普通 8 个顶层 PASS、含子项 10 RUN；十轮 race 80 顶层 PASS、含子项 100 RUN，0 FAIL、0 DATA RACE。两条命令均对 ./core/providers/openai ./internal/logic/chat 使用日志内冻结精确正则，-count=1 或 -race -count=10，-timeout=60s -v。

测试来源与层级：
- preserved/：切换前原审查文件逐文件保护副本及哈希，来自上一轮独立复现，不属于目标提交。
- repros/server_core_providers_openai_review_managed_session_test.go：恢复上一轮原复现，仅追加 nil-budget 父取消用例；包含真实 Registry 百炼原复现。
- repros/server_internal_logic_chat_review_managed_session_test.go：恢复上一轮原文件，无本轮语义修改。
- repros/*.diff、hashes.txt：当前两份补充文件的完整新增 diff/哈希。
- 工作树位置分别为 server/core/providers/openai/review_managed_session_test.go 与 server/internal/logic/chat/review_managed_session_test.go，保留未跟踪状态，不算作目标自带测试。

文件中旧 TestReviewManagedChatCleanupOwnsFrontend 未被本轮正则选中，不计入结果。

## 4. 工作区与交付保护

复用 detached linked worktree `C:/Zhy/code/go-rag-review-1d3`；common dir `C:/Zhy/code/go-rag/.git`。已核实 worktree 关系；没有 clone、fetch、主工作区 checkout/reset/clean/stash 或配置变更。既有适用指令检查未发现 AGENTS.md，本轮差异也未新增。

切换前保护两份独立测试后，再将审查 worktree 切到目标。clean-status.txt、pre-supplement-status.txt 均为空。最终 tracked server diff exit=0，只有两份独立补充测试未跟踪，见 final-worktree-status.txt；目标 SHA 见 target.txt。没有覆盖历史审查证据、他人修改或清理 CRLF 差异。

未修改生产代码、方案、项目进度或验收状态；未提交、推送、合并。

## 5. 实现者证据、非阻塞说明及边界

新增六项测试均在目标树内，独立干净测试可复现通过，独立关键十轮确实命中。实现者日志位于 verification/stage2b-fix11/，与本轮独立结果分开，不作为独立实测替代。

新增测试的几个名称/注释需要按实际断言解读：NoBudgetFullContent 使用零上限的非 nil ProcessBudget，不等于 nil lease；ReadError 使用 HTTP 500，不能单独证明建流后的 body 读错；ConcurrentCloseEOF 是读完 EOF 后并发 Close，并非主动制造 Close/EOF 同时发生。服务端已发送也不单独证明客户端 pipe 已满。本轮以原独立缓冲观察、nil-budget 路径和已有共享错误/终结回归补足关键证据；这些不构成额外阻塞，也不要求为命名或日志格式开启新修复轮次。

保留 2A/此前 2B 已闭合预算、配置隔离、controller owner、初始化/Reset 等结论，本轮不重复全面审计。

仍待后续：2C 六处维度配置、索引 fingerprint/模型身份 guard，新 Embedding profile 生产启用继续等待 guard；2D 真实协议/usage/模型可用性在线探针。A-4、真实 ES/Qdrant、SQL driver 取消、持久化任务恢复等边界不变，未执行，不写成通过。受控会话通过不等于原始 Eino reader.Close 可单独中断任意阻塞调用；继续使用已批准 StreamSession 生命周期契约。

本轮不需再向 Qoder 下发修复任务，不消耗专家 credits 做一般文档或归档修订。后续任务按正确性风险及范围选择模式，简单确定任务优先 flash。
