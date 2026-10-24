# Stage 2B 独立增量复核

结论：**存在阻塞，暂不可验收 2B**。干净目标的全部规定验证通过，但生产共享路径的独立补充复现仍确认两组生命周期问题。

目标：`fbbaeb4c12ab48db68c778ac38bb64676ddcb5d1`；增量基线：`cd1a7acf9b2a381dc12471aa45ac9dff50475447`，已核实对象存在及祖先关系。仅集中复核本轮修复及相邻生命周期回归。

## 工作区与证据层

复用 detached linked worktree `C:/Zhy/code/go-rag-review-1d3`，Git common dir 为 `C:/Zhy/code/go-rag/.git`。切换前原有两份独立测试保存于本目录 `preserved/`（含哈希）；未操作原仓库分支、配置、远端或行尾差异。先在干净目标执行完整验证，再恢复/补充审查测试。最终 `git diff --exit-code -- server` 为 0，只有两份独立测试未跟踪，见 `final-worktree-status.txt`。未修改生产代码、未提交推送、未更新验收状态。

## B1 / P1：失败 cleanup 在前台背压下仍会无限等待

位置：`server/internal/logic/chat/chat.go:243-251, 402-409, 416-440`。

生产调用链：Chat.GetAnswerStream → setupSessionCoordinator → session.Reader → fgWriter.Send / bgWriter.Send → SSE 消费前台；SSE 写失败后停止消费 → cleanupFail → session.Drain → fgCleanupCh.Close → 等待 coordDone。

当前 fgPipe 仍 Copy(2)，消费者持 child0，cleanup 只关闭从不消费的 child1。只有两个 child 都关闭才会关闭父 pipe。当前台容量 8 被填满，Chat 转发阻塞在 fgWriter.Send；关闭 child1 不解除该 Send，session.Drain 也不能取消这个独立 Chat pipe 的阻塞写。转发无法执行关闭 bgWriter 的 defer，后台等不到 EOF，cleanup 等不到 coordDone。需要消费者额外关闭真正的前台 reader 才能退出。

证据：`TestReviewManagedChatFailureBackpressure` 使用真实 OpenAI 构造、httptest SSE 32 chunks 和生产 Chat 编排；消费首条后停读，观察前台 buffer 满，再失败 cleanup。普通复现失败；十轮 race **9 FAIL / 1 PASS**，失败轮在显式补关前台 reader 后退出。一次通过属于调度未进入阻塞写窗口，不能抹去实际失败；也不将其写成十轮必现。未保存部分 assistant。

最小方向：失败 cleanup 必须拥有能够实际解除前台阻塞写的关闭/取消句柄，且关闭幂等、与业务提前 Close 不冲突；等待包含 Chat 转发与后台退出。不要用未消费的 Copy 子 reader 代替真正的关闭所有权。

原“前台提前 Close 后 cleanup panic”已闭合：`TestReviewSessionEarlyFrontendClose` 普通及 race 十轮通过。本项是同一关闭所有权修复仍不完整的背压路径，不再报告旧 panic。

## B2 / P1：Done 未覆盖真实 Recv；父取消背压仍不能独立收敛

位置：`server/core/providers/budget_adapter.go:460-480, 483-494, 500-528`。

WG 只登记 recv supervisor 和 forward。每次真正执行 bsr.Recv 的临时 goroutine（478）不登记；supervisor 可以因 cancelSig 直接 Done，waiter 关闭 session done，但实际 Recv 尚未返回。因此 Close/Done 并不是全部拥有任务的完成证明。源码 465-467 也明确承认临时任务可能残留。

此外该包装层只接收取消函数，父 ctx 取消并不直接关闭其 cancelSig。当输出背压时，HTTP 即使已经取消，转发和接收桥仍可被内部队列卡住，lease slot 不释放，直到额外显式 session.Close。新增 ParentCancel 测试紧接着调用 Close，不能证明父取消自身收敛。

证据：
- `TestReviewManagedCloseWaitsReceiver`：真实 OpenAI SDK/HTTP 路径，仅在响应 Body.Read 返回取消错误前设置可释放闸门。实际 Recv 仍不能返回时，Close/Done 已返回。普通及 race 十轮均失败。测试随后释放闸门，清理全部任务；不是要求任意底层阻塞立即消失，而是要求尚未退出时不能宣告完整完成。
- `TestReviewManagedParentCancelUnderBackpressure`：真实 SDK SSE，不消费输出，观察背压后只取消父 ctx。服务端已收到取消，session 仍未 Done，进程 InFlight=1，预留仍存在；额外 Close 后才释放。普通及 race 十轮均失败。

最小方向：将真正执行 Recv 的任务纳入拥有关系与等待，不能以新建未登记裸 goroutine 隐藏阻塞。父取消、显式关闭、错误和 EOF 应进入同一幂等终结路径，并解除输出背压；Done 必须在实际拥有任务结束后关闭。保留取消中断真实 HTTP、保守结算及单一消息消费者。

## 干净目标独立实测

工作目录统一为 `C:/Zhy/code/go-rag-review-1d3/server`。完整命令、正则、SHA、时间和退出码见各日志及 `run-clean.ps1`。

环境：Go 1.27.1 windows/amd64；MSYS2 UCRT64 GCC 16.2.0，Binutils 2.47.20260726；仅进程 PATH/CC/CXX/CGO_ENABLED=1；GOFLAGS 空，GOPROXY/GOSUMDB=off。见 environment.log。

| 项目 | 日志 | 退出码 / 实际顶层 PASS |
|---|---|---|
| 受影响包普通测试 | ordinary.log | 0 / 295 |
| providers/... race 一轮 | race-providers.log | 0 / 130 |
| controller/common/rerank/logic rag/chat race | race-wiring.log | 0 / 186 |
| 精确关键正则 race 十轮 | race-key-stress.log | 0 / 500 |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | 0 |
| go vet ./... | vet.log | 0 |
| go build ./... | build.log | 0 |

以上均在补充文件恢复之前完成，无 DATA RACE。五处旧测试签名问题已闭合；目标自带测试可编译。通过 race 不代表生命周期契约已满足。

## 目标加独立复现测试

来源与完整 diff/哈希：`repros/`；工作树文件：
- `server/core/providers/openai/review_managed_session_test.go`（保留原复现）
- `server/internal/logic/chat/review_managed_session_test.go`（保留原复现并新增背压失败用例）

命令对两包使用日志中冻结精确正则，普通 -count=1 与 -race -count=10，均 -timeout=60s -v。日志 `managed-review.log` / `race-managed-review.log`，均 EXIT 1。

| 测试 | 普通 | race 十轮 |
|---|---|---|
| ManagedRealClose（百炼/OpenAI 两子项） | PASS | 10 PASS，各子项 10 PASS |
| ManagedNoBudgetABC | PASS | 10 PASS |
| SessionEarlyFrontendClose | PASS | 10 PASS |
| ManagedCloseWaitsReceiver | FAIL | 10 FAIL |
| ManagedParentCancelUnderBackpressure | FAIL | 10 FAIL |
| ManagedChatFailureBackpressure | FAIL | 9 FAIL / 1 PASS |

race 顶层合计 31 PASS / 29 FAIL；包括子测试共有 80 RUN；无 DATA RACE。文件中的旧 ManagedChatCleanupOwnsFrontend 未选中，不计为本轮结果。两份 cli-error 日志为审查命令首次参数拼接错误（未执行测试），已纠正，不能作为代码失败证据。

## 实现者证据及边界

实现者宣称的普通/race 通过，与本轮干净目标通过不矛盾。问题在验收观察边界：Done 测试没有等待真正的临时 Recv；父取消测试又调用 Close；Chat 失败测试在短流/读完 EOF 后失败，没有容量 8 的活跃前台背压。这些测试不足以证明本轮两组契约。

实际百炼/OpenAI 受控 Close 的 HTTP 取消、无预算 ABC 完整传递、原重复关闭 panic 均有本轮独立正面证据。保留此前 owner、配置、初始化、预算等已闭合结论，不扩大重审。

非阻塞建议：测试名称与断言应区分“转发任务退出”和“全部拥有任务退出”；无需为命名另开修复轮次。

2C 维度/模型身份 guard、索引 fingerprint、新 Embedding 启用门禁与 2D 在线探针仍保留；本轮未访问真实设施或模型，未重新审计 Stage 1，不宣布 Stage 2 完成。
