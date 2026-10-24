请基于 735786966e3e8e8e0f1eb238045ca49209410505 处理独立复核剩余的两项必要测试证据阻塞。完整报告：C:/Zhy/code/go-rag/verification/independent-1d3-7357869/REPORT.md。先读 AGENTS.md，保护原工作区既有修改；不要覆盖/清理审查 worktree 或旧日志。

先明确：生产 Reset 和 Indexing/Failed 语义的本轮修复源码成立，旧并发复现及独立真实 WaitGroup 超时重试均通过；未发现新的生产逻辑回归。本轮优先修测试，不再重写这些正确行为，不为命名或文档改动另开范围。

1. 真正连接后台编排与共享 chunks 内核，修复 cleanup 假通过。
现有 newFullChainTestRag 使用模拟 saveFn 自行 return sentinel 或 updateFn(Indexing)，没有调用实际 SaveChunksDataIndexingOnly/共享 saveChunks；复制 markFailed 并忽略其更新错误。不能把这称为已验证组合链。
请以真实 prodAsyncIndexer、真实 indexAsyncByDocsID、共享 chunks 内核及 indexingOnly 策略组合，注入最低层 chunkStore/statusUpdater 等依赖；如需最小依赖边界，应复用内核，不能在 fake 中重复写状态规则。保持不连接真实外部设施。
断言准确 docID、chunk 原始 sentinel 的 errors.Is、Failed 恰好一次；成功 chunks 后 QA 受控阻塞时已经 Indexing；Indexing 状态写失败阻断 QA/Active 并统一终结；终结状态写失败有报告。不要用入口预取消替代中途取消，不用信号发出前的数据作完成证明。
CleanupBeforeDone 第一个 fake 返回 nil，根本不触发清理；第二次复用 fake 会重复 close(asyncDone)，生产 recover 吞掉 panic 后仍 PASS（target-1.log 已有实际栈）。请用真实失败任务进入 cleanup，等待 cleanupStarted 后在未释放期间验证 Shutdown 不返回/登记未完成，释放后等待生产 Done。修复 fake 生命周期，不让测试依赖生产 recover 吞错。删除固定 sleep 完成假设；等待生产完成后再检查写入次数，不在状态写入前发信号就断言结束。

2. 修复 Reset fixture，不复制已使用 WaitGroup、不手工关闭生产 done。
retriever_test.go:newTestRagWithShutdown 先 bgWG.Add(1)，reflect.Set 复制进 Rag，随后 Done 原本地副本并自行 close(done)，导致 Rag 内 WG 永不归零却伪装成功。改为获取实例自身 bgWG 指针，在同一对象 Add/Done；done 只能由真实 Rag.Shutdown 关闭。t.Cleanup 必须释放所有等待并等结束。
用 channel 证明已进入等待，覆盖可识别超时保留精确对象/状态、不提前 Close、释放真实任务后二次 Reset 成功、并发关闭一次、换代不影响新实例。换代测试要明确其实际驱动路径，优先真实 Reset/Init；不能把直接改全局值描述成已执行真实交接。
可只读参考独立测试 C:/Zhy/code/go-rag/verification/independent-1d3-7357869/review_reset_lifecycle_test.go（已 race 通过），确认归属后再有选择补入，不自动整包复制审查文件，不把未提交测试算入目标自带证据。

保持 AfterFunc 修复和已提交的 7 个 ShutdownCoordinator 测试。公开 SaveChunksData 原有兼容性不变，后台成功 Indexing、Failed 唯一所有者、错误链和 Active 条件不回退。

验证：先新增/修正测试，后受影响包普通/race；按准确包与完整测试名十轮，检查实际 RUN 与 panic/error 日志，不能只看 PASS；必要串行全量/build/vet。现有“30 PASS”只有 ./core 三个 BackgroundChunks 各十次，没有 Reset/关闭协调器；新证据请逐名记录。不要升级工具链或连接付费模型/真实外部服务。

交付新完整 SHA、测试源及实际命中/退出码日志供定点复核；不宣称整个 Stage 1 验收完成，不清理原仓库或审查目录，不合并 main。
