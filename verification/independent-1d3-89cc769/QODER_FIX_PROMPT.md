请在本地 PaperDesk Go 仓库 C:/Zhy/code/go-rag 基于 89cc769440b6b405c2ef16eb78a035f486f03fab 修复本次独立增量复核发现的两项剩余阻塞。先读取适用 AGENTS.md，保护已有修改；不要覆盖或清理审查 worktree C:/Zhy/code/go-rag-review-1d3 的临时测试。完整证据：C:/Zhy/code/go-rag/verification/independent-1d3-89cc769/REPORT.md。

1. Reset 并发与实例身份（P1）
retriever.go:402 在释放 ragMu 后读取全局 ragSvr，与 :417 写入竞争。独立 race 复现已发现 DATA RACE 和 9 次 nil panic；成功等待后没有身份校验，还可在其他 Reset＋Init 换代后关闭新实例。
请在锁内捕获实例及其匹配资源/代次，解锁等待该实例；失败返回 errors.Is 可识别错误并保留依赖和状态；成功重新加锁后核实仍为同一代，才关闭并清空。并发 Reset 不重复关闭旧资源，不触碰新实例。不要恢复持 ragMu 等待，不复制有使用历史的 WaitGroup。保留初始化失败清理及错误聚合。
补离线真实 Reset 测试：并发 Reset 无 race/panic；等待时 GetRagSvr 可调用；超时保留准确对象、状态、资源；任务结束后二次 Reset 成功；等待期间另一调用完成 Reset＋Init 后旧调用不关闭新对象。测试有 channel 协调、有限等待和最终释放，不能留下永不 Done 的登记。

2. Failed 所有权修复过度移除 Indexing（P2）
当前 New 绑定 SaveChunksDataWithoutStatus，nil updater 跳过成功 Indexing，后台持久化后仍为 Pending 直到 Active，违反原状态契约。请只分离 Failed 终结责任，不全局删除状态维护。
公开 SaveChunksData 签名、成功 Indexing/失败 Failed 及原始错误聚合兼容性保留。后台走共享 chunks 内核，chunk 失败仅传播，由后台用独立有限 cleanupCtx 唯一标 Failed；成功持久化后仍写 Indexing，其写失败须传播并停止 QA/embedding；仅全部成功才 Active。保持原空输入/取消等边界，不重复实现 chunks 算法，不吞原始错误。
新增组合测试必须串起真实 Rag.Index 后台、prodAsyncIndexer/indexAsyncByDocsID 和共享 chunks 内核。现有 NoDuplicateFailed 只是 fakeAsyncIndexer 返回错误，单独 helper 测试也不够。断言准确 docID、Failed 恰好一次、保留 chunk 原始错误、状态写失败有报告、不继续后续索引/不误 Active，以及 cleanup 完成前不 Done；另验证成功 chunks 后 QA 尚阻塞时状态为 Indexing。保留公开入口兼容回归。

已闭合项不要回退：AfterFunc defer stop 已被原始复现及正常/error/panic/Goexit 观察测试验证；shutdown_test.go 已提交并有 7 项测试。可以补强断言，但不为命名和一般文档重构。

临时独立测试位置：审查 worktree 的 server/internal/logic/rag/review_reset_test.go、server/core/review_bridge_paths_test.go；副本及 diff 在新证据目录。原始复现原件在该目录 preserved/review_bridge_test.go。只读借鉴，不自动整包复制或误算为提交内测试。

验证时使用准确测试名与包，记录完整命令、实际命中、SHA、cwd、退出码。历史“20 PASS”仅 core 两项各十次，没有 Reset/关闭协调器。先定点与受影响包普通/race，再必要串行全量、build、vet；不用普通压测代替 race，不连接真实设施或付费模型，不修系统。完成后给出新的完整 SHA 和证据清单供增量复核；不要宣布整个 Stage 1 验收完成。
