**1D-3 修复交接补充（2026-10-18）**

原审查及 REPORT.md 继续有效；本轮只读核实目录关系和调用方，未重跑测试、未修业务代码、未改变验收状态。

**目录关系**

审查目录 `C:/Zhy/code/go-rag-review-1d3` 是原仓库的 linked worktree，非独立 clone。

- git worktree list --porcelain 同时列出主工作区 `C:/Zhy/code/go-rag`（refs/heads/review/stage1a）及审查工作区（detached），两者 HEAD 均为 `9e59029133cfdb6b42a449f8dcb625c3708aeaa1`。
- 主工作区 git rev-parse --git-common-dir 返回 `.git`，解析为 `C:/Zhy/code/go-rag/.git`；审查工作区返回 `C:/Zhy/code/go-rag/.git`。
- 审查工作区 git rev-parse --absolute-git-dir 返回 `C:/Zhy/code/go-rag/.git/worktrees/go-rag-review-1d3`；其 .git 文件指向同一路径。
- 两目录共享 origin=https://github.com/HyCheng-reborn/paperdesk-go.git、upstream=https://github.com/wangle201210/go-rag.git（fetch/push URL 相同）。本轮仅读取，没有 fetch 或更改 remote/config/branch。
- 审查工作区当前仅有两个未跟踪测试：server/core/review_bridge_test.go、server/core/shutdown_test.go。保留该目录及全部证据，后续优先复用。

**阻塞 1：Reset 超时仍清理（P1）**

位置：目标 server/internal/logic/rag/retriever.go:393-414，关键 400-412。

生产链：正常退出 cmd.Main → s.Run 返回 → ShutdownCoordinator.Shutdown → Rag.Shutdown；只有该等待成功才调用资源 closer → rag.Reset → 再次 Rag.Shutdown → vectorStore.Close、清空引用。初始化失败链为 RunInitSteps（后续 chat.Init 失败）→ rag.Reset → dao.Reset，初始化失败通常没有已准入任务。危险分支是运行期直接调用公开 Reset：Rag.Shutdown 超时 → 仅 Warning → 仍 Close 和清引用；正常协调器的超时分支不会进入 Reset，不能将两条路径混称。

复现条件：已有任务/失败清理持续超过 Reset 的 5 秒预算。后果：仍在使用的依赖被关闭、全局引用丢失，Close 成功时返回 nil 掩盖未收敛；Qdrant Close 实际关闭客户端。

现有复现：本项只有源码证据，尚无独立动态复现测试。目标 server/internal/logic/rag/retriever_test.go 的 TestReset_ClosesVectorStore、TestReset_VectorStoreCloseError 未设置活跃 ragSvr，不能覆盖此分支；补充 core/shutdown_test.go 只覆盖协调器，也不能证明 Reset 安全。

最小方向：未收敛即返回可识别错误，保留依赖/引用；真正收敛后再清理。全局锁与等待需妥善分离，并防止初始化/Reset 并发发布破坏状态；保留初始化失败的逆序清理。新增调用真实 Reset 的离线协调测试：任务仍阻塞时不 Close、不清引用、不返回 nil，释放后可再次收敛清理。

**阻塞 2：同步 panic 遗留 AfterFunc（P2）**

位置：目标 server/core/indexer.go:119-126。

生产链：HTTP ControllerV1.Indexer → GetRagSvr → Rag.Index → context.AfterFunc(serviceRoot, syncCancel) → prodSyncIndexer.invoke → Eino runnable.Invoke（Loader/Transformer/Indexer）。MCP HandleIndexerByFilePath 也进入 Rag.Index。

复现条件：注册桥接后，同步依赖 panic 并由上层 recover（或其他提前展开）。普通顺序 stopAfterFunc 被跳过；syncCancel defer 不能注销独立注册在服务 root 上的回调，虽 slot/WG 已释放，闭包仍保留到 root 取消。

独立复现：`C:/Zhy/code/go-rag-review-1d3/server/core/review_bridge_test.go`，TestReviewSyncPanicStopsCancellationBridge；备份及 diff 位于证据目录。调用真实 Rag.Index，通过可观测 root 记录注册/注销，同步 fake 仅触发 panic。既有 race 结果退出 1：registered=1、stopped=0、slots=0。

最小方向：注册后立即 defer stopAfterFunc，覆盖全部退出路径；正常路径仍可尽早注销。保留同步请求值/deadline、双路取消及单次 slot/WG 所有权。

**阻塞 3：Failed 有两个写入所有者（P2）**

位置：目标 core/indexer.go:338-341、182-185；core/rag.go:174-181；关联 internal/logic/knowledge/chunks.go:90-134。

生产链：HTTP ControllerV1.Indexer → Rag.Index → 后台 goroutine → prodAsyncIndexer.indexAsyncByDocsID → Rag.indexAsyncByDocsID → saveChunksFn（New 中绑定 knowledge.SaveChunksData）→ saveChunks → chunk DAO 出错 → UpdateDocumentsStatus(Failed) → 返回聚合错误 → 外层后台 defer → 默认 markFailed → 再次 UpdateDocumentsStatus(Failed)。

复现条件：chunk 查询/插入/更新出错，且内层 Failed 状态更新成功。两次写入同一 docID，违反至多一次契约。

调用方核查：在目标 worktree 全仓搜索 Go 源码，SaveChunksData 唯一生产引用是 core.New 中的函数绑定，随后由上述后台路径调用；未找到其他独立生产调用方。不能据此声称“其他调用方依赖它”或“删除公开状态语义没有风险”。公开入口已有明确状态契约，knowledge/chunks_test.go 直接测私有 helper，验证成功 Indexing、失败 Failed、状态写入错误聚合；这些兼容性必须保留。当前无仓内第二生产调用方，不代表可任意改变公开入口或未来/外部复用的语义。

现有复现：本项是组合生产调用链的源码证据，尚无独立端到端动态复现测试。目标 core/indexer_test.go 的 TestIndexAsyncByDocsID_SaveChunksError_Stops 仅让 saveChunksFn 返回 sentinel；knowledge/chunks_test.go 单独证明底层状态处理，二者均没有计数真实组合链中的两次 Failed。

最小方向：保留公开 SaveChunksData(ctx, docID, chunks) 的签名及现有状态/错误契约；提取共享持久化与错误聚合内核，并提供供后台使用的明确“由外层负责失败终结”入口/策略。后台入口不写 Failed，任何错误原样传播，由外层用独立有期限 cleanupCtx 唯一写 Failed。成功所需 Indexing 状态处理应明确保留或迁移且保持原语义，最终 Active 仍仅在全部成功后写入；状态写入错误仍须传播。公开兼容入口继续使用原状态策略。不要全局删除状态处理、吞错误或简单取消失败 cleanup 重试以消除计数。补组合链计数测试和公开入口兼容回归，准确断言 docID、Failed 至多一次及 cleanup 后才 Done。

**证据分层与路径（历史运行结果，本轮未重跑）**

统一证据目录：`C:/Zhy/code/go-rag/verification/independent-1d3-9e59029/`。

| 审查层 | 测试/新增文件 | 日志及证据 | 结果归属 |
|---|---|---|---|
| 干净目标提交 | 目标已提交 core/indexer_test.go、internal/logic/rag/retriever_test.go 及默认已有测试；无临时新增测试 | clean-1.log～clean-7.log；run-clean.ps1；environment-and-scope.txt；test-source-inventory.txt | build/vet/普通测试/指定 race/14 项十轮 race 均退出 0；不含 shutdown_test.go |
| 目标提交＋补充测试 | `C:/Zhy/code/go-rag-review-1d3/server/core/shutdown_test.go`，仅从原工作区复制，5 个 TestShutdownCoordinator_* | supplement.log、supplement-source.txt | race 十轮 50 RUN、退出 0；不能算目标自带测试 |
| 独立复现测试 | `C:/Zhy/code/go-rag-review-1d3/server/core/review_bridge_test.go`；备份 `C:/Zhy/code/go-rag/verification/independent-1d3-9e59029/review_bridge_test.go` | repro.log、repro.diff | AfterFunc 问题复现退出 1；执行时审查树已含补充测试，但 -run 仅选独立复现 |

原补充测试来源 `C:/Zhy/code/go-rag/server/core/shutdown_test.go`，SHA256 `5D660781FF88F4324B20619D3FB76E76C655226A1C3171F87F3A70BE6D89FB55`；原件未改动。现有 REPORT.md 保留。没有将临时测试复制回原仓库或提交。此前报告提及的 clean-status.txt 没有实际落盘文件：当时空管道未生成文件；干净状态由当时 git status 无输出及后续生产文件 git diff --exit-code=0 的工具结果确认，不将缺失文件当证据。

**新 SHA 到达后的增量规则**

1. 固定完整新 SHA，核实对象、祖先关系及现有目录状态。优先复用审查 worktree；先保留临时测试内容/哈希，再处理可能与新提交同名的 shutdown_test.go，不能强制覆盖或清理。原工作区不切换、不清理，不改 remote/config/branch；不删除任一目录。
2. 先审 `git diff 9e59029133cfdb6b42a449f8dcb625c3708aeaa1 NEW_SHA`，结合原始累计范围复核三个问题和相邻回归，不重复无关范围。
3. 重跑 AfterFunc 最小复现、实现者新增的 Reset/状态所有权测试、受影响包测试及必要回归；结果按新目标及临时复现分层标注。
4. 用新提交 Git 对象确认 shutdown_test.go 已提交，新目标的测试运行不依赖原工作区未跟踪文件。若新提交包含同名测试，应以新提交内容建立干净基线，保留旧副本作为历史证据。
5. 报告已修复项、剩余阻塞、测试覆盖和未验证边界。保留 A-4、历史 race、driver SQL 取消、Stage 4 恢复、History context/MCP 生命周期等原边界，不更新项目验收状态或宣布 Stage 1 完成。

交接完成，等待实现者提供新的完整 SHA。
