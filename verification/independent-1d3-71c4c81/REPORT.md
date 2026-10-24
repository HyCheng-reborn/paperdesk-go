**结论：有明确边界可通过。71c4c81cb6a3eae060365e1f59f6dfe79aed83ce 未发现剩余阻塞，本轮 Stage 1D-3 可以验收；不代表整个 Stage 1 完成。**

本轮审查 3e24a533f99aaa8578b848ae550dfd2e10292723..71c4c81cb6a3eae060365e1f59f6dfe79aed83ce，结合此前累计审查及证据复核必要调用链，未做无关全项目源码审计。

**两个前次阻塞的闭合**

1. 依赖不完整：已闭合。
目标知识层 chunks.go 已提交 ChunkStore、DocStatusUpdater、SaveChunksDataWithDeps。WithDeps 与生产 SaveChunksDataIndexingOnly 同样将 updater 包装为 indexingOnlyUpdater，再进入同一个 saveChunks 内核；不再把全状态策略误用于后台。chunk 失败保留原错、跳过内层 Failed，外层统一终结；成功持久化后仍写 Indexing；Indexing 写入失败继续传播并阻断 QA/Active。公开 SaveChunksData 原有兼容语义未改变。
干净目标 core 测试可编译，build/vet/普通/race/串行全量全部通过，不依赖原工作区未提交 chunks.go。

2. 完成边界与 cleanup 假通过：阻塞已闭合。
失败及 Indexing 写失败的组合测试现在先等内层返回，再等待生产 Rag.Shutdown 完成，之后才检查状态或 QA 调用，覆盖 markFailed、容量释放和 bgWG.Done。CleanupBeforeDone 在受控 markFailed 阻塞期间实际启动 Shutdown，检查不能完成，释放 cleanup 后才等待 Shutdown 成功；删除第二次 Index，原重复 close(asyncDone) 路径不再存在。本轮定点及十轮日志未观察到该 fake panic。

需要准确限定实现者摘要：并非四项测试全都改为 Shutdown；成功 Indexing 测试仍轮询状态并在 cleanup 中仅释放 QA。测试构造仍使用委派到真实 indexAsyncByDocsID 的 fake adapter 和自定义 markFailed，不是完整 New 装配。以上不写成全部覆盖，也不单凭数量验收。共享内核/后台策略已实际接通；本轮另加独立生产适配器验证补足关键断言，见下。

**独立补充验证**

干净目标全部检查结束后，加入审查临时文件 server/core/review_production_chain_test.go；证据副本及 diff 位于本目录。没有修改生产实现。

TestReviewProductionAdapterChunksLifecycle 使用真实 prodAsyncIndexer，调用真实 indexAsyncByDocsID 和知识层 SaveChunksDataWithDeps（共享实际 saveChunks＋indexingOnly 策略），仅底层存储、QA runnable、状态 updater 为 fake。三个场景各十轮 race：

- chunk insert 失败：清理收到 errors.Is 可识别的原始 sentinel，docID=42，清理 context 未取消；真实任务登记完成后 Failed 恰好一次、没有 Active、QA 未执行。
- chunks 成功：通过 QA-entered channel 在 QA 尚阻塞时检查准确 docID 的 Indexing；释放 QA 后等实例 bgWG 完整结束，再检查 Active 一次、无 Failed。
- Indexing 状态写失败：原始 sentinel 保留，真实任务登记完成后 Failed 一次、无 Active、QA 未执行。

未增加 Index 调用时 bgWG 的所有 Add 已在已返回的 Index 中完成，补充测试仅等待同一个生产计数归零；最终 t.Cleanup 仍调用 Shutdown 收尾，所有受控等待均释放。

该层 race 退出 0：10 个顶层测试运行、三个子场景各十次。结果归属“目标＋独立补充测试”，不算目标自带 30 次通过。它不测试真实 DAO/SQL driver、真实模型、默认状态写失败日志捕获；默认 markFailed 报错日志通过生产源码确认保持，不宣称这些真实设施已验证。

**前期其他修复保持闭合**

- Reset 生产实现仍锁内捕获实例、解锁等待捕获对象，失败保留资源/引用，重加锁核实实例身份后才单次清理。本轮未回退，受影响 rag 测试与 race 通过。
- Reset fixture 不再复制已使用 WaitGroup 或手工关闭生产 done；其九项在前轮已有独立十轮 90 次证据，本轮普通及 race 包级回归再次通过，没有重复未变化的 90 次历史压测。
- AfterFunc 注册后 defer stop 未改，正常/error/panic/Goexit 的前轮独立证据保持有效，本轮 core 普通/race 通过。
- shutdown_test.go 仍在目标中，blob=6ae6e2832ea4587bd3e630ac46c2d9954825b016；7 项共享 ShutdownCoordinator 专项测试本轮各十次 race 通过，无原工作区未跟踪测试依赖。
- cmd 共享关闭协调、超时不清理依赖、逆序清理错误聚合及初始化失败清理链未回退。

**干净目标实测**

实际工作目录：C:/Zhy/code/go-rag-review-1d3/server。
环境：Go 1.27.1 windows/amd64，CGO_ENABLED=1、CC=gcc、CXX=g++、GOFLAGS 空、GOTOOLCHAIN=auto，既有 MinGW GCC 8.1.0。设置 GOPROXY=off、GOSUMDB=off，无 online/integration 标签，无真实 DB/ES/Qdrant 或付费模型访问，未调整系统/工具链或 Git 行尾配置。

所有完整命令、目标 SHA、cwd、时间及真实 EXIT 已记录，运行脚本 run-review.ps1。

| 验证 | EXIT | 日志 |
|---|---:|---|
| core/knowledge/rag 定点测试，修复用例＋知识层/Reset/关闭专项，count=1、v、timeout=90s | 0 | target-1.log |
| go test ./core ./internal/logic/rag/... ./internal/logic/knowledge/... ./internal/cmd/... -count=1 -timeout=90s | 0 | target-2.log |
| 同一组受影响包 -race -count=1 -timeout=90s | 0 | target-3.log |
| core/knowledge 关键用例 -race -count=10 -v -timeout=120s | 0 | target-4.log |
| go test -p 1 ./... -count=1 -timeout=90s | 0 | target-5.log |
| go build ./... | 0 | target-6.log |
| go vet ./... | 0 | target-7.log |
| 独立生产适配器三个子场景 -race -count=10 -v -timeout=60s | 0 | production-adapter.log |

干净目标十轮完整正则：
`^(TestIndex_Background(Chunks.*|CleanupBeforeDone)|TestSaveChunks_IndexingOnlyUpdater_SkipsFailed|TestShutdownCoordinator_.*)$`

实际命中 12 项，各 10 次，合计 120 RUN，无 no tests to run；逐名统计见 stress-matches.txt：3 个 BackgroundChunks、CleanupBeforeDone、1 个知识层 indexingOnly 测试、7 个 ShutdownCoordinator。没有将普通测试的包 ok、独立补充子测试或历史日志混入该计数。

本轮未出现 0xc0000139；历史仍只记录“运行异常、race 未完成、根因待确认”，不因此抹除其他包的历史 race 边界。

**实现者交付日志的归属**

目标对象未包含 verification/stage1d3-r4/；这些日志在原工作区为未跟踪目录。它们不能表述为已提交证据。本轮已用干净目标独立重跑并保存完整日志，因此该归档问题不构成新的正确性阻塞，也不要求为一般日志整理再开修复轮次。

本次提交额外纳入 server/verification/stage1d3a 的历史错位日志，不能当作 r4 的新验证；本轮未使用它们支持通过。dao_test.go 额外四行字段对齐无业务意义，不列问题。

**非阻塞建议及明确边界**

- 成功 Indexing 测试可改用 QA-entered channel，并在释放后等待生产任务结束；失败路径可进一步强化 docID/原始错误/状态写失败日志断言。当前关键行为已由共享内核测试、源码和独立生产适配器补证支持，不据此再设阻塞。
- CleanupBeforeDone 可在负向等待前再确认 root 已取消，减少调度偶然性；现有源码和实际十轮生产 Shutdown 阻塞证据支持本轮结论。
- Reset 换代测试仍模拟锁内指针更换，不等于完整真实 InitWith 交接；已识别的无锁读和实例身份问题有源码与历史独立复现修复证据，不扩大结论。
- A-4 缺 QA 向量 kNN；indexer/common/retriever/rerank 历史 race 补证；driver 中断执行中 SQL；Stage 4 持久化恢复；History context/完整 MCP 生命周期仍未验证，不写成通过。

**工作区与结论**

复用现有 linked worktree，确认前次目标为祖先后仅切换审查目录；前轮临时测试已保存在各轮 preserved/，无删除或覆盖。旧 REPORT/HANDOFF 全部保留。新目标先完成干净验证，再添加一份明确标记的独立测试。原工作区未 checkout/reset/clean/stash，未改 remote/config/branch，未 fetch 或确认实时远端推送。

报告后审查树只保留独立未跟踪测试 core/review_production_chain_test.go；生产跟踪文件保持目标内容。未提交、推送、合并或更新项目验收状态。

未发现剩余阻塞。建议按上述明确边界验收 Stage 1D-3，本轮无需再向 Qoder 发阻塞修复提示词；Stage 1 整体验收仍需按既定范围处理遗留验证事项，不能据此宣布全部完成。
