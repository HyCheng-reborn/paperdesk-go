**结论：存在阻塞，89cc769440b6b405c2ef16eb78a035f486f03fab 暂不能验收 1D-3。**

本轮仅审查 `9e59029133cfdb6b42a449f8dcb625c3708aeaa1..89cc769440b6b405c2ef16eb78a035f486f03fab` 的阻塞修复及相邻回归，并参照前轮累计范围、REPORT.md 和 HANDOFF.md。未重新 clone、fetch、开展无关全项目源码审计或修改验收状态。

**原三个问题的闭合情况**

| 原问题 | 判断 | 证据 |
|---|---|---|
| Reset 超时关闭依赖 | 部分修复，未闭合 | 超时现在返回带 DeadlineExceeded 的包装错误且保留资源；等待不持 ragMu。但新增无锁全局读取及缺失实例身份校验，见 P1 |
| 同步 panic 未 stop AfterFunc | 已闭合 | 注册后 defer stop；原始复现 race 通过，registered=1、stopped=1、slots=0；补充四种退出路径十轮通过 |
| Failed 重复写入 | 重复写本身在生产装配上已消除；完整状态契约未闭合 | New 绑定 WithoutStatus → 共享 saveChunks 内核传 nil → 不会在内层写 Failed；但同时丢失成功 Indexing，见 P2，且目标测试未串起后台与 chunks 内核 |

**阻塞 P1：Reset 解锁后访问全局实例且未校验身份（本次修复引入）**

目标位置：`server/internal/logic/rag/retriever.go:400-409`，关键 402；相关写入 417。

Reset 在锁内检查 ragSvr 非 nil 后解锁，却直接执行全局 `ragSvr.Shutdown`，没有先捕获局部实例。并发 Reset 可在此时将 ragSvr 清空，造成无锁读写竞争及 nil receiver panic。等待成功后重新加锁，也没有确认当前 ragSvr/vectorStore 仍为此前等待的实例。

实际复现：审查新增 `server/internal/logic/rag/review_reset_test.go` 的 TestReviewConcurrentResetUsesStableInstance，在无已准入任务、已初始化的生命周期 fixture 上并发调用真实生产 Reset。fixture 在发布前设置 root/cancel/done，不复制 Reset 编排；各轮结束等待全部调用者，未复制 WaitGroup。race 命令退出 1，报告生产 `retriever.go:402` 读与 `:417` 写；捕获 9 次空指针 panic。见 reset-repro.log、reset-repro.diff。

即便只修复无锁读，仍存在源码可证的换代窗口：A 等旧实例收敛后、重新加锁前被暂停；B 完成旧实例 Reset；InitWith 发布新实例；A 恢复后关闭当前 vectorStore 并清空新实例。此处没有对新实例执行 Shutdown。此换代场景为源码时序证据，未宣称本轮动态复现已运行 Init 换代。

最小方向：锁内捕获待停实例及匹配的向量存储/代次；解锁等待捕获实例；失败保留状态并返回可识别错误；成功重新加锁后确认身份未变化，只有仍属于同一代才清理。并发 Reset 对同一资源只清理一次，已换代时不得触碰新对象。初始化失败的无任务清理路径及错误链继续保留。不能把持锁等待恢复回来规避竞态。

目标新增 TestReset_ShutdownTimeout_PreservesDependencies 证明单次超时保留行为；TestReset_ShutdownSuccess_CleansUp 将 ragSvr 设 nil，根本未进入 Shutdown 等待成功路径。两者没有覆盖换代/并发。超时测试通过 unsafe 复制一个 Add(1) 的 WaitGroup 且永不 Done，会遗留等待 goroutine；本轮未将其单独列业务阻塞，但它不能作为任务结束后重试成功的证明。

**阻塞 P2：WithoutStatus 同时移除了后台成功 chunks 持久化后的 Indexing（本次修复引入）**

目标位置：`server/internal/logic/knowledge/chunks.go:65-68,137-141`；装配 `server/core/rag.go:174`；后续阶段 `server/core/indexer.go:338-357`。

生产链：Controller.Indexer 先创建 Pending 文档 → Rag.Index → 后台 prodAsyncIndexer → indexAsyncByDocsID → SaveChunksDataWithoutStatus → saveChunks(...,nil) → QA/embedding → 最终 Active。新入口跳过全部文档状态写入，没有任何替代 Indexing 更新。成功写 chunks 后，在 QA/embedding 仍运行期间，文档仍是 Pending；原来是 Indexing。全仓 Go 源码核查未发现后台另有 Indexing 写入。

既定契约证据：原 HANDOFF 明确要求保留该成功状态；design/stage1d-plan.md §8.4 仍记载 Indexing 由后台 SaveChunksData 写入；公开入口及现有 chunks 测试保留成功 Indexing 语义。用户未授权变更此契约。这里是明确生产路径状态回归，不是命名问题。原有成功状态写入失败即传播、不继续后续阶段的边界也被该后台路径一并绕过。

最小方向：只分离 Failed 终结责任，不全盘删除状态处理。公开 SaveChunksData 的签名、状态及聚合错误兼容性保持；后台共享内核遇 chunk 错误仅传播，由外层独立 cleanupCtx 唯一写 Failed；成功持久化后按既有边界写 Indexing，写失败须传播并停止 QA/embedding，失败统一终结；全部后续成功才 Active。实现可选明确策略/专用入口，但应共享持久化内核，不能复制业务算法。

必要证据缺口（归并在本项）：TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed 只配置 fakeAsyncIndexer 返回字符串错误，根本未执行 prodAsyncIndexer/indexAsyncByDocsID/SaveChunksDataWithoutStatus。TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite 单独测 helper 的 nil updater 错误路径。两者分别通过不能证明生产组合链的 Failed 次数、docID、Indexing 或状态写失败处理。需新增调用真实后台编排和共享 chunks 内核的组合测试，不可将 fake 模拟描述为已覆盖。此回归为源码证据，本轮没有连接真实数据库去制造状态记录。

**AfterFunc 的验证与关闭交付**

- 原始复现保持原内容，切换前保存到 preserved/review_bridge_test.go 并记录哈希；先复制到新目标运行 `go test -race ./core -count=1 -v -timeout=60s -run ^TestReviewSyncPanicStopsCancellationBridge$`，退出 0，见 original-repro.log。随后移回 preserved/executed-original-review_bridge_test.go，建立新目标自身的干净验证层。
- 新增临时 TestReviewBridgeAllSyncExitPaths 真正观察同步桥接注册/stop，并覆盖正常、error、panic、runtime.Goexit；每次同时等待 Shutdown 并检查容量释放。与原复现共十轮 race，退出 0，见 bridge-paths.log。以第一次 root 订阅识别同步桥接，排除成功路径新增的合法后台 timeout 订阅。
- 审查扩展测试首版错误地把后台 timeout 的第二个订阅也计入“同步桥接”，导致 success 误报。首版源及 bridge-paths-v1.log 保留，不计为产品失败；修正测试观察范围后重新运行，上述 v2 通过。未改生产代码使测试通过。
- 目标新增的两个 TestIndex_Sync*StopsAfterFuncBridge 仅验证 Shutdown 可结束，无法证明 stop 执行。闭合结论来自源码及独立观察测试，不依赖这两个弱断言。
- `server/core/shutdown_test.go` 已进入目标，blob 为 `6ae6e2832ea4587bd3e630ac46c2d9954825b016`，含 7 项专项测试（原 5 项＋2 项）。新目标所有 baseline 测试使用该已提交文件，没有再导入原工作区未跟踪关闭测试。交付缺口已闭合。

**验证结果及准确命中范围**

工作目录均为 `C:/Zhy/code/go-rag-review-1d3/server`。日志各自记录完整命令、新 SHA、cwd、时间及 EXIT。Go 1.27.1 windows/amd64，CGO_ENABLED=1、CC=gcc、CXX=g++、GOFLAGS 空、GOTOOLCHAIN=auto；既有 MinGW GCC 8.1.0；未升级工具链或更改系统。GOPROXY=off、GOSUMDB=off，无 online/integration 标签，无真实 DB/ES/Qdrant 或付费模型调用。

| 层及命令 | 退出码 | 日志 |
|---|---:|---|
| 新目标＋原始复现，race count=1 | 0 | original-repro.log |
| 干净新目标：新增修复与关闭用例普通测试，精确正则、count=1、v、timeout=90s | 0 | target-1.log |
| go test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=120s | 0 | target-2.log |
| go test -race ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=120s | 0 | target-3.log |
| 干净新目标：三个受影响包精确正则，race count=10、v、timeout=180s | 0 | target-4.log |
| go test -p 1 ./... -count=1 -timeout=120s | 0 | target-5.log |
| go build ./... | 0 | target-6.log |
| go vet ./... | 0 | target-7.log |
| go test ./core ./internal/logic/rag ./internal/logic/knowledge ./internal/cmd -list . | 0 | target-8.log |
| 独立并发 Reset 最小复现，race count=1、v、timeout=60s | 1，DATA RACE＋9 次 panic | reset-repro.log |
| 独立桥接四退出路径＋原复现，race count=10、v、timeout=90s | 0 | bridge-paths.log |

干净目标十轮精确正则（实际传入单一 -run 参数）：

`^(TestIndex_Sync(Panic|Error)_StopsAfterFuncBridge|TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed|TestReset_Shutdown(Timeout_PreservesDependencies|Success_CleansUp)|TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite|TestShutdownCoordinator_.*)$`

实际命中 13 个名字，各 10 次、合计 130 RUN，全部通过，非 no tests to run；逐名计数见 stress-matches.txt：

- TestIndex_SyncPanic_StopsAfterFuncBridge
- TestIndex_SyncError_StopsAfterFuncBridge
- TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed
- TestReset_ShutdownTimeout_PreservesDependencies
- TestReset_ShutdownSuccess_CleansUp
- TestSaveChunks_WithoutStatusUpdater_SkipsStatusWrite
- TestShutdownCoordinator_ShutdownSuccess_ThenCleanup
- TestShutdownCoordinator_ShutdownTimeout_NoCleanup
- TestShutdownCoordinator_CleanupPartialFailure_ContinuesOthers
- TestShutdownCoordinator_NoResources
- TestShutdownCoordinator_NilCloser
- TestShutdownCoordinator_ShutdownTimeout_ThenWaitSuccess
- TestShutdownCoordinator_CleanupFailsBeforeConverged

名称命中不等于覆盖正确：Failed 组合路径、Reset 成功等待和实例换代并未被这些目标测试覆盖。独立桥接十轮另有两个顶层测试，其中四退出路径按 success/error/panic/goexit 子测试各十次通过，不能与干净目标层混算。

实现者 `verification/stage1d3-fix/race-stress.log` 的 20 PASS 准确范围是仅在 ./core 命中 TestIndex_SyncPanic_StopsAfterFuncBridge 和 TestIndex_BackgroundSaveChunksFailure_NoDuplicateFailed 两项各十次。TestReset_Shutdown 属于另一个包，没有被该命令执行；该正则也没有包含关闭协调器。不能称为三个阻塞及关闭协调的完整压测。

本轮没有出现 0xc0000139。历史异常仍只记录“运行异常、race 未完成、根因待确认”，不认定非代码问题，也不因本轮成功抹除历史边界。

**保存及工作区保护**

复用 linked worktree，目标对象已在本地、前次目标为其祖先。切换前保存旧两份未跟踪测试及 SHA256 至本目录 preserved/；旧 REPORT.md、HANDOFF.md 和全部旧日志保留原位。未覆盖或切换原工作区，未清理他人文件，未改 remote/config/branch。关闭测试以新目标版本为准，旧补充副本只作历史证据。

本目录含 run-review.ps1、environment-and-scope.txt、stress-matches.txt、clean-target-status.txt，以及临时测试源和 diff。干净目标验证完成后才加入独立并发 Reset 及桥接扩展测试。最终审查 worktree 仅两个未跟踪文件：server/core/review_bridge_paths_test.go、server/internal/logic/rag/review_reset_test.go；git diff --exit-code HEAD 为 0，生产文件未改。

**剩余边界与验收**

初始化失败 RunInitSteps 清理逻辑未改，受影响 cmd/初始化测试通过；无任务路径没有发现此次引入的独立回归。公开 SaveChunksData 原有状态与错误测试仍通过，chunk 原始错误在共享内核继续聚合传播；后台错误仍阻断后续索引及 Active，默认 markFailed 状态写失败报告逻辑未变。这里不把尚未实现的组合测试算作通过。

非阻塞/范围外边界继续保留：A-4 缺 QA 向量 kNN、历史 indexer/common/retriever/rerank race 补证、driver 对执行中 SQL 的取消响应、Stage 4 持久化恢复、History context 及完整 MCP 生命周期。未重跑未受影响历史压测，也不提前实现范围外事项。

1D-3 暂不验收：修复 Reset 并发/换代问题，恢复后台成功 Indexing 的既有契约，补真实组合链验证后以新完整 SHA 增量复核。可交给本地 Qoder 的修改提示词另见 QODER_FIX_PROMPT.md。未提交、推送或更新任何项目验收文档。报告完成后停止。
