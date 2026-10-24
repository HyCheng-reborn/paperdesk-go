**结论：生产修复已消除此前可证实的缺陷，未发现新的生产逻辑回归；必要验收测试仍存在真实性缺口，1D-3 暂不验收。**

目标：735786966e3e8e8e0f1eb238045ca49209410505；增量起点：89cc769440b6b405c2ef16eb78a035f486f03fab。仅审查本次修复及相邻调用链，沿用前两轮 REPORT/HANDOFF 和原始复现，未重新 clone 或做无关全项目源码审计。

**生产修复判断**

| 原问题 | 本轮判断 | 证据 |
|---|---|---|
| Reset 超时保留、无锁读取、换代误关闭 | 源码修复成立；旧并发复现已通过 | retriever.go:398-446 锁内捕获 target/targetVS，解锁只调用局部 target.Shutdown；失败保留状态；重加锁后检查实例身份并在锁内单次清理。独立并发复现 race 十轮通过 |
| AfterFunc 同步退出未注销 | 保持闭合 | 未回退 defer stop；原始复现及正常/error/panic/Goexit 四路径独立 race 十轮通过 |
| 重复 Failed 与 Indexing 丢失 | 源码修复成立 | core/rag.go:174 绑定 SaveChunksDataIndexingOnly；chunks.go:77-90 用 indexingOnlyUpdater 包装共享 saveChunks 内核：失败只传播、跳过内层 Failed，成功 Indexing 仍执行，写入错误仍聚合并阻断后续 QA；外层 markFailed 继续唯一终结 |

公开 SaveChunksData 原有签名及状态处理未改；WithoutStatus 虽保留但不再用于后台生产装配。默认 markFailed 使用独立有限 cleanupCtx、状态写入失败日志及 Done 在清理之后的 defer 顺序未回退。配置、集合完整性、原始 chunk 错误链和 Active 成功条件未发现本次新增回归。

Reset 的 targetGen 是布尔初始化标志，并非递增代次；当前生产 InitWith 发布新 Rag 指针，配合 target 指针身份检查足以阻止已识别换代窗口，不因命名或未发生的指针复用假设新增问题。当前生产写入入口下，实例/向量存储在同一锁内发布、清空。

独立新增 TestReviewResetTimeoutThenRetryRealRegistration 直接对实例内真实 bgWG 登记，没有复制 WaitGroup、没有手工 close done。实际验证等待期间 GetRagSvr 可调用、5 秒超时 errors.Is(DeadlineExceeded)、精确保留对象及初始化状态、不提前 Close、真实 Done 后再次 Reset 成功且 Close 仅一次。race 退出 0。测试未提交，不能作为目标自带证据。

**剩余阻塞 1（P2）：chunks/终结验收仍以模拟行为替代共享生产内核**

目标位置：server/core/indexer_test.go:2232-2271、2287-2293、2365-2369、2431-2434、2469-2518。

- newFullChainTestRag 使用 fakeAsyncIndexer 重写 prodAsyncIndexer 适配，并手写 markFailed，且忽略状态更新错误。虽调用真实 indexAsyncByDocsID，但核心 saveFn 没有进入 knowledge.SaveChunksDataIndexingOnly 或共享 saveChunks。
- Failure_RealChain 的 saveFn 直接 return sentinelInsert；Success_WritesIndexing 与 StatusWriteFailure_Propagated 的 saveFn 自行调用 updateFn(Indexing)。这不能检验真正 indexingOnlyUpdater 的装配、chunk 错误和状态错误如何穿过实际内核，也不能证明整个组合链 Failed 恰好一次。
- Failure_RealChain 的 markFailedCalled 在真实记录状态前发信号，asyncDone 又在 saveFn 返回前关闭；之后检查计数可能早于状态写入。未精确断言 docID/原始错误/Active 未调用。不是数据竞争（切片有锁），但存在完成事件语义错误。
- CleanupBeforeDone 的第一个 fake 返回 nil，根本不会触发失败清理；没有等待 cleanupStarted，也没有在清理阻塞期间调用 Shutdown 验证其不返回。容量为 8 时再成功准入一次也不能证明前一 slot 已释放。
- 同一测试第二次复用 fake，重复 close(asyncDone)，由生产 recover 捕获。在 target-1.log:58-65 实际记录 `Index background panic: close of closed channel`，栈指向 indexer_test.go:2477，而测试已经显示 PASS。此为 fake 错误被生产 recover 吞掉的实测证据，不是生产恢复逻辑缺陷。

触发条件：以本次新增测试作为 Failed、Indexing、状态错误传播和 cleanup-before-Done 的验收依据。

实际后果：即使生产适配器再次绑定成公开全状态入口或 WithoutStatus，模拟 saveFn 的核心测试仍可能通过；cleanup 完成边界也未被证明。此项与此前明确要求的真实组合链证据直接相关，不能用测试数/现有 PASS 消除。

最小方向：用真实 prodAsyncIndexer、共享 chunks 内核和实际 indexingOnly 策略，注入最底层存储/状态依赖做确定性组合测试；不要在 fake 中重写写状态规则。覆盖 chunk 失败只 Failed 一次且 docID/原错正确、成功 chunks 后 QA 阻塞时 Indexing、Indexing 写入失败停止 QA/Active、终结状态写入失败报告。另用真正失败任务进入受控 cleanup，在未释放前证明 Shutdown 不返回，释放后才完成登记。所有完成断言推进到生产 Shutdown/Done，不使用固定 sleep 或 fake 返回前信号；修复重复关闭 channel 的 fake，不让 recover 将其变成 PASS。所需依赖注入边界应复用内核，避免为测试复制业务算法。无需重写目前正确的生产行为。

**剩余阻塞 2（P2）：Reset fixture 伪造收敛，不符合实际 WaitGroup/done 所有权**

目标位置：server/internal/logic/rag/retriever_test.go:858-878，特别是 865、869、876-877；使用该 fixture 的测试在 884、929、1030、1062。

newTestRagWithShutdown 在本地 bgWG.Add(1) 后，通过 reflect.Set 将 WaitGroup 值复制进 Rag；释放 goroutine 对原本地 bgWG.Done，而非 Rag 内副本 Done，并手工 close(done)。生产 Rag.Shutdown 仍对副本 Wait，永不归零，却可因手工 done close 返回 nil。

后果：这些测试中的“成功收敛”并不是生产 wait 完成，绕过了本轮最关键的所有权契约；遗留等待 goroutine。AfterTaskCompletion_SecondResetSucceeds 在第一次 Reset 前就 release，没有覆盖超时后真实任务完成再重试；ConcurrentInitDoesNotCloseNewInstance 直接改全局指针，未运行真实 Reset＋Init 交接；现有固定 sleep 不能保证进入等待。ShutdownTimeout_PreservesExactObjects 又保留永不 Done 的副本，失败时还可能在持 RLock 下 Fatal 导致 cleanup 卡住。

最小方向：不要复制使用后的 WaitGroup；直接登记实例自身真实计数，释放也 Done 同一对象，done 只由生产 Shutdown 关闭。每个测试 t.Cleanup 必须最终释放并等待结束。以 channel 确认已进入等待；测试精确对象保留、真实超时后再成功、并发 Close 至多一次及换代不动新实例。若使用字段反射，也只获取实例内指针，不复制同步原语。可参考本轮未提交的 review_reset_lifecycle_test.go，经核实归属后补入目标；不要自动把审查文件整包复制或把其结果算作提交内证据。

本项是目标交付测试缺口；独立真实登记复现的通过支持生产修复正确，不等于目标已具备这些回归测试。与前项各合并为一项必要证据阻塞，不按测试数量拆问题。

**验证结果**

工作目录：C:/Zhy/code/go-rag-review-1d3/server。全部日志记录目标 SHA、层级、完整命令、实际 cwd、EXIT；干净目标运行脚本为 run-review.ps1。

环境：Go 1.27.1 windows/amd64；CGO_ENABLED=1、CC=gcc、CXX=g++、GOFLAGS 空、GOTOOLCHAIN=auto，既有 MinGW GCC 8.1.0；GOPROXY=off、GOSUMDB=off。未升级工具链或排障系统，未加 online/integration 标签、未连接真实 DB/ES/Qdrant 或付费模型。本轮未发生 0xc0000139；历史仍只记运行异常、race 未完成、根因待确认。

| 验证 | EXIT | 日志 |
|---|---:|---|
| 原始并发 Reset、同步 panic、桥接四退出路径，race count=1 | 0 | original-repros.log |
| 新目标新增修复＋关闭专项精确选择，普通 count=1 | 0 | target-1.log |
| go test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=150s | 0 | target-2.log |
| 上述受影响包 -race -count=1 -timeout=150s | 0 | target-3.log |
| 三包关键用例 -race -count=10 -v -timeout=180s，完整正则见 target-4.log | 0 | target-4.log |
| go test -p 1 ./... -count=1 -timeout=150s | 0 | target-5.log |
| go build ./... | 0 | target-6.log |
| go vet ./... | 0 | target-7.log |
| 四包 go test -list . | 0 | target-8.log |
| 独立真实登记 Reset 超时→释放→重试，race count=1 | 0 | reset-real-registration.log |
| 原始并发 Reset＋桥接独立复现，race count=10 | 0 | original-repros-stress.log |

干净目标十轮实际命中 18 项、180 RUN（逐名计数见 stress-matches.txt）：4 个 core 新测试（3 个 BackgroundChunks＋CleanupBeforeDone）、5 个新 Reset、2 个知识层 updater/helper 测试、7 个 ShutdownCoordinator 测试。没有 no tests to run；但上述 PASS 不扩大为缺失组合链及真实收敛已验证。core 日志中 fake 重复关闭 panic 如实保留。

Qoder 的 verification/stage1d3-r2/race-stress.log 运行范围仅 ./core，正则 TestReset_Concurrent|TestIndex_BackgroundChunks 实际只命中 3 个 BackgroundChunks 测试各十次，合计 30 PASS。没有运行另一个包里的 Reset，也没有选择 CleanupBeforeDone、knowledge 内核或关闭协调器，不能视为全部修复的压测证据。

shutdown_test.go 仍已提交，blob=6ae6e2832ea4587bd3e630ac46c2d9954825b016，7 项专项测试未依赖原工作区未跟踪文件。此前交付缺口保持闭合。

**证据分层及保护**

复用 linked worktree，切换前保存两份旧未跟踪测试及哈希到本目录 preserved/，确认 89cc769 为新目标祖先后仅 detached 切换审查目录。目标在本地，无需 fetch；Qoder 的“已推送”仅作交付声明，本轮不作实时远端确认。旧 REPORT.md、HANDOFF.md 和全部前轮日志保留原位。

原独立测试先运行再移回 preserved/executed-*，随后建立干净目标层；全部干净验证完成后才加入独立测试。新目录：C:/Zhy/code/go-rag/verification/independent-1d3-7357869。

- 干净目标：target-1.log～target-8.log、stress-matches.txt、clean-target-status.txt、run-review.ps1。
- 目标＋历史独立复现：original-repros.log、original-repros-stress.log；原源文件在 preserved/，后者运行时仅选择旧复现正则。
- 目标＋新增真实登记验证：review_reset_lifecycle_test.go、reset-real-registration.diff、reset-real-registration.log。临时执行位置 server/internal/logic/rag/review_reset_lifecycle_test.go。
- 最终临时测试另有 server/core/review_bridge_paths_test.go、server/internal/logic/rag/review_reset_test.go，均未提交。不自动复制回原工作区。

未改生产代码、原工作区分支/config/remote、项目进度或验收文档，未提交、推送、合并或删除目录。

**验收及下一步**

未发现新的生产正确性阻塞；两项剩余阻塞均为此前明确要求的必要验收证据真实性，不是命名、格式或一般文档问题。建议本次修复重点限于测试和最小必要依赖边界，不再次改动已证明正确的生命周期/状态语义。

补齐真实组合链与生产所有权一致的 Reset fixture、消除 fake panic 假通过后，按新完整 SHA 定点复核。历史 A-4、indexer/common/retriever/rerank race、driver 执行中 SQL 取消、Stage 4 恢复、History/MCP 等边界继续保留，不扩大本轮。不宣布 Stage 1 完成。给本地 Qoder 的修复提示词见 QODER_FIX_PROMPT.md。报告完成后停止。
