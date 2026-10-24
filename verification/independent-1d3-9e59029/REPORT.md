**结论：存在阻塞，当前不能验收 1D-3。**

审查对象为 `17d7654ecc0656dcc638e92791fdca61c743cf21..9e59029133cfdb6b42a449f8dcb625c3708aeaa1`，完整覆盖 a845d21、fcd99d3、e3586a5、9e59029 四次提交。未修改业务代码、进度文档或 Git 配置，未提交、推送、合并，也未 fetch。原工作区没有执行 checkout/reset/clean/stash。

**基线及隔离**

- 原仓库 `C:/Zhy/code/go-rag`，原 HEAD 与目标一致；起点祖先检查退出 0。分支按本地引用核实，不将 origin 跟踪引用视为实时远端。
- 审查 worktree：`C:/Zhy/code/go-rag-review-1d3`，detached HEAD 为目标 SHA。干净验证前后均无跟踪文件差异，见 clean-status.txt（空输出）。测试工作目录均为其 `server/`。
- 检查仓库和父目录，未找到适用 AGENTS.md。累计 diff 的全部测试文件只有 `server/core/indexer_test.go` 和 `server/internal/logic/rag/retriever_test.go`；目标不包含 shutdown_test.go。
- 独立核查原工作区：约 70 项 M 不能等同业务改动。git diff --numstat 实际源码差异仅 dao_test.go 四行字段对齐；另外有已知删除/日志改动。未清理或归入目标差异。
- 环境：Go 1.27.1 windows/amd64；GOOS=windows、GOARCH=amd64、CGO_ENABLED=1、CC=gcc、CXX=g++、GOFLAGS 空、GOTOOLCHAIN=auto；MinGW-W64 GCC 8.1.0。go.mod 声明 Go 1.23.1，GoFrame 2.9.0、Eino 0.6.0、ES client 8.16.0、Qdrant client 1.15.2。WSL 仅列出 docker-desktop，未安装或搭建环境。
- 验证设置 GOPROXY=off、GOSUMDB=off，无 online/integration 标签。未发现真实 DB/ES/Qdrant 或付费模型访问；本地 httptest 不视为外部设施。

**阻塞 1 — P1：Reset 在未收敛时仍关闭依赖并丢失服务引用**

位置：目标 `server/internal/logic/rag/retriever.go:398-413`，特别是 400-402、409、412。

触发：存在已准入任务，直接调用 Reset，任务未在内置 5 秒预算内退出（包括正在完成失败清理、或下游暂不响应取消）。

源码证据：Shutdown 错误只记 Warning，随后无条件执行 vectorStore.Close、vectorStore=nil、ragSvr=nil、ragInit=false；最终返回的仅为 Close 错误。即 Close 成功时，调用者收到 nil，虽然任务仍未结束。QdrantVectorStore.Close 会实际关闭同一客户端；ES Close 为空操作，但服务引用仍被丢弃。

后果：正在使用的依赖提前关闭，且无法经全局服务引用再次等待原任务，违反“超时保留依赖、引用及可识别错误”的契约。正常 cmd 协调器的超时分支是安全的，但不能修复直接 Reset 的这个分支。未发现当前后台实际调用链中依赖 ragMu 的必然持锁死锁，不将其列为已证实问题。

最小方向：Shutdown 未成功时立即返回可识别错误，保留依赖和状态；收敛后才清理，并避免持有全局锁进行长时间等待。保留初始化失败、尚无任务时的清理行为。现有 TestReset_* 仅设置 vectorStore、没有活跃 ragSvr，因此通过不能证明此路径安全。本项为明确源码证据，未声称实测真实 Qdrant 断连。

**阻塞 2 — P2：同步 panic/提前退出未注销 AfterFunc 桥接**

位置：目标 `server/core/indexer.go:119-126`。

触发：注册服务 root 的 AfterFunc 后，syncIndexer.invoke panic；上层 recover 后进程继续服务。runtime.Goexit 等提前展开也会跳过普通顺序注销。

证据：stopAfterFunc 仅在 invoke 正常返回后调用，未 defer。已有 syncCancel 的 defer 取消的是请求派生子 context，不会注销另行挂在服务 root 上的 AfterFunc。容量与 WaitGroup 已释放，但回调及其闭包仍由长期存在的 root 持有，直至服务停止。

最小复现：审查添加的 review_bridge_test.go 调用真实 Rag.Index，使用可观测 context.AfterFunc 根上下文，仅 fake 同步依赖触发 panic；race 执行退出 1，输出 registered bridges=1、stopped bridges=0、occupied slots=0。测试没有复制 Index 编排，没有改生产实现。见 repro.log、repro.diff。实际默认 root 使用标准 cancelCtx，同样会保留未 stop 的回调子节点。

最小方向：注册后立即 defer stopAfterFunc，保证全部退出路径注销；可保留正常返回时尽早 stop。增加断言桥接确实释放的 panic 测试。

**阻塞 3 — P2：chunks 失败时 Failed 写入有两个所有者**

位置：目标 `server/core/indexer.go:182-185`；关联 `server/core/rag.go:174-181`、`server/core/indexer.go:338-341` 以及未修改的 `server/internal/logic/knowledge/chunks.go:90-134`。

触发：后台 SaveChunksData 的 Find/Insert/Update 发生错误，且其末尾 Failed 状态更新成功。

源码证据：生产 saveChunksFn=knowledge.SaveChunksData。saveChunks 收集 chunk 错误，将 status 置 Failed，并在 chunks.go:130 更新文档状态，再返回聚合错误。indexAsyncByDocsID 原样返回该错误，外层终结 defer 再调用默认 markFailed，第二次更新同一 docID 为 Failed。没有“已终结”标记或统一所有权。既有 chunks 测试亦明确断言底层失败会写 Failed；新增核心测试将 saveChunksFn 替换为单纯返回 error 的 fake，因此没覆盖这条组合路径。

后果：同一任务至少两次 Failed 状态写入，违反本轮“至多一次、统一终结”契约；额外 SQL 在故障时也扩大清理工作。本项未声称幂等状态值本身必然造成数据损坏。

最小方向：明确单一终结所有者，例如保留公开 SaveChunksData 的既有行为，提供供后台编排使用的仅传播错误路径，并由后台统一用 cleanupCtx 标 Failed；或使用能明确传递已终结结果的方案。不能通过吞掉 chunk 错误规避。补组合调用链测试。

**干净目标提交的实测结果**

完整命令、SHA、实际 cwd、时间、真实 EXIT 分别保存于 clean-1.log 至 clean-7.log；可重复命令脚本为 run-clean.ps1。

| 命令（server/） | 退出码 | 证据 |
|---|---:|---|
| go build ./... | 0 | clean-1.log |
| go vet ./... | 0 | clean-2.log |
| go test ./core ./internal/logic/rag/... ./internal/cmd/... -count=1 -timeout=120s | 0 | clean-3.log |
| go test -race ./core ./internal/logic/rag/... ./internal/cmd/... -count=1 -timeout=120s | 0 | clean-4.log |
| go test -p 1 ./... -count=1 -timeout=120s | 0 | clean-5.log |
| go test -race ./core -count=10 -v -timeout=180s -run 正则（完整文本见 clean-6.log） | 0 | clean-6.log |
| go test ./core ./internal/logic/rag/... ./internal/cmd/... -list . | 0 | clean-7.log |

十轮 race 精确选择以下 14 项，实际 140 个 RUN，均 PASS，没有 no tests to run：

- TestIndex_AdmissionClosed_Rejects
- TestIndex_AdmissionFull_Rejects
- TestIndex_SyncFail_ReleasesOnce
- TestIndex_SyncSuccess_TransfersOwnershipOnce
- TestIndex_Shutdown_WaitsAdmittedSyncAndBG
- TestIndex_Shutdown_Timeout_KeepsWaiting
- TestIndex_Shutdown_Idempotent
- TestIndex_SyncPanic_ReleasesCapacityAndRegistration
- TestIndex_BackgroundFailure_MarksFailed
- TestIndex_BackgroundPanic_MarksFailed
- TestIndex_BackgroundTimeout_MarksFailed
- TestIndex_BackgroundSuccess_NoMarkFailed
- TestIndex_CleanupCtxIndependent
- TestIndexAsyncByDocsID_RetryRespectsContext

测试通过的解释限制：TransfersOwnershipOnce 使用 fake 返回前信号及 50ms sleep，不能严格证明 defer 已完成；SyncFail_ReleasesOnce 容量是 8，失败后再成功一次也不能排除泄漏一个 slot；WaitsAdmittedSyncAndBG 在 Shutdown 前已经释放同步，并 sleep 100ms，不能单独证明同时等待同步和后台；RetryRespectsContext 使用预取消 ctx，实际命中入口检查，没有进入检索重试等待。失败测试多只观察 fake 的进入/退出或一个信号，没有准确断言 docID、一次性，以及失败 cleanup 结束前 Shutdown 必须继续等待。IndexAndShutdownRace 的额外 Index 调用可能在 closed 设置前进入同一个阻塞 fake，且存在无界 channel 等待；本次通过不证明其始终可收敛。上述限制集中记录为覆盖不足，不按测试数制造业务缺陷。

静态正向结论：同锁准入与 Add、closed 后禁止 Add 的顺序合理；同步失败/panic 容量和登记 defer 释放；后台单次接管，cleanup defer 在 Done 前完成；请求同步 context 保留 values/deadline，同时桥接服务取消；后台从服务 root 派生 timeout；默认初始延迟及检索重试用可取消 select；独立 5 秒 cleanupCtx；默认状态写入失败有日志。集合完整性及 chunks 错误传播仍在。配置默认/上限、先限制分钟再 duration 换算、类型拒绝、Copy/RagConfig/factory 传递未发现本轮新增阻塞。InitWith 本身未新增读取真实全局配置。

真实 cmd 在 GoFrame s.Run 返回后调用共享 ShutdownCoordinator；本地 GoFrame 2.9.0 源码核实 Run 正常返回路径。协调器超时不调 closers，成功才逆序关闭并 errors.Join；cmd 清理链 dao.Close 仅一处，未发现该链 dao 重复关闭。初始化失败 RunInitSteps 仍逆序清理已成功步骤；rag 工厂失败仍关闭已创建向量存储并聚合错误。

**未提交关闭测试：单独补充结果及交付缺口**

原文件 `C:/Zhy/code/go-rag/server/core/shutdown_test.go` 不属于目标，SHA256：`5D660781FF88F4324B20619D3FB76E76C655226A1C3171F87F3A70BE6D89FB55`。来源及哈希见 supplement-source.txt。

在全部干净基线结束并记录状态后，仅复制此文件到审查 worktree；先运行补充测试，再加入审查复现测试。补充测试命令 `go test -race ./core -count=10 -v -timeout=90s -run ^TestShutdownCoordinator_`，退出 0，5 项各 10 次，共 50 个 RUN，见 supplement.log。结果归属“目标提交 + 未提交补充测试”，不能表述为目标自带测试通过。

五项为 ShutdownSuccess_ThenCleanup、ShutdownTimeout_NoCleanup、CleanupPartialFailure_ContinuesOthers、NoResources、NilCloser（均前缀 TestShutdownCoordinator_）。确实调用共享生产 ShutdownCoordinator，没有复制关闭编排；成功及超时、逆序清理、错误链/部分失败有基本覆盖。但成功测试无真实任务；未覆盖同一个协调器首次超时后再次等待并清理；NilCloser 忽略返回 err；超时测试 cleanup 等待 fake 返回信号，未等待整个 Rag 终结。应补强后由实现者确认归属并补提交；本轮将缺失文件合并为一项交付/验收证据缺口，不拆成五项。

**实现者历史证据的限制**

- CHANGES 声称新增五个 TestShutdownCoordinator_*，但目标 Git 对象没有该文件。不能由目标独立复现该声明。
- verification/stage1d3c/race-stress.log 的正则为 TestShutdown_|TestMarkFailed_|TestBackground，日志只出现 TestBackgroundCancelAfterWait_NoAsyncIndexer 十次。TestShutdown_ 不匹配 TestShutdownCoordinator_，TestMarkFailed_ 在目标不存在；不能据此宣称 C 组关闭或失败终结压力覆盖。
- 本轮独立重跑支持当前目标 build/vet/指定包 race/全量普通测试通过；不反向证明历史命令的执行环境和覆盖范围。
- 原工作区 server/verification/stage1d3a/ 是未跟踪错位过程日志，不并入根目录已提交证据。
- 历史 Windows 0xc0000139 仅保留“运行异常、race 未完成、根因待确认”。本轮指定包 race 成功不替代其他历史包的补证，不认定历史异常必然非代码问题。

**非阻塞建议及边界**

- 将关键测试的完成边界推进到生产 Done/Shutdown，并用 channel 协调代替固定 sleep；补准确 docID、Failed 至多一次、清理中 Shutdown 不返回、重试中取消及协调器二次等待。
- ShutdownCoordinator 自身未保护重复或并发 Cleanup，当前 cmd 只调用一次；若未来暴露给多个调用者，应明确一次性清理及错误缓存语义，当前不将未出现的并发调用场景单列阻塞。
- cmd 当前清理列表未调用 chat.Reset；它只清引用，当前退出场景不单列资源关闭阻塞。完整 History context/MCP 生命周期仍在范围外。
- 未验证：A-4 缺 QA 向量 kNN 行为；indexer/common/retriever/rerank 历史 race 补证；driver 对执行中 SQL 取消；Stage 4 持久化恢复；History context、完整 MCP 生命周期及真实基础设施行为。

**验收判断**

1D-3 暂不验收：先修复三项明确契约问题，完善关键退出路径测试，并确认/补提交 shutdown_test.go，再按新 SHA 独立复核。Stage 1 整体仍需本轮收尾、约定的历史 race 补证及 A-4 等未验证项的明确验收处置；真实设施验证不能写成已通过。Stage 4 等明确范围外事项不要求在本轮提前实现。不宣布 Stage 1 完成。

审查 worktree 保留两份明确标记的临时测试（未提交关闭测试及复现测试），生产文件未改；源文件与 diff 均在本证据目录可复查。原工作区未提交测试未被删除或修改。审查结束，等待用户安排修复。

