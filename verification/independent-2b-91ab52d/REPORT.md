# Stage 2B 第十轮修复独立复核

结论：**存在阻塞，2B 暂不可验收；剩余一项 P1：百炼父取消在流输出背压下不能独立收敛。** 本轮原 Chat 背压问题与通用预算包装的实际 Recv 等待问题均已闭合；通用包装父取消也已闭合，但相同契约尚未覆盖百炼生产分支。

目标：`91ab52dd3f942da00fadcdbb8963a5537a0e4c9a`。
增量基线：`fbbaeb4c12ab48db68c778ac38bb64676ddcb5d1`；对象存在、祖先关系已核实，完整增量 18 文件。仅集中检查本轮两组 P1 与相邻流生命周期，不重审已关闭的 Stage 1 或整个 Stage 2。

## 工作区保护与依据

复用 `C:/Zhy/code/go-rag-review-1d3` detached linked worktree，common dir `C:/Zhy/code/go-rag/.git`。适用目录及仓内未找到 AGENTS.md。原有两份未跟踪独立测试先逐文件备份至本目录 preserved/ 并记录 SHA256，再移出审查工作树；随后 detached 切到固定目标，clean-status.txt 为空。没有 clone/fetch，没有切换、清理、配置或修改原仓库工作区。

先完成干净目标全部验证，再恢复独立测试。最终 tracked server diff=0；仅两份审查测试未跟踪，见 final-worktree-status.txt。没有改生产代码、提交推送或更新验收状态。证据均位于本目录，旧报告原样保留。

读取了批准方案、本轮文档增量、上一轮独立报告、go.mod/go.sum、本轮生产/测试完整增量、实现者日志，以及锁定 Eino v0.6.0 stream.go。go.mod/go.sum 本轮未变。本文不以文档或实现者自测代替正确性证明。

## B1 / P1：百炼实际生产包装遗漏父取消终结

**位置**：`server/core/providers/bailian/chat.go:146, 272-307`；生产分派 `server/core/providers/registry.go:225-241`，注册 `server/core/providers/bailian/register.go:9-14`。

**调用链**：Registry.GetChat → getChatRole/buildChat → 已注册百炼 BuildChat（直接返回该 role，没有通用预算包装补救）→ chatAdapter.StreamSession → SDK.Stream(streamCtx) → wrapLeaseStreamReader → lsw.Recv → writer.Send → 业务 reader。

**触发条件**：生产 profile 使用 bailian；调用方停止消费导致输出容量 1 满；仅取消传入父 ctx，没有额外调用 session.Close/Drain。这里不是恢复纯 reader.Close 驱动旧契约，而是验证已约定的父取消路径。

**源码原因**：wrapLeaseStreamReader 的 ctx 参数被命名为 `_ context.Context`，没有 watcher。HTTP 能因 derived ctx 取消，但转发阻塞在 writer.Send；取消 ctx 不会关闭该输出 pipe 的 closed channel，转发也到不了 defer finalize/close(done)。本轮 parent watcher、reader.Close 和 WG 修复都只在 budget_adapter.go，未覆盖独立的百炼包装。

**实际后果**：服务端已收到 HTTP 取消，百炼 session.Done 仍不关闭、转发任务残留，进程预算 slot 保持占用。多个此类请求可耗尽并发额度。显式 Close 可以补救，不等于父取消契约成立。

**独立证据**：
- 普通补充 `TestReviewManagedBailianParentCancelUnderBackpressure` FAIL；相同真实 SDK/HTTP 用例 race 十轮 10 FAIL，观察窗 100ms。
- 为排除构造路径和短等待疑问，再从实际 NewRegistry/GetChat 入口运行 `TestReviewRegistryBailianParentCancelUnderBackpressure`，仅取消父 ctx，观察一秒。**-race -count=10：10 RUN / 10 FAIL，EXIT 1，无 DATA RACE**。
- 每轮取消后快照一致：AttemptsUsed=1、CommittedAttempts=1、InFlight=1、InputReserved=1、OutputReserved=8192。HTTP handler 已退出；断言完成后才 Close，随后 InFlight=0，测试清理全部资源。见 race-registry-bailian-parent.log。
- 测试调用真实 Registry/builder/锁定 SDK/共享预算；只用 httptest，无自写 SSE 解析或终结实现。对 Eino buffer 的 reflect 仅只读观察，无 unsafe 或字段修改。

**最小修复方向**：让百炼生命周期也响应父取消，并解除其输出 Send 背压，走统一幂等终结；确保取消任务有退出条件，Close/Done 仍等待实际拥有的转发/接收完成。可共享生命周期核心，但不能为了套通用包装而重复 Reserve/Commit/结算，也不能丢弃错误或引回双重关闭、无预算抢读。生产百炼与 OpenAI 都应经同一取消/背压验收矩阵，包含有预算和无预算受控会话。

此项是原“父取消背压收敛”契约的未覆盖生产分支，不是在线探针或 2C 才需处理的事项。

## 原问题逐项状态

| 项目 | 本轮结论 | 独立证据 |
|---|---|---|
| Chat 前台满队列失败 cleanup | 已闭合 | 原真实 SDK+Chat 编排复现普通 PASS、race 10 PASS；不先关闭消费者 reader，不保存部分回答 |
| 业务提前 Close 后 cleanup panic | 保持闭合 | 原复现普通 PASS、race 10 PASS |
| 通用包装 Close/Done 早于实际 Recv | 已闭合 | 原真实 HTTP Read 终结闸门复现普通 PASS、race 10 PASS；未释放闸门前 Close 不返回，释放后收敛 |
| OpenAI 父取消 + 输出背压 | 已闭合 | 原复现普通 PASS、race 10 PASS |
| 百炼父取消 + 输出背压 | **未闭合** | B1，生产 Registry 路径 race 10 FAIL |
| 百炼/OpenAI 显式 managed Close | 保持闭合 | 两子项普通 PASS，race 各 10 PASS |
| 无预算百炼 ABC 完整传递 | 保持闭合 | 普通 PASS、race 10 PASS |

关闭 reader 而非并发关闭 writer 的修正合理。锁定 Eino SetAutomaticClose 既设置 closeRecv CAS，也安装 SDK 自身 finalizer；本轮正确性依赖显式 Close/CAS，不依赖 GC，因此不单独将此 API 使用判为问题。新增临时 Recv 已登记 WG；supervisor 自身保持计数非零，未发现 Add/Wait 竞争。

## 干净目标独立实测

所有命令 cwd：`C:/Zhy/code/go-rag-review-1d3/server`。完整命令、准确正则、时间、SHA、退出码见 run-clean.ps1 及对应日志。实际去重命中名见 TEST-NAMES.md。

环境见 environment.log：Go 1.27.1 windows/amd64、MSYS2 UCRT64 GCC 16.2.0、Binutils 2.47.20260726；仅进程级 PATH/CC/CXX/CGO_ENABLED=1，GOFLAGS 空，GOPROXY/GOSUMDB=off。全部离线，没有真实设施、模型 API 或 online/integration 标签。

| 独立命令范围 | 日志 | EXIT | 顶层 PASS / RUN（含子项） |
|---|---|---|---|
| 受影响 providers/common/rerank/config/logic rag/chat 普通 -count=1 -v | ordinary.log | 0 | 299 / 324 |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | race-providers.log | 0 | 132 / 146 |
| controller rag/common/rerank/logic rag/chat race -count=1 -v | race-wiring.log | 0 | 188 / 199 |
| providers 精确关键正则 -race -count=10 -v | race-key-stress.log | 0 | 520 / 520 |
| Chat 精确 ^TestChatStream_Session.* -race -count=10 -v | race-chat-key.log | 0 | 70 / 70 |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | 0 | 非 verbose，不以 RUN=0 否认实际执行 |
| go vet ./... | vet.log | 0 | — |
| go build ./... | build.log | 0 | — |

全部测试无 DATA RACE；race 并不证明父取消生命周期正确。以上均在恢复独立补充文件之前运行，不能与后续失败混淆。

## 目标＋独立补充测试

未提交文件来源：上一轮原复现保存在 preserved/；当前复现源码、diff 和哈希在 repros/。当前工作树仅两份：
- server/core/providers/openai/review_managed_session_test.go
- server/internal/logic/chat/review_managed_session_test.go

Chat 背压用例只将旧 Copy child 的 buffer 观察路径调整为新的直接 pipe；验收断言不变。其他原始选中测试未更改语义。

第一组混合验证日志 managed-review.log / race-managed-review.log，均 EXIT 1：7 个顶层用例，普通 6 PASS/1 FAIL；race 十轮 60 PASS/10 FAIL，含 RealClose 两子项共 90 RUN。唯一失败项为新补百炼父取消；原 6 项均各十轮通过。该版源码另存 repros/openai-direct-v1.go.txt，防止与后续增强版混淆。

随后将百炼用例增强为 Registry 入口、一秒观察窗，单独日志 race-registry-bailian-parent.log：10 RUN、10 FAIL、EXIT 1。其他测试没有再重复运行。旧文件中的 TestReviewManagedChatCleanupOwnsFrontend 没有被本轮正则选中，不计入结果。

## 实现者日志核查与未验证边界

实现者日志的通过与本轮干净目标通过一致。其“8 个关键测试”实际列表包含 Chat 4 个、providers 根包 5 个，即 9 个顶层用例；两个 provider 子包显示 no tests to run，不能据此声称验证了百炼/OpenAI 各自的父取消生产链。此计数偏差不是独立阻塞，不为归档格式另开轮次。关键证据缺口已由 B1 真实复现落到正确性问题。

非阻塞建议：新增阻塞型测试的 gate/源取消在 t.Cleanup 中兜底释放，使未来断言失败时也能结束测试拥有任务；本轮实际通过，不据此再添阻塞。

保留原已闭合预算、配置隔离、controller owner、初始化、Reset 等结论；未全面重跑历史审计。2C 六处维度、fingerprint 和新 Embedding 生产启用 guard，2D 在线探针，以及真实 ES/Qdrant、A-4、SQL driver 取消等均保持既有边界，本轮未验证，不写成通过。不宣布整个 Stage 2 完成。
