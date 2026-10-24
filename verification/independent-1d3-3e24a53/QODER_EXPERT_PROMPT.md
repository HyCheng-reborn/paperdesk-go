请用【专家模式】处理 PaperDesk Go 在目标 3e24a533f99aaa8578b848ae550dfd2e10292723 的两项剩余阻塞。只做必要依赖补全和正确测试，不重写上一轮已验证正确的生产 Reset/AfterFunc/Indexing 语义。先读 AGENTS.md 并保护现有工作区。报告：C:/Zhy/code/go-rag/verification/independent-1d3-3e24a53/REPORT.md。

P1：干净提交 core 测试编译失败，core/indexer_test.go:2284/2285/2293 引用 knowledge.ChunkStore、DocStatusUpdater、SaveChunksDataWithDeps，但定义只在原工作区未提交 chunks.go。请核实归属并完整交付，不能再用带未提交改动的工作区 PASS 当提交证据。当前未提交 helper 直接 saveChunks(...,updater)，未套 indexingOnlyUpdater，不能盲目补提交：这会在内核失败时写 Failed、外层再写 Failed。应共享真实后台策略边界（Indexing 写、Failed 跳过）和 chunks 内核，保持公开 SaveChunksData 兼容行为。

P2：asyncDone 在内层 asyncIndexer wrapper defer 关闭，仍早于外层 markFailed 和 bgWG.Done，不是真实后台完成证明。请以生产 Shutdown/WG 的完成作为计数边界，调用真实 prodAsyncIndexer 及共享后台 chunks 策略，fake 仅在最低层存储/模型/状态边界。覆盖准确 docID、errors.Is 原始 chunk 错误、Failed 恰好一次、成功 Indexing、状态写失败停止 QA/Active、默认失败状态写错误的报告。成功任务也需释放并等待真正完成。

CleanupBeforeDone 需要在受控 cleanup 阻塞期间实际启动 Shutdown，确认服务已进入关闭等待但 Shutdown 不能返回；释放 cleanup 后才等待 Shutdown 成功。仅检查自己 cleanupDone 未关闭是同义反复。不要复用同一个 asyncDone 又发第二次 Index：defer close 会再次关闭同一 channel，恢复 markFailed 不会解决 asyncIndexer 重复 close。删除无关第二任务，或用独立信号并收齐全部任务。容量 8 时第二次准入不证明上一 slot 释放。注入 asyncWait 为空不是问题，不必保留一秒延迟；不要把“真实等待”等同于真实生产完成。

Reset fixture 的核心复制 WG/手工 done 问题已修复，本轮九项普通/race和十轮90次通过。只在必要时改善失败 cleanup，不再回退。换代测试当前只是锁内模拟指针换代，不能声称真实 Init 已测；InitWith 支持 fake 工厂，可在不接真实设施的前提下补测。不要引入永不释放等待。

完成后先验证新目标能独立编译，再受影响测试普通/race、准确关键名十轮、必要全量/build/vet。明确包含 CleanupBeforeDone，不只测三个 BackgroundChunks。记录真实退出码与 panic/error 日志。不要连接真实设施/付费模型、升级工具链或更改行尾配置，不清理审查 worktree，不复制旧临时测试算作提交自带测试。

向用户输出需交付的准确文件清单、验证结果和注意事项。后续整理文档/日志及提交可交给 qwen3.8flash；如果平台不能直接切模式，请停止在完成的实现/验证交接处，由用户切换。不要自行建立新 Codex 聊天。不要宣布 Stage 1 验收完成。
