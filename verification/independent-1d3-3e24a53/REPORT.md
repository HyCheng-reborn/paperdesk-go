**结论：存在阻塞，3e24a533f99aaa8578b848ae550dfd2e10292723 暂不能验收 1D-3。Reset fixture 的主要缺陷已闭合；core 测试交付不完整，组合链完成边界仍未闭合。**

本轮增量：735786966e3e8e8e0f1eb238045ca49209410505..3e24a533f99aaa8578b848ae550dfd2e10292723。复用 linked worktree C:/Zhy/code/go-rag-review-1d3；保存三份临时测试及哈希后 detached 切换。旧报告、原始复现和日志未覆盖。未 fetch、clone、清理他人文件、修改生产代码、提交/推送或更新验收状态。

**P1：目标自身 core 测试无法编译，必要注入接口未提交**

目标位置：server/core/indexer_test.go:2284、2285、2293。

干净目标引用 knowledge.ChunkStore、knowledge.DocStatusUpdater、knowledge.SaveChunksDataWithDeps，但目标 knowledge/chunks.go 仍只有私有 chunkStore/docStatusUpdater，没有该函数。定点普通测试、受影响包 race、串行全量及 vet 均因这三个 undefined 符号失败；测试执行前即编译失败，不能称为命中测试后失败或 race 运行完成。

只读检查原工作区发现 server/internal/logic/knowledge/chunks.go 为 M，三个定义仅存在于该未提交修改中。其来源 diff/hash 保存为 original-workspace-chunks.diff 与 original-workspace-chunks-hash.txt，未将原文件导入审查树，也没有替目标补代码。

后果：目标对象不能独立构建 core 测试；交付所附 core PASS/race/full/vet 结论无法由目标复现。go build ./... 虽退出 0，但它不编译 _test.go，不能证明测试依赖已完整交付。

最小方向：由实现者确认必要接口/共享入口的归属、语义及完整提交，先在干净提交树验证编译；不能只再次运行有未提交修改的原工作区。尤其不能盲目补入目前未提交 helper：它直接 saveChunks(...,updater)，没有 production indexingOnlyUpdater 策略；组合测试必须通过共享的后台策略边界，而非恢复内层 Failed 写入。

**P2：asyncDone 仍是内层返回信号，未覆盖终结/Done；cleanup 契约未被检验**

目标位置：server/core/indexer_test.go:2348-2349、2471-2472、2519-2520、2546-2574；关联 core/indexer.go:169-187。

asyncDone 在 asyncIndexer wrapper 的 defer 中关闭。这个时间点只是 indexAsyncByDocsID 返回，外层 Rag.Index goroutine 才将错误交给 bgErr，随后执行 markFailed，最后释放信号量和 bgWG.Done。因此等待 asyncDone 不等于等待后台终结；Failure_RealChain 仍可能在 Failed 写入前读计数，也不能证明至多一次。

CleanupBeforeDone 现在确实由失败任务进入受控清理，比上轮有改善；但只检查 cleanupDone 尚未关闭，这只证明测试自己的 <-cleanupProceed 阻塞。它没有在阻塞期间调用生产 Shutdown 或观察真实登记，无法证明清理前 Done 没有被错误提前释放。释放后等待 cleanupDone，也仍早于生产 Done。保留真实 1 秒等待并不能修复这个边界。

此外：
- 同一 async wrapper 仍 defer close(asyncDone)，测试又第二次调用 Index，第二个后台会再次关闭同一 channel。恢复 markFailed 不会恢复 asyncIndexer；重复关闭的 fake 缺陷仍存在。第二个后台还保留 1 秒初始等待，而 cleanup 等待的 asyncDone 第一轮早已关闭，所以第二轮可能在测试结束后继续执行。此为目标源码证据；由于本轮 core 编译失败，不声称已在该目标动态观测第二次 panic。
- 成功场景轮询看到 Indexing 就 return，cleanup 只释放 qaBlock、不等待任务终结；未核对 docID 和最终 Active/无 Failed。
- r.markFailed 仍手写并忽略状态错误，实际 prodAsyncIndexer 仍由等价 fake 代替；原始错误链及默认状态写失败报告的组合断言仍不足。
- 目前未提交的 SaveChunksDataWithDeps 直接使用全状态 updater，若直接照搬，会在内层 chunk 失败时写 Failed，外层又写 Failed；提前读计数还可能掩盖第二次写入。此为对未提交补全方案的只读风险分析，不作为目标已运行结论。

最小方向：共享生产 indexingOnly 策略及 chunks 内核，使用真实 prodAsyncIndexer；失败后等生产 Shutdown/WG 完成再计数，准确断言 docID、errors.Is 原错及不执行 QA/Active。cleanup 阻塞期间启动 Shutdown，确认已进入关闭等待且不能完成，释放 cleanup 后才允许 Shutdown 返回。去掉复用一次性 asyncDone 的第二次任务，或使用每任务独立信号并等待全部生产完成；不要用容量 8 时准入第二次代替释放证明。成功场景用 QA-entered channel 协调，再释放并等待任务结束。测试注入等待函数本身没有问题，无需为了“真实”保留一秒延迟；关键是走生产编排/内核并观察正确完成边界。

**Reset：原 fixture 伪造收敛问题已闭合，保留有限覆盖边界**

newTestRagWithShutdown 及超时测试现在取得实例自身 bgWG 指针，对同一对象 Add/Done，不再复制已使用 WaitGroup、不再手工 close 生产 done。正常路径等待生产 ragDoneChan；ExactObjects 真实超时后释放任务，再次 Reset 成功。生产代码本轮未改，上一轮已证明正确的身份检查等保持不变。

本轮干净目标九个 TestReset_* 普通测试、race count=1 均通过；race count=10 实际 90 RUN，全部通过，逐名见 reset-stress-matches.txt。这支持 Reset 必要证据主问题闭合。

非阻塞覆盖局限：DuringWait 和换代测试仍有 100ms sleep；换代仍直接设置全局值而非执行真实 InitWith，代码已明确标注，结论限于身份检查分支；部分失败断言在 RLock 下 Fatal 的清理健壮性及失败路径 release cleanup 仍可改善。生产 InitWith 本身支持 fake 工厂，不应宣称必须使用真实基础设施才能测，但本轮不为该解释单列业务阻塞。

**实测与命中范围**

全部命令实际 cwd：C:/Zhy/code/go-rag-review-1d3/server；日志包含 SHA、完整命令、退出码。Go 1.27.1 windows/amd64；CGO_ENABLED=1、CC=gcc、CXX=g++、GOFLAGS 空、GOTOOLCHAIN=auto，既有 MinGW GCC 8.1.0。GOPROXY=off、GOSUMDB=off；无 online/integration 标签、无真实 DB/ES/Qdrant 或模型访问，未修系统或工具链。

| 命令 | EXIT | 证据 |
|---|---:|---|
| go test ./core -count=1 -v -timeout=60s -run ^TestIndex_BackgroundChunksFailure_RealChain$ | 1，编译失败，未执行用例 | target-core.log |
| go test ./internal/logic/rag -count=1 -v -timeout=60s -run ^TestReset_ | 0，9 RUN | target-1.log |
| go test -race ./internal/logic/rag -count=1 -v -timeout=60s -run ^TestReset_ | 0，9 RUN | target-2.log |
| go test -race ./core ./internal/logic/rag ./internal/logic/knowledge ./internal/cmd -count=1 -timeout=90s | 1，core 编译失败；其余三包通过 | target-3.log |
| go test -race ./internal/logic/rag -count=10 -v -timeout=180s -run ^TestReset_ | 0，90 RUN | target-4.log |
| go test -p 1 ./... -count=1 -timeout=90s | 1，core 编译失败 | target-5.log |
| go build ./... | 0 | target-6.log |
| go vet ./... | 1，同一 undefined 符号 | target-7.log |

Reset 精确命中九项：ClosesVectorStore、VectorStoreCloseError、ShutdownTimeout_PreservesDependencies、ShutdownSuccess_CleansUp、Concurrent_NoRaceOrPanic、DuringWait_GetRagSvrCallable、ShutdownTimeout_PreservesExactObjects、AfterTaskCompletion_SecondResetSucceeds、ConcurrentInitDoesNotCloseNewInstance（均前缀 TestReset_）。

实现者历史 BackgroundChunks 30 PASS 正则确实列出三个用例各十次，但不包含 CleanupBeforeDone；Reset 90 PASS 范围与本轮一致。历史 core 30 PASS、两包 race、full/vet 退出 0 均不能由当前目标独立复现，原因已定位为提交中缺失依赖，不采用历史日志替代目标证据。没有导入未提交 helper 去重现或替它补齐。

本轮 core 阶段在编译处阻断，没有重复运行不可能通过编译的同类 core 压测、旧 core 复现或关闭专项测试。server/core/shutdown_test.go 仍已提交，生产 AfterFunc/Reset/Indexing 修复未变；不将本轮未执行的关闭测试写成通过。历史 0xc0000139 继续仅保留运行异常、race 未完成、根因待确认；本轮未发生该异常。

**保护、结论及下一步**

审查 worktree 保持干净目标；临时测试均保存在本目录 preserved/，未自动复制回来。上轮所有报告与日志保留原位。本轮未修改原工作区未提交 helper 或 Git 配置、分支、远端；未提交、推送、合并或更新项目验收状态。原“已推送”声明未实时联网核验。

仍暂不验收 1D-3：先补齐可独立编译的完整交付，并修正组合链完成/cleanup 证据。生产缺陷在上一轮已闭合，本轮未发现新的生产逻辑回归；不要为填测试而重新设计正确业务行为。剩余 A-4、历史 indexer/common/retriever/rerank race、SQL driver 取消、Stage 4 恢复、History/MCP 边界保持不变。

按用户偏好，复杂的生产策略边界与并发测试修正交给专家模式；只有在专家确认行为和测试通过后，文档、真实日志整理及按路径提交工作交给 qwen3.8flash。分工提示词见 QODER_EXPERT_PROMPT.md 和 QODER_FLASH_PROMPT.md。报告完成后停止。
