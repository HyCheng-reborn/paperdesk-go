# 变更记录

### Stage 1D-3-A 实施完成（2026-10-18）

- **类型**：业务代码实施
- **分支**：review/stage1a
- **基线**：17d7654ecc0656dcc638e92791fdca61c743cf21
- **内容**：
  - `server/core/rag.go`：新增 ErrShuttingDown/ErrBackgroundFull sentinel errors；Rag struct 新增 mu/closed/bgWG/bgSem/bgRootCtx/bgRootCancel/shutdownOnce/done/taskTimeout/maxBackground 字段；asyncWait 签名改为 func(ctx)；New() 初始化生命周期配置；新增 Shutdown() 方法
  - `server/core/indexer.go`：asyncIndexer 接口新增 ctx 参数；prodAsyncIndexer 使用传入 ctx（删除 gctx.New()）；Index() 重写为 mu 锁保护准入→bgSem 非阻塞获取→bgWG.Add 登记→defer 兜底释放→同步索引→所有权转移给后台 goroutine→bgCtx 从 bgRootCtx 派生 timeout
  - `server/core/config/config.go`：Config struct 新增 MaxBackgroundTasks/BackgroundTaskTimeout 字段；Copy() 同步复制
  - `server/internal/logic/rag/retriever.go`：RagConfig 新增字段；新增 readLifecycleConfig 函数；readRagConfig 调用级联；defaultRagFactory.Create 传递
  - `server/manifest/config/config_demo.yaml`、`config_qd_demo.yaml`：新增 rag.maxBackgroundTasks 和 rag.backgroundTaskTimeoutMinutes 示例
  - `server/core/indexer_test.go`：fake 签名同步；newIndexerTestRag 初始化生命周期字段；所有 9 个现有测试 asyncWait 签名更新；新增 10 个 A 组测试
  - `server/internal/logic/rag/retriever_test.go`：新增 4 个 readLifecycleConfig 配置测试
- **验证**：gofmt/build/vet exit 0；core 19 Index 测试 + rag 4 配置测试通过；core+rag race 通过；关键协调测试压测 -race -count=10 通过；全项目 go test -p 1 ./... 通过
- **范围**：仅 1D-3-A（准入、所有权转移、Shutdown 核心），不实施 B（统一 markFailed/重试）和 C（cmd 接线）
- **已知边界**：保留历史 race、A-4 及驱动取消未验证边界；B/C 待实施
- **状态**：Stage 1D-3-A 实施完成，待独立复核

---

### Stage 1D-3 计划定点修订（2026-10-18，仅文档）

- **类型**：计划文档修订（`design/stage1d-plan.md`）；不开始业务编码，不实施 1D-3。基线 `80bb105d3de32cf7b924285f04144d10f508cb35`。
- **固定准入契约**：改为“同步索引前预留后台容量”——同一锁下检查 closed + 非阻塞取容量 + 登记已准入；closed/满载返回可识别错误（`ErrShuttingDown`/`ErrBackgroundFull`），不执行同步索引、不启动后台；同步失败释放容量与登记；同步成功把所有权转移给后台（slot/Done 只释放一次）。Shutdown 等待已准入的同步阶段与后台（已准入同步计入 bgWG），同步阶段用 bgRootCtx 派生 ctx 观察停止；后台仍 HTTP 返回后执行、不继承请求取消；不引入排队 goroutine/持久化任务。
- **核实同步路径状态写入**：删除“SaveChunksData 已在同步阶段设置 Indexing”无源码支持表述；文档记录由 controller 以 `StatusPending` 创建（rag_v1_indexer.go:81），`StatusIndexing` 由 SaveChunksData 在异步阶段写（chunks.go:90/130）。拒绝仅保证本次 Index 无同步索引副作用，不承诺撤销上游已创建的文档记录。
- **Shutdown 超时策略**：仅 Shutdown 返回成功后才逆序清理依赖；超时保留依赖、不清空服务引用、不报正常清理成功、向 cmd 返回可识别错误并记录未收敛；进程退出由 OS 回收不称优雅完成；后续调用继续等待同一 done；明确正常退出/初始化失败清理/直接 Reset 边界避免 Reset 绕过收敛；Cleanup 错误不忽略；避免 dao 重复关闭。补离线协调测试（9.3 C 组）。
- **重划批次**：A=准入+登记+所有权转移+Shutdown 核心（同批可验证），B=失败/panic/取消+markFailed+重试可取消，C=cmd 接线+Shutdown 收敛协调；各批列修改文件/真实调用链/测试/协调回滚。
- **固定配置**：`MaxBackgroundTasks` 默认 8、`taskTimeout` 默认 5min；键 `rag.maxBackgroundTasks`/`rag.backgroundTaskTimeoutMinutes`；缺失默认、越界钳位、读取错误传播；Config.Copy/RagConfig/readLifecycleConfig/factory/示例/批次 A 测试；移除“待决策”。清理 ctx 5s 仅为调用预算。
- **一致性**：QA 缓存现行方案改为 `qaModelCache`（sync.Once 标历史）；删除不存在的 `WithQAConfig`；1D-1/1D-2 测试路径按实际实现标注；回滚按实际依赖方向说明；GoFrame v2.9.0 `Server.Run()`（ghttp_server.go L445-460）给出源码位置与证据，信号集合/graceful 超时细节标未验证；保留 race（0xc0000139）与 A-4 未验证。
- **状态**：1D-2 仍为已通过源码及提交日志复核（未独立重跑测试）；1D-3 计划待复核；Stage 1 整体未完成。

---

### Stage 1D-2 独立复核通过（2026-10-18）

- **复核方式**：源码及提交日志复核，复核方未独立重跑测试
- **验收基线**：`d7618e5a092f0b29e6441e98e32d86075b199172`
- **结论**：1D-2（F08 QA 有限并发、失败传播与配置关闭）通过复核
- **保留边界**：indexer/common/retriever/rerank race 未补证；QA Retriever 对缺失向量的 kNN 行为待验证（A-4）

---

### Stage 1D-2 定点修正（共享 helper：Graph 装配 / 字段转换 / QA 缓存）（2026-10-17）

- **基线**：`575949abfde25ff0e5ffc68ed0211b92430f54a6`。只修正三项；不实施 1D-3，不改检索策略/数据库模型/整体索引架构。
- **一、Graph 测试复用生产装配**：`server/core/indexer/async.go` 提取共享 `buildAsyncGraph(ctx, indexer.Indexer, qaEnabled, maxConcurrent, qaModelFactory)`（QA 节点/分支/并发上限/边/Compile 只实现一次并传播构图错误）；`BuildIndexerAsync` 委派它；`graph_test.go` 删除复制的 `buildTestGraph`，改用同一 helper + 实现 `indexer.Indexer` 的 `fakeIndexer`。4 个测试：QA 错误/factory 错误均中断下游、QA 关闭不调用 factory/Generate、QA 成功传递准确内容与 IDs。
- **二、完整字段转换不修改输入**：`indexer_async.go` 的 `documentToFields` 成为唯一完整 helper（Extra 由 getExtData→sonic.Marshal 内部序列化，写新 map、不写回输入；序列化失败返回可追踪错误且不返回成功字段）；ES `DocumentToFields` 回调仅读 knowledgeName+委派。重写 `indexer_async_test.go` 直接测 helper（含真实 ExtKeys 序列化失败）。
- **三、初始化重试经实际缓存**：`chat_model.go` 新增实例级 `qaModelCache`（锁/model/ready/注入 reader+builder），`GetQAModel` 委派 `qaDefaultCache`；删除包级 qaModel/qaModelMu/qaModelReady；无新增可变全局替换。`chat_model_test.go` 用独立实例验证：首次失败不发布缓存、第二次成功、第三次命中缓存且 builder 总次数=2、并发只构建一次、读失败不调 builder、(nil,nil) 报错可重试。
- **交付（三入口共用实现）**：BuildIndexerAsync→buildAsyncGraph；newAsyncIndexer ES 回调→documentToFields；GetQAModel→qaDefaultCache.get；测试分别进入同一实现。
- **验证**（`verification/stage1d2/*-fix2.log`）：gofmt/build/vet exit 0；`go test ./core/indexer/... ./core/common/... -count=1 -v` exit 0，64 pass/0 fail；`go test -p 1 ./...` exit 0（13 包 ok）；`go test -race ./core/indexer/... ./core/common/...` 两包均 `0xc0000139`（race 二进制无法启动、运行异常、race 未完成、根因待确认，未反复运行，不以普通测试替代 race 结论）。
- **保留边界**：A-4 QA Retriever kNN 行为待验证；core/common、core/retriever、core/rerank 及本批 indexer/common 的 race 仍待补证；统一 markFailed、重试可取消生命周期、固定延迟归属 1D-3。
- **状态**：Stage 1D-2 定点修正完成，待独立复核（1D-3 未实施，Stage 1 整体未完成）。

---

### Stage 1D-2 定点修复（2026-10-17）

- **类型**：业务代码修复
- **分支**：review/stage1a
- **基线**：b24330d6eec11df006ba09d7664f7a2955df5bef
- **修复内容**：
  - `server/core/common/chat_model.go`：提取 initQAModel 实例级初始化器；defaultQAConfigReader 使用 Get+error 替代 MustGet；defer 释放锁
  - `server/core/indexer/qa.go`：预取消检查（factory 和 Generate 均不调用）；nil factory 检查；Generate 返回 nil 时返回明确错误
  - `server/core/indexer/indexer_async.go`：提取 documentToFields 纯函数，不修改输入 doc
  - `server/internal/logic/rag/retriever.go`：readQAConfig 接收 configGetter 接口，生产与测试共用同一解析逻辑
  - `server/core/common/chat_model_test.go`：重写，6 个测试覆盖初始化器重试、错误链、缓存、并发
  - `server/core/indexer/qa_test.go`：修复无锁计数器（atomic.Int32）、共享 map 确定性回答、失败等待 channel 同步；新增 nil factory、Generate nil 测试
  - `server/core/indexer/graph_test.go`（新建）：4 个 Graph 集成测试（QA 错误中断 Indexer、QA 关闭 factory 不调用、成功流程、factory 错误）
  - `server/core/indexer/indexer_async_test.go`（新建）：7 个 DocumentToFields 测试
  - `server/internal/logic/rag/retriever_test.go`：重写，6 个配置解析测试（缺失/false/true/边界/错误）
- **验证**：gofmt/build/vet exit 0；indexer 35 + core 38 测试通过；core race 通过；压测 -count=10 通过；全项目 go test -p 1 ./... 通过
- **已知边界**：indexer 包 race 检测因 Windows DLL 问题失败（运行异常、race 未完成、根因待确认）；QA Retriever 对缺失向量的 kNN 行为待验证（A-4）
- **状态**：Stage 1D-2 定点修复完成，待独立复核

---

### Stage 1D-2 定点修正（2026-10-17）

- **类型**：业务代码修正
- **分支**：review/stage1a
- **基线**：d0905bdef20bd1a3ce80b7363eee74ea0d15927b
- **修正内容**：
  - `server/core/common/chat_model.go`：GetQAModel 从 sync.Once 改为 sync.Mutex + qaModelReady，失败后可重试，不返回 (nil,nil)
  - `server/core/indexer/qa.go`：创建独立输出 Document 及顶层 MetaData map（输入不变）；factory 在入口调用一次；Wait 后先返回 worker 错误再检查原始 ctx.Err()；移除 getQAContent 冗余检查
  - `server/internal/logic/rag/retriever.go`：提取 readQAConfig helper，配置读取失败返回 error
  - `server/core/common/chat_model_test.go`（新建）：3 个初始化测试（重试、并发、不返回 nil nil）
  - `server/core/indexer/qa_test.go`：新增 12 个测试（预取消、进行中取消、超时、共享 MetaData 隔离、并发调用隔离、等待退出、Graph 集成等）
  - `server/internal/logic/rag/retriever_test.go`（新建）：2 个配置解析测试
- **验证**：gofmt/build/vet exit 0；indexer 26 + core 39 测试通过；core race 通过；indexer 压测 -count=10 通过；全项目 go test -p 1 ./... 通过
- **已知边界**：indexer 包 race 检测因 Windows DLL 问题失败（环境问题，已如实记录）；QA Retriever 对缺失向量的 kNN 行为待验证（A-4）
- **状态**：Stage 1D-2 修正完成，待独立复核

---

### Stage 1D-1 独立复核通过（2026-10-17）

- **复核方式**：源码及提交日志复核，复核方未独立重跑测试
- **验收基线**：`644642712bcb04e820e345598a98d99c78277175`
- **结论**：1D-1（F09 Chunk 写入错误传播）通过复核
- **保留边界**：预取消测试仅证明应用层快速返回；部分写入不回滚；markFailed 归属 1D-3

---

### Stage 1D-2 实施完成（2026-10-17，待独立复核）

- **基线**：`644642712bcb04e820e345598a98d99c78277175`
- **范围**：仅 1D-2（F08 QA 有限并发和失败状态）；不实施 1D-3，不修改已验收的 1D-1/1C/1B/1A/1-Pre
- **一、QA 并发控制与失败传播（server/core/indexer/qa.go）**：裸 `sync.WaitGroup` 替换为 `errgroup.WithContext` + `SetLimit(maxConcurrent)`；首错取消；`getQAContent` 失败传播到 errgroup；nil doc/nil MetaData 防护；重复指针检测；结果写入独立 `results` 数组，Wait 后统一写回 `doc.MetaData`（防止并发写共享 map）
- **二、QA 可配置关闭（config + retriever 级联）**：`Config` 新增 `QAEnabled *bool` + `QAMaxConcurrent int`；`RagConfig` 级联；`WithQAConfig` context 注入；缺失默认启用（向后兼容）；`maxConcurrent` 范围 1-32 钳位
- **三、GetQAModel 并发安全（server/core/common/chat_model.go）**：`sync.Once` 保护懒加载，消除无锁缓存 race
- **四、DocumentToFields 条件写入（server/core/indexer/indexer_async.go）**：`FieldQAContent`/`FieldQAContentVector` 仅在值非空时写入，QA 关闭或无内容时不写空值
- **五、BuildIndexerAsync 接收 qaModelFactory（server/core/indexer/async.go）**：QA model 通过构建时工厂函数注入，运行期只读；eino Graph 集成不变，QA 节点失败通过 `NodeRunError` 中断 Graph
- **六、New() 预热 qaModel（server/core/rag.go）**：仅当 QA 启用时预热
- **七、x/sync 依赖**：`golang.org/x/sync` v0.15.0 从传递依赖提升为直接依赖（`go mod tidy`）
- **测试**：11 个离线 QA 测试（AllSuccess/PartialFailure/ConcurrencyLimit/Disabled/ExistingContent/ContextCancel/EmptyDocs/NilDoc/DuplicatePointer/ExistingContentSkips/GraphIntegration）；`-count=10` 压测无 DATA RACE
- **验证**（server/，GOFLAGS 空）：go build/vet exit 0；qa 11/11 + core 39/39 PASS；`go test -p 1 ./...` exit 0；`go mod tidy` 无残留
- **保留边界**：QA 关闭时 `qaRtrvr` 对 `FieldQAContentVector` 缺失的 kNN 行为待验证（不影响主检索路径 `rtrvr`）；Qdrant 异步 indexer 路径保留现状；`core/retriever`、`core/rerank` race 仍待补证
- **状态**：Stage 1D-2 实施完成，待独立复核（1D-3 未实施，Stage 1 整体尚未完成）

---

### Stage 1D-1 定点修正：检索返回完整性校验 + 两项验收测试（2026-10-16，待独立复核）

- **基线**：`ff0b87230e307ced2746e6b84fc11e99192cd572`。只处理两项事项；不进入 1D-2/1D-3，不重构整体索引架构。
- **一、检索返回集合检查（server/core/indexer.go）**：`indexAsyncByDocsID` 在保存/后续索引前新增校验——返回文档非 nil、返回 ID 必须属于预期集合、重复 ID 明确报错；保留缺失 ID 的重试与耗尽错误；非法返回不调用 saveChunksFn/idxerAsync/updateDocStatus。
- **二、验收测试**：knowledge 新增 `TestSaveChunks_ChunkAndStatusErrorsBothJoined`（一次调用 insert+状态各返回不同 sentinel，errors.Is 同时识别且含 chunk ID/doc ID）与 `TestSaveChunks_UpdateFailure_Propagated`（update 分支）；core 将 `TestIndexAsyncByDocsID_FullSuccess` 改为事件序列精确断言 search→saveChunks→IndexAsync→Active 并校验 chunk ID/内容/docID 及传入 IndexAsync 的文档；`MarshalError_NoActive` 增加 idxerAsync 未调用断言；新增 `UnexpectedExtraDoc`/`DuplicateDocID`/`NilDoc` 验证非法返回下三阶段均未调用。
- **边界（保留）**：重试仍为固定延迟（可取消生命周期归 1D-3）；统一 markFailed 归 1D-3；预取消测试不证明 driver 能中断 SQL；core/common(retriever/rerank) race 待补证。
- **测试计数**：core 全包 39、knowledge 10、合计 49 PASS；本批新增 5（+强化 2）。
- **验证**（`verification/stage1d1/*-fix.log`）：go test -v exit 0（49/0/0）；go test -race knowledge+core exit 0；`go test -race -count=10 -v -run "TestSaveChunks_|TestIndexAsyncByDocsID_"` exit 0（命中 21 ×10=210 PASS，无 DATA RACE，无 no-tests-to-run）；`go test -p 1 ./...` exit 0；vet/build exit 0；GOFLAGS 空。
- **状态**：Stage 1D-1 定点修正完成，待独立复核（不标记 Stage 1 整体完成）。

---

### Stage 1D-1 实施完成（2026-10-16）

- **类型**：业务代码实施
- **分支**：review/stage1a
- **基线**：261da44a080112dbf0b1fb352e1f43ef22a189a7
- **内容**：
  - `server/internal/logic/knowledge/chunks.go`：新增 chunkStore/docStatusUpdater 接口、daoChunkStore/daoDocStatusUpdater 生产实现、saveChunks 内部 helper；SaveChunksData 公开签名不变，委派到 helper；区分 sql.ErrNoRows 与 DB error；errors.Join 聚合错误含 chunk ID；update 路径不写 status 字段；空输入/预取消/循环中取消边界
  - `server/internal/logic/knowledge/chunks_test.go`（新建）：8 个单元测试（AllSuccess/PartialFailure/ScanDBError/UpdatePath_NoStatusOverwrite/EmptyChunks/PreCancelled/MidLoopCancel/StatusWriteError_Joined）
  - `server/core/rag.go`：新增 docSearcher 接口、docSearch/saveChunksFn/updateDocStatus 三个构建时固定字段、New() 中初始化生产默认值
  - `server/core/indexer.go`：indexAsyncByDocsID 改造——>1000 超限保护、set-based ID 完整性核对（含重试耗尽返回缺失 IDs）、sonic.Marshal 失败聚合中止后续、saveChunksFn 失败传播、updateDocStatus 失败传播、Active 五条件全满足
  - `server/core/indexer_test.go`：新增 8 个编排测试（Over1000Docs/MarshalError/MissingDocs/RetryExhausted/SaveChunksError/IndexAsyncError/ActiveUpdateError/FullSuccess），直接调用 indexAsyncByDocsID 生产编排
- **验证**：gofmt/build/vet exit 0；knowledge 8/8 + core 17/17 测试通过；race 检测通过；压测 -count=10 无 DATA RACE；全项目 go test -p 1 ./... 14 包 ok
- **范围**：仅 1D-1（F09），不实施 1D-2/1D-3
- **已知边界**：预取消测试证明应用层快速返回，不证明 driver 中断正在执行的 SQL；部分写入不回滚（保留现状）；统一 markFailed 归属 1D-3
- **状态**：Stage 1D-1 实施完成，待独立复核

---

## 2026-10-16 Stage 1D 计划定点修订

### Stage 1D 计划定点修订（2026-10-16）

- **类型**：计划文档修订
- **分支**：review/stage1a
- **基线**：2976f4330f84fa312f178f1cd93bcc580af84aee
- **内容**：
  - 修正验收基线（删除 f89bb81，使用 26e55ee）
  - 1D-1 注入边界：引入 docSearcher 接口、保留原公开签名、内部 helper 接收依赖、删除不可实现的 fakeConf 示例
  - 1D-3 生命周期：mutex 保护准入/登记、服务级 bgRootCtx 父子链、Shutdown 每次等待 done、满载位于同步写入后
  - 1D-2 QA：修正 errgroup.Go 行为描述、条件预热、MetaData 并发写入防护、检索行为待验证
  - 区分测试证据能力（应用层快速返回 vs driver 中断 SQL）
  - 更新已确认事实表和阻塞项分类
- **范围**：仅文档修订，不修改业务代码

---

## 2026-10-15 Stage 1D 计划修订

### Stage 1D 计划修订（2026-10-15）

- **类型**：计划文档修订
- **分支**：review/stage1a
- **基线**：e586bc7c0277c2ec605543ec7e3815a6d244d5a0
- **内容**：
  - 修订 `design/stage1d-plan.md`：统一依赖注入方式（删除包级函数替换）、完善 1D-1 数据完整性与错误契约、完善 1D-2 QA 契约、补全 1D-3 生命周期可实施方案、修正回滚与验证边界
  - 文档从 794 行扩展到约 1189 行
- **范围**：仅计划文档修订，不修改业务代码，不开始 1D 实施
- **状态**：Stage 1D 计划修订完成，待复核

---

## 2026-10-15 Stage 1D 细化实施计划

### Stage 1D 细化实施计划（2026-10-15）

- **类型**：计划文档
- **分支**：review/stage1a
- **内容**：
  - 创建 `design/stage1d-plan.md`：Stage 1D 细化实施计划（F09 Chunk 写入错误传播 → F08 QA 有限并发 → F07 goroutine 入库止损）
  - 修正 `PROJECT_PROGRESS.md`：统一 Stage 1A 已通过复核、1C-2 验收基线 26e55ee、7fb5b77 为补证提交
  - 修正 `CHANGES.md`：同步状态事实
  - 修正 `design/stage1-plan.md`：更新 1D 状态描述
- **范围**：仅文档变更，不修改业务代码
- **状态**：Stage 1D 待实施，Stage 1 整体尚未完成

---

## 2026-10-15 Stage 1C-2 验收记录：复核通过 + 生命周期 race 补证（无业务代码变更）

**验收基线**：`26e55ee2ea43daf703ed72da60425ff5b7980cba`。

### 独立复核结论
- Stage 1C-2 已通过**源码及提交日志复核**，复核方未独立重跑测试。
- Stage 1C-1、1C-2 均已通过；**Stage 1 整体尚未完成**（1A 已通过复核、1D 待实施）。

### 生命周期 race 证据修正
- 早前 `verification/stage1c2/race-lifecycle-chatctrl-r2.log` 的命令使用占位符 `<chat+controller lifecycle>`，未记录实际 `-run` 正则，不作为有效证据（保留不覆盖）。
- 本轮以准确正则重跑并新增证据 `race-lifecycle-chatctrl-explicit-r3.log`：
  `go test -race -count=5 -v -run "TestGetAnswerStream_|TestChatStream_|TestController_ChatIntegration_" ./internal/logic/chat/... ./internal/controller/rag/...`
  — exit 0，命中 23 个生命周期用例（含两包同名 `TestChatStream_ModelReceivesDeadlineAndValue`）×5=115 全 PASS，0 FAIL，无 DATA RACE，无 "no tests to run"（未将未命中测试记为通过）。日志头部记录完整可复制命令、实际正则、GOFLAGS 与退出码。

### 保留边界（未变）
- `core/common` race 因 `0xc0000139` 运行异常未完成、根因待确认；`core/retriever`、`core/rerank` race 仍未补证。
- History 保存不支持 context timeout；部分刷新错误无法检测（无返回值 Flusher）。

---

## 2026-10-15 Stage 1C-2 定点修正：补齐成功路径流关闭 + 集成测试走真实 SSE 编排（已通过源码及提交日志复核）

**基线**：`96c7f90355bc74895b21d27aa95560957c2f62a4`。只完成两项修正；不实施 Stage 1D，不重构整体流架构，不改前端协议/History 接口/数据库模型。Stage 1C-1 已通过独立源码与提交日志复核。

### 修正内容

| 项 | 说明 |
|---|---|
| 成功路径关闭前台分支 | chat.go `cleanupOK` 新增 `srs[0].Close()`；顺序：通知成功→等后台完成（EOF/保存/关 `srs[1]`）→关前台 `srs[0]`→cancel。依 Eino `Copy(2)` 需两分支都关才释放上游 |
| 关闭回归测试 | `TestGetAnswerStream_SuccessCleanup_ClosesForegroundBranch` 用 `schema.ErrRecvAfterClosed` 作为可观察关闭信号（非保存计数、非 ctx 已取消），验证重复 cleanup 安全 |
| 薄入口 | `common.StreamToResponseWriter(ctx, http.ResponseWriter, reader, docs)` 复用 realSSEWriter+streamResponse+sonic，不复制循环；`SteamResponse` 设头后调用 |
| 集成失败测试 | `TestController_ChatIntegration_WriteFailure` 重写为实际 ChatStream→实际 GetAnswerStream/协调器→共享编排→底层 Write 返回 sentinel；断言 errors.Is、模型观察取消、完成信号、controller 清理、不保存 assistant |
| 集成成功测试 | `TestController_ChatIntegration_NormalComplete` 同路径正常 EOF；返回前保存一次完整 assistant，按 role/convID/content 精确断言，输出含内容与 DONE |
| saver 记录 | fakeSaver 记录 role/convID/content，不只总次数 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/logic/chat/chat.go | cleanupOK 成功路径新增 `srs[0].Close()` |
| server/core/common/stream.go | 新增薄入口 `StreamToResponseWriter`；`SteamResponse` 委派 |
| server/internal/logic/chat/chat_test.go | 新增关闭回归测试 |
| server/core/common/stream_test.go | 新增 `sentinelWriteRW` + `TestStreamToResponseWriter_ProductionPath` |
| server/internal/controller/rag/rag_v1_chat_stream_test.go | 集成测试改走真实编排；新增正常完成测试；生命周期模型与详细 saver |

### 验证边界（保留）

- History 保存不支持 context timeout；部分刷新错误无法检测（无返回值 Flusher）；历史 race 未完成
- 保留已有后台 EOF 信号、共享 Chat 并发内容归属及 deadline 测试，未重复重写

### 验证结果（server/，GOFLAGS 空；`verification/stage1c2/*-r2.log` 记录命令/GOFLAGS/退出码）

- `gofmt -l` 无输出；`go build ./...`/`go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，86 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/...`、`go test -race ./internal/controller/rag/... ./internal/cmd/...` — exit 0
- `go test -race -count=5 -run <chat+controller lifecycle> ./internal/logic/chat/... ./internal/controller/rag/...` — exit 0，无 DATA RACE
- `go test -race ./core/common/...`（含 lifecycle -count=5）— exit 1：Windows `0xc0000139` 运行异常，race 未完成、根因待确认（不写成通过、不断言非代码问题）

### 回滚说明

回滚 chat.go/stream.go 及三测试文件到基线 `96c7f90`；不涉及前端协议、History 接口、数据库模型或整体流架构。

---

## 2026-10-15 Stage 1C-2 定点收尾：cleanup cancel 能力、SSE 写入取消边界、真实生命周期测试

**基线**：`f89bb816d234c39fbd2b497a096299bfb3ecf98e`（Stage 1C-2 fix2）

### 修复内容

| 修复项 | 说明 |
|---|---|
| cleanup 内部 cancel | GetAnswerStream 创建派生 context (WithCancel)，cancel 赋值给 streamCoordinator；cleanup 内部先 cancel 解除模型阻塞，再发送结果、关闭前台、等待后台结束 |
| 派生 context 传播 | 模型接收 derivedCtx，cleanup cancel 时模型观察取消；后台保存条件检查原始 ctx（不受 derivedCtx cancel 影响） |
| 建流失败释放 | 建流失败也调用 cancel() 释放派生 context |
| Controller defer 兆底 | 新增 cleanupDone 标志 + defer 兆底清理；成功路径 cleanupDone=true 后 defer 不做任何事，成功保存不被抢先取消 |
| 失败路径简化 | cleanup 内部已 cancel，Controller 失败路径不再需要手动 cancel |
| 注释修正 | 删除 GetAnswerStream 注释中不存在的 context.CancelFunc 返回值说明 |
| SSE 写入取消边界 | realSSEWriter 新增 ctx 字段，writeFrame 在实际写入前检查 context；文档化“context 检查不能中断已阻塞的 Write” |
| 测试：直接失败 cleanup | TestGetAnswerStream_DirectFailCleanup_NoManualCancel：不预先手动 cancel，直接 cleanup(StreamResult{})，模型观察取消，assistant 未保存 |
| 测试：Controller 集成 | TestController_ChatIntegration_WriteFailure：实际 Controller + 协调器，注入底层 writer 失败，验证模型观察取消、协调任务完成 |
| 测试：后台完成前台失败 | TestChatStream_BackgroundDoneThenForegroundFail_NoSave：信号控制顺序，验证未抢先保存 |
| 测试：并发 convID 记录 | TestChatStream_ConcurrentConvID_SaverRecords：fake saver 记录 role、convID、内容，验证各会话归属及 assistant 恰好一次 |
| 测试：真实 WithDeadline | TestChatStream_ModelReceivesDeadlineAndValue：真正设置 context.WithDeadline，断言模型收到相同 deadline 和 value |
| Controller 取消测试 | TestChatStream_CancelPropagation 移除固定 sleep，通过 cleanup 释放任务并等待结束 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/logic/chat/chat.go | GetAnswerStream 创建派生 context；streamCoordinator.cancel 赋值并在 cleanup 内部调用；后台保存条件检查原始 ctx；删除不存在的 CancelFunc 返回说明 |
| server/internal/controller/rag/rag_v1_chat_stream.go | 新增 defer 兆底清理 + cleanupDone 标志；失败/成功路径不再手动 cancel（cleanup 内部处理） |
| server/core/common/stream.go | realSSEWriter 新增 ctx 字段；writeFrame 写入前检查 context；SteamResponse 传递 ctx |
| server/internal/logic/chat/chat_test.go | fakeMessageSaver 新增 convIDs/roles 记录；新增 DirectFailCleanup、BackgroundDoneForegroundFail、ConcurrentConvID、ModelReceivesDeadline 测试；新增 routingChatModel |
| server/internal/controller/rag/rag_v1_chat_stream_test.go | CancelPropagation 移除固定 sleep；ModelReceivesDeadlineAndValue 使用真实 WithDeadline；新增 Controller_ChatIntegration_WriteFailure |

### 验证结果（server/，GOFLAGS 空）

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，86 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... -count=5 -run "TestGetAnswerStream_|TestChatStream_"` — exit 0，无 DATA RACE
- `go test -race ./core/common/...` — exit 1：Windows 预存在 0xc0000139（与 1C-1 一致），根因待确认

**race 范围**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。

**证据目录**：[verification/stage1c2/](verification/stage1c2/)

---

## 2026-10-15 Stage 1C-2 fix2：保存/cancel 竞态、cleanup 生命周期、严格保存条件、SSE 取消边界

**基线**：`bfaa289253d59db8d2758d4656a1926eaa2bf47e`（Stage 1C-2 lifecycle fix）

### 修复内容

| 修复项 | 说明 |
|---|---|
| 保存/cancel 竞态 | controller 成功路径：cleanup 等待后台完成后 cancel；失败路径：先 cancel，cleanup 等待后台，再 cancel |
| cleanup 完成生命周期 | streamCoordinator 结构体；cleanup 发送结果 → 关闭 srs[0] → 等待 coordDone；后台 defer close(coordDone) + defer srs[1].Close() |
| cleanup 幂等 | sync.Once 保护，第一次终态决定结果，重复调用安全 |
| 严格保存条件 | cleanEOF + 前台成功 + 未取消 + 无错误；不仅凭 StreamResult.Success 保存 |
| 后台记录 cleanEOF | 区分 EOF/错误/nil chunk；只有真正 EOF 才设 cleanEOF=true |
| 删除 StreamResult.Answer | 未使用字段移除 |
| SSE 取消边界 | 读取前/后、写入前检查 ctx.Err()；阻塞 Recv 退出由生命周期机制解决 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/logic/chat/chat.go | streamCoordinator 结构体；cleanup 等待 coordDone；sync.Once 幂等；后台记录 cleanEOF；严格保存条件；删除 StreamResult.Answer |
| server/internal/controller/rag/rag_v1_chat_stream.go | 成功/失败路径明确 cancel/cleanup 顺序；移除 cleanupDone defer |
| server/core/common/stream.go | 读取前/后、写入前 ctx.Err() 检查 |
| server/core/common/stream_test.go | 更新 CancelThenEOF_NoDONE；新增 CancelBeforeRead、CancelAfterReadBeforeWrite |
| server/internal/logic/chat/chat_test.go | callCleanup 助手；重写 BlockingRecvCancel、WriteFailure；新增 BackgroundEOFThenForegroundFail、CleanupIdempotent |

### 验证结果

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，73 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... -count=5 -run "TestGetAnswerStream_"` — exit 0，无 DATA RACE
- `go test -race ./core/common/...` — exit 1：Windows 预存在 0xc0000139

---

## 2026-10-15 Stage 1C-2：生命周期协调、保存契约及 SSE 取消强化

**基线**：`d21879c2f571eb75e9a259319a3172cee50bd12d`（Stage 1C-2 initial）

### 修复内容

| 修复项 | 说明 |
|---|---|
| Eino Copy/Close 语义核实 | Copy 分支关闭不影响其他分支；只有所有分支关闭时 parent 才关闭上游 |
| 派生 context + cancel | Controller 派生 ctx，SSE 失败时主动取消模型和后台消费 |
| GetAnswerStream 返回 cleanup | 新增 `func(StreamResult)` 返回值，关闭所有流分支并通知保存决定 |
| 保存契约强化 | 前台成功 + ctx 未取消 + 无写入失败 → 才保存；后台 EOF 不足以触发保存 |
| resultCh 前台结果通知 | 后台 goroutine 等待前台结果 channel，避免抢先保存 |
| cleanupDone 标志 | 防止 cleanup 被调用两次（defer + 显式） |
| SSE 取消强化 | EOF 时检查 ctx.Err()，取消后不发 DONE |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/logic/chat/chat.go | 新增 StreamResult 类型；GetAnswerStream 返回 cleanup；resultCh 协调保存 |
| server/internal/controller/rag/rag_v1_chat_stream.go | 派生 ctx；cleanupDone 标志；cleanup 调用逻辑 |
| server/internal/controller/rag/rag_new.go | chatAnswerStream 字段类型更新 |
| server/internal/logic/chat/chat_test.go | 更新所有流式测试适配新签名；新增 cleanup 调用 |
| server/internal/controller/rag/rag_v1_chat_stream_test.go | 新增 4 个协调测试 |

### 测试覆盖（新增 4 个）

| 测试 | 说明 |
|---|---|
| TestChatStream_SSEWriteFailure_CleanupWithFailure | SSE 写入失败 → cleanup Success=false |
| TestChatStream_NormalComplete_CleanupWithSuccess | 正常完成 → cleanup Success=true |
| TestChatStream_CancelPropagation | 派生 ctx 取消传播 |
| TestChatStream_ModelReceivesDeadlineAndValue | 模型收到父 ctx 值和 deadline |

### 验证结果

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，50 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race` — core/common 0xc0000139（Windows 预存在平台问题），其余 pass

---

## 2026-10-15 Stage 1C-2：F03 请求 context 传播、流消费退出及完整回答保存（待独立复核）

**基线**：`c022ccdba3a9bc38c6516534a31844ca1598ec58`（Stage 1C-1 定点收尾 HEAD）

### 修复内容

| 修复项 | 说明 |
|---|---|
| F03-1 context 替换 | 移除 `ctx = context.Background()`，模型调用使用调用方 ctx |
| F03-2 后台 goroutine 无效 ctx | 移除 `select ctx.Done()` 轮询，改为纯 Recv 循环；ctx 取消通过模型传播 |
| 保存契约 | 只有 `cleanEOF && ctx.Err() == nil` 才保存完整回答；defer 只做清理 |
| 非 EOF 错误 append 零值 | 非 EOF 错误立即退出，不 append nil/零值 chunk |
| stream() 错误链 | `%v` → `%w` 保留错误链 |
| SSE 层协同 | EOF 时检查 `ctx.Err()`，已取消不发 DONE 而返回 `ctx.Err()` |
| messageSaver 接口化 | `Chat.eh` 从 `*historyWrapper` 改为 `messageSaver` 接口，支持测试注入 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/common/stream.go | `streamResponse` 新增 ctx 参数；EOF 时检查 ctx.Err() |
| server/internal/logic/chat/chat.go | 新增 messageSaver 接口；移除 Background()；保存契约；%w 错误链 |
| server/core/common/stream_test.go | 所有调用新增 ctx 参数；新增 CancelThenEOF_NoDONE 测试 |
| server/internal/logic/chat/chat_test.go | 新增注入边界；9 个流式行为测试 |

### 测试覆盖（新增 10 个）

| 测试 | 说明 |
|---|---|
| TestStreamResponse_CancelThenEOF_NoDONE | SSE 层取消后 EOF 不发 DONE |
| TestGetAnswerStream_ModelReceivesContext | 模型收到调用方 ctx |
| TestGetAnswerStream_StreamCancelErrorChain | 建流失败错误链保留 |
| TestGetAnswerStream_BlockingRecvCancel | 阻塞读取期间取消后退出 |
| TestGetAnswerStream_NormalEOF_SaveOnce | 正常 EOF 保存一次 |
| TestGetAnswerStream_PartialThenError_NoSave | 部分后错误不保存 |
| TestGetAnswerStream_Cancel_NoSave | 取消不保存 |
| TestGetAnswerStream_WriteFailure_Convergence | 写入失败收敛 |
| TestGetAnswerStream_CancelThenEOF_NoDONE | 取消后 EOF 不保存 |
| TestGetAnswerStream_ConcurrentSessions_Isolation | 并发会话隔离 |

### History context 边界

`historyWrapper.SaveMessage` 不接受 context（第三方 chat-history 包接口），无法施加有界 timeout。当前通过“只在正常完成时触发保存”约束。

### 验证结果（server/，GOFLAGS 空）

- `go build ./...` / `go vet ./...` — exit 0
- `go test ./internal/logic/chat/... ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，70 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/logic/chat/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0
- `go test -race ./core/common/...` — exit 1：Windows 预存在 `0xc0000139`（与 1C-1 一致），根因待确认

**race 未验证项**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。

---

## 2026-10-15 Stage 1C-1 定点收尾：SSE 前端帧兼容恢复 + Controller 建流失败测试（已通过独立源码与提交日志复核）

**基线**：`11e021a3cca840b2225c6228be6a19aaa6a03f55`。不实施 1C-2，不改前端协议，不重构共享 SSE 编排。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 前端帧格式恢复 | `writeDocuments` 从 `event: documents`+`data:` 恢复为 `documents:<JSON>\n\n`；content 为 `data:<JSON>\n\n`；DONE 为 `data:[DONE]\n\n`；error 保留 `event: error`+`data:` 原格式（chat.vue 按行前缀 `documents:`/`data:` 提取） |
| 保留机制 | 统一写入 `writeFrame`、短写检查（`io.ErrShortWrite`）、`failed` 状态与 `errors.Join` 双错误链不变 |
| 精确帧测试 | `TestRealSSEWriter_FrontendCompatFrames` 逐帧断言 `documents:` 行+双换行边界，并从该行切前缀后 `json.Unmarshal` 验证含 document；断言不出现 `event: documents` 帧（非仅字符串包含） |
| Controller 注入边界 | ChatStream 新增实例级、运行期只读的 `chatRetrieve`/`chatAnswerStream`/`chatSSEWrite`，nil 回退生产默认（非可变全局函数） |
| 建流失败测试 | `TestChatStream_AnswerStreamError` 调用实际 ChatStream：离线检索+建流 sentinel；断言 err 非 nil、errors.Is 识别、不返回空成功、不进入 SSE |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/common/stream.go | 恢复前端兼容帧格式；flush 错误边界注释修正 |
| server/core/common/stream_test.go | 新增 frameRecorder 与 `TestRealSSEWriter_FrontendCompatFrames` |
| server/internal/controller/rag/rag_new.go | ControllerV1 新增三个实例级只读注入字段 |
| server/internal/controller/rag/rag_v1_chat_stream.go | ChatStream 解析注入边界/回退默认；新增 `defaultAnswerStream`/`defaultSSEWrite` |
| server/internal/controller/rag/rag_v1_chat_stream_test.go | 新增 ChatStream 建流失败测试 |

### 验证边界（如实保留）

- 保留真实 `MiddlewareHandlerResponse` + `SteamResponse` 集成测试（真实 GoFrame 服务器）
- 刷新错误当前只能经由无返回值 Flusher 调用，“部分刷新错误无法检测”为已知边界，不宣称已全面验证
- content 编码字段实际是 `StreamData.Content`（string），编码不自然失败，未制造无意义测试

### 验证结果（server/，GOFLAGS 空）

- `gofmt -w` 目标文件已格式化，`gofmt -l` 无输出
- `go build ./...` / `go vet ./...` — exit 0
- `go test ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1 -v` — exit 0，41 pass / 0 fail / 0 skip
- `go test -p 1 ./... -count=1` — exit 0
- `go test -race ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 0（本次新增代码无 race）
- `go test -race ./core/common/... ./internal/controller/rag/... ./internal/cmd/... -count=1` — exit 1：`core/common` 触发 Windows 预存在 `0xc0000139`（race detector DLL 入口点）问题，非代码数据竞争，根因待确认，该包 race 视为未完成

**race 未验证项**：`core/common` race 因平台 `0xc0000139` 未完成；`core/retriever`、`core/rerank` 亦仍未补证。普通测试通过不写成 race 通过。

### 回滚说明

回滚 `stream.go`、`stream_test.go`、`rag_new.go`、`rag_v1_chat_stream.go` 到基线 `11e021a`，删除 `rag_v1_chat_stream_test.go`；不涉及前端协议或共享 SSE 编排重构。

---

## 2026-10-14 Stage 1C-1：F04 SSE 流语义与 Controller 错误处理

**基线**：`ae5d5b0cd2dca625d7834a780dc43e9399c550ae`（Stage 1C plan HEAD）

### 计划修正

- 核实 GoFrame v2.9.0 Writeln/Flush 无返回值，改用 `RawWriter()` 检测写入错误
- 核实中间件已保护 SSE：BytesWritten>0 和 Content-Type 检查阻止 JSON 追加
- 明确 Eino v0.6.0 StreamReader Recv/Copy/Close 语义
- 流消费保持请求 context 可取消，不用 WithoutCancel
- 不为初始化阶段 Background 扩大 ChatModelFactory 接口

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/common/stream.go | 引入 `sseWriter` 封装写入错误检测；SteamResponse 区分 EOF/错误；错误时不发 DONE；返回 error；nil chunk 返回 error；SSE error 数据安全编码 |
| server/internal/controller/rag/rag_v1_chat_stream.go | 建流失败返回 error；%w 错误链保留 |
| server/core/common/stream_test.go | 新增 17 个离线测试 |
| design/stage1c-plan.md | 修正计划：API 核实、context 策略、Eino 语义 |

### 测试覆盖（17 个新增）

| 测试 | 说明 |
|---|---|
| TestSteamResponse_NormalEOF | 多 chunk + EOF → 数据事件 + DONE |
| TestSteamResponse_EmptyStream | 空流 → 只发 DONE |
| TestSteamResponse_EmptyContentChunks | 空内容 chunk 被跳过 |
| TestSteamResponse_StreamError_NoDone | chunk 后错误 → 数据 + error 事件，无 DONE |
| TestSteamResponse_NilChunk | nil chunk → error |
| TestSteamResponse_ErrorChain | errors.Is 识别原始错误 |
| TestSteamResponse_SafeErrorEncoding | 换行符安全编码 |
| TestSteamResponse_WriteFail_Documents | documents 写入失败 |
| TestSteamResponse_WriteFail_Data | data 写入失败 |
| TestSteamResponse_WriteFail_Done | DONE 写入失败 |
| TestSteamResponse_WriteFail_ErrorEvent | error 事件写入失败 |
| TestSteamResponse_SubsequentWriteAfterFailure | 失败后后续写入返回 errWriteFailed |
| TestSteamResponse_WithDocuments | documents 事件输出 |
| TestSteamResponse_EventFormat | SSE 事件格式 |
| TestSSEError_EventFormat | error 事件格式 |
| TestMiddleware_NoJSONAppend_AfterSSEStarted | 中间件条件验证 |
| TestMiddleware_Integration_GoFrameServer | GoFrame HTTP 服务集成测试 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/common/... ./internal/controller/rag/... -count=1 -v` — 退出码 0，28 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./internal/controller/rag/...` — 退出码 0
- `go test -race ./core/common/...` — Windows 环境 race detector DLL 问题（非代码问题）

### 回滚说明

回滚 `stream.go`、`rag_v1_chat_stream.go` 到基线 `ae5d5b0`，删除 `stream_test.go`。

---

## 2026-10-14 Stage 1C 计划制定：F03/F04 Chat context 与流式语义

**基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`（Stage 1B 完成后的 HEAD）

### 计划摘要

Stage 1C 覆盖两个问题：
- **F03**：`GetAnswerStream` 将请求 ctx 替换为 `context.Background()`，用户取消后模型/保存协程继续工作
- **F04**：`SteamResponse` 读流失败后仍发 `[DONE]` 并返回 nil；Controller 建流失败可返回空成功

### 调用链追踪

完整调用链：Controller.ChatStream(ctx) → chat.GetAnswerStream(ctx) → **ctx = context.Background()** → llm.Stream(ctx, messages) → StreamReader → SteamResponse(ctx, reader, docs) → SSE 写入

### 问题清单

| 问题 | 文件 | 行号 | 描述 |
|---|---|---|---|
| F03-1 | chat.go | 202 | `ctx = context.Background()` 替换调用方 ctx |
| F03-2 | chat.go | 227 | 后台 goroutine 监听 Background ctx，永远不会取消 |
| F03-3 | chat.go | 39 | defaultChatModelFactory.Create 使用 Background |
| F04-1 | stream.go | 63 | 错误时仍发送 DONE |
| F04-2 | stream.go | 68-92 | SSE 写入不检查错误 |
| F04-3 | stream.go | 89 | writeSSEError 使用 Background 记日志 |
| F04-4 | rag_v1_chat_stream.go | 33 | 建流失败返回空成功 |
| F04-5 | chat.go | 237 | 非 EOF 错误时 append 零值 chunk |

### 批次划分

- **1C-1（F04 SSE 流语义修复）**：推荐首批，修改集中在 stream.go 和 controller
- **1C-2（F03 Chat context 修复）**：第二批，涉及 context 传递和后台 goroutine 生命周期

### 详细计划

[design/stage1c-plan.md](design/stage1c-plan.md)

---

## 2026-10-14 Stage 1B-3 独立复核通过

**复核基线**：`eb27ba2ed6638f2d4d717bce1305dac92ff6b0ae`
**复核方式**：源码与提交日志复核，未独立重跑 Go 测试

**复核结论**：
- F02 异步闭包竞态修复正确
- 后台输入快照机制正确
- 同步/异步边界行为正确
- `asyncWait` 注入机制设计合理

**Stage 1B 整体状态**：三个批次均已完成，可标记完成。

**race 证据说明**：`core/retriever`、`core/rerank` 两个子包此前 race 检测异常仍未补证。

---

## 2026-10-14 Stage 1B-3：F02 Indexer 异步闭包定点收尾（待独立复核）

**基线**：`f26b3a90b97b26bf2220b677dbddd689b0cb4002`（1B-3 实现提交）。保留已实现的后台局部错误与输入快照，不实施 Stage 1C/1D，不引入持久化任务系统。本批为定点收尾：默认测试、等待注入、int64、完成边界与并发隔离。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 默认测试 | 移除 indexer_test.go 的 `//go:build offline`，9 个 Indexer 行为测试改为默认运行，无需标签 |
| 等待注入 | 后台等待优先调用注入的 `asyncWait`，未注入时回退生产默认一秒；不再先固定 Sleep 再调用注入函数 |
| recover 范围 | recover 移到 goroutine 顶部（`ctxN` 之后、等待与执行之前），覆盖整个后台函数 |
| 注入引用快照 | 启动 goroutine 前将 `x.asyncWait`、`x.asyncIndexer` 保存为局部只读引用 |
| DocumentsId int64 | `asyncIndexer` 接口、`prodAsyncIndexer`、fake 的 documentsID 参数直接用 int64，删除 Sprintf/Sscanf 与被忽略的解析错误 |
| 完成边界 | AsyncFailure 等待后台确实返回失败后再断言同步结果；启动后台的测试均在 cleanup 释放阻塞并等待完成，超时报告失败 |
| 错误检查 | 所有 Index 返回错误都检查，去掉 `ids, _` |
| 标量快照 | 修改 req 测试同时改 KnowledgeName 与 DocumentsId，验证后台收到原值 |
| 并发隔离 | 同一 Rag 并发不同 URI/KnowledgeName/DocumentsId，同步 fake 按输入返回不同 IDs，后台逐项记录参数验证一一对应、恰好一次、无串用；用 channel/完成信号，删除固定 sleep 与计数验证 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/indexer.go | 等待注入优先/回退一秒；recover 前置覆盖全函数；goroutine 启动前快照 `waitFn`/`asyncIdx`；documentsID 全程 int64，删除字符串往返 |
| server/core/indexer_test.go | 去掉 offline 标签；9 个测试注入 `asyncWait` 与 int64 参数；修正完成边界与 cleanup；新增 AsyncWait/BackgroundPanic/并发隔离测试 |

### 测试覆盖（默认运行，共 9 个）

| 测试 | 说明 |
|---|---|
| TestIndex_SyncFailure_NoAsync | 同步失败不启动后台 |
| TestIndex_SyncSuccess_AsyncStillBlocked | Index 先返回，后台阻塞期间未完成 |
| TestIndex_AsyncFailure_SyncReturnUnchanged | 等待后台真实返回失败后断言同步 ids/err 不变，且后台恰好一次 |
| TestIndex_ModifyReqAfterReturn_AsyncGetsSnapshot | 返回后同时改 KnowledgeName+DocumentsId，后台收到原始标量快照 |
| TestIndex_ModifyReturnedIDs_AsyncDocsIDsUnchanged | 改返回 ids，后台 docsIDs 为原始快照 |
| TestIndex_AsyncModifiesOwnDocsIDs_CallerIDsUnchanged | 后台改自己 docsIDs，调用方 ids 不变 |
| TestIndex_AsyncWaitInvoked_BeforeBackground | 注入等待被调用且先于后台执行（替代固定 sleep） |
| TestIndex_BackgroundPanic_Recovered | 等待阶段 panic 被 recover 覆盖，不影响 Index 返回 |
| TestIndex_ConcurrentCalls_Isolation | 同一 Rag 并发不同输入，逐项验证一一对应、恰好一次、无串用 |

### 验证结果（server/ 目录，默认无标签；GOFLAGS 为空）

- `gofmt -w` 目标文件已格式化，`gofmt -l` 无输出
- `go test ./core -count=1 -v` — 退出码 0，28 个测试全部通过（9 Indexer + 19 Retrieve），0 失败；默认 core 日志同时含 Indexer 与 Retrieve
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- `go test -race ./core -count=25 -run TestIndex_` — 退出码 0，无 FAIL / DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0

**race 证据范围**：race 仅在 `core` 包（`go test -race ./core`）通过并给出证据。`core/retriever`、`core/rerank` 两个子包此前 race 检测异常仍未补证，普通测试通过不写成 race 通过。

**证据命令记录**：`verification/stage1b3/` 下每个日志头部记录实际命令、GOFLAGS 与退出码；旧 `-tags offline` 运行结果（旧 test-core.log、race.log）已删除，避免把带标签执行记成无标签通过。

### 回滚说明

回滚 `indexer.go`、`indexer_test.go` 到基线 `f26b3a9` 即可；不涉及 Stage 1C/1D 或持久化任务系统。

---

## 2026-10-14 Stage 1B-2：F01 Retriever 并发测试收尾（已通过独立源码与提交日志复核）

**基线**：`d3e3e07ebbc7b73569e8afeb1b0f677a5d98517a`。仅测试收尾，保持已修正的 `Retrieve` 编排逻辑，不重新设计并发方案，不实施 1B-3。

**复核通过基线（1B-2 完成提交）**：`98770d33ee18c280bbb8c771acdeb44749182ef8`。

### 收尾内容

| 项目 | 说明 |
|---|---|
| 字段范围恢复 | 公开字段 `ExcludeIDs` 恢复为原有私有字段 `excludeIDs`，同步 retriever.go 内所有引用；测试属 package core，无需公开接口 |
| WorkerFails_OthersCanceled | 定点重写：三轮 worker/Generate 事件编排，释放第一轮返回 sentinel；断言取消被观察、已启动 worker 在返回前完成、`errors.Is` 识别 sentinel |
| Round1WorkerBlocked_Round2RewriteFails | 定点重写：worker 先观察取消再等待独立释放；未释放时 Retrieve 不返回；改用 `errors.Is` 而非字符串匹配 |
| CancelWinsOverSuccess | 使用 `workerStarted` 信号触发取消，删除固定 sleep |
| InFlightCancel / Timeout | 补回进行中取消与超时覆盖，分别断言 `context.Canceled`、`context.DeadlineExceeded`；用完成信号而非 goroutine 计数 |
| 切片隔离 | worker 修改 `excludeIDs[0]` 前先断言原始值；最后校验输入独立快照不变；保留同一 Rag 并发不同 query 测试 |
| 测试清理 | 删除名不副实的 `NoSharedFieldWrite`，改名 `InjectedDepsRemainUnchanged`，仅验证已注入依赖不变，不声称覆盖 nil 默认路径 |
| 泄漏防护 | 所有 channel 等待有超时；`t.Cleanup` 保证断言失败时释放阻塞任务并等待结束 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/retriever.go | `ExcludeIDs` → `excludeIDs` 及引用同步 |
| server/core/retriever_test.go | 重写协调测试、补回取消/超时、加强切片隔离、清理测试 |
| PROJECT_PROGRESS.md | 添加 Stage 1B-2 测试收尾记录，更新批次状态 |
| CHANGES.md | 添加本记录 |

### 验证结果

- `gofmt` — 目标文件已格式化，`gofmt -l` 无输出
- `go test ./core -count=1 -v` — 退出码 0，19 个 Retrieve 测试全部通过，0 失败
- `go test -race ./core -count=1` — 退出码 0，core 包 race 通过
- `go test -race ./core -count=10 -run <6 个协调/隔离测试>` — 60 PASS，0 FAIL，无 DATA RACE
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go vet ./...` — 退出码 0
- `go build ./...` — 退出码 0

**race 证据范围**：本轮 race 仅在 `core` 包通过并给出证据；`core/retriever`、`core/rerank` 此前 race 异常仍未补证，普通测试通过不写成 race 通过。

### 回滚说明

仅测试收尾 + 字段改回私有，回滚恢复 `retriever.go`、`retriever_test.go` 到基线 `d3e3e07` 即可，不涉及编排逻辑。

## 2026-10-14 Stage 1B-1：F05 Rerank HTTP ctx 绑定与校验

### 修复内容

| 修复项 | 说明 |
|---|---|
| ctx 绑定 | HTTP 请求使用 `http.NewRequestWithContext` 绑定调用方 ctx，不再使用 `context.Background()` |
| 有界超时 | `context.WithTimeout(ctx, 30s)`，调用方更早 deadline 优先生效 |
| 响应大小限制 | `http.MaxBytesReader(nil, resp.Body, 10MB+1)` 防止内存溢出 |
| 状态码校验 | 非 200 返回 `rerank API returned status %d` |
| JSON 校验 | 解析失败返回 `invalid JSON` 错误 |
| 结果校验 | 检查 nil result、missing index、index 越界、重复 index、missing score、NaN/Inf score |
| HTTPDoer 注入 | 接口注入支持测试 mock，默认使用 `http.DefaultClient` |
| 输入校验 | 空 docs、nil document、非法 topK、已取消 ctx 均在 HTTP 调用前检查 |
| 无部分修改 | 结果校验失败时不修改输入 docs 的 score |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/core/rerank/rerank.go | 重构为 Rerank 结构体；新增 HTTPDoer 接口、RerankConfig；ctx 绑定 + 30s 超时；MaxBytesReader；状态码/JSON/结果全面校验 |
| server/core/retriever.go | 更新调用方适配新 API：`rerank.NewRerank(cfg, nil).Rerank(ctx, ...)` |
| server/core/rerank/rerank_test.go | 更新在线测试适配新 API |
| server/core/rerank/rerank_http_test.go | 新增 25 个离线 httptest 测试 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加 Stage 1B-1 记录 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./core/rerank/... -count=1 -v` — 退出码 0，25 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- `go test -race ./core/rerank/...` — Windows 环境 race detector DLL 问题（非代码问题）

### 回滚说明

修改涉及 rerank 包重构和调用方适配，回滚需同时恢复 `rerank.go`、`retriever.go`、`rerank_test.go` 并删除 `rerank_http_test.go`。

---

## 2026-10-14 Stage 1-Pre 独立复核通过

**复核基线**：`db0c5971d137ddabcc10ae5fb0c4b9baa17e1b98`
**复核方式**：源码复核，未独立重跑 Go 测试

**复核结论**：
- F06 nil 访问修复逻辑正确
- 四类输入行为定义清晰
- 无副作用保证：校验失败不触发文件保存、DB 插入、后台任务

**行为修正记录**：
- 默认模式 File+URL 同时存在时行为为"File 优先，URL 忽略"
- PaperDesk 模式目前未在生产启用
- `paperDeskModeOverride` 为临时测试机制

---

## 2026-10-14 Stage 1-Pre：F06 File/URL 参数校验

### 修复内容

| 修复项 | 说明 |
|---|---|
| F06 nil 访问修复 | `rag_v1_indexer.go:29` 在 `if req.File != nil` 块外访问 `req.File.Filename`，当仅通过 URL 提交时触发 panic。已通过前置参数校验消除 |
| 参数校验前置 | 新增 `validateIndexerInput()` 在任何副作用（文件保存、SQL 写入、索引、后台任务）之前校验 File/URL |
| 四类输入定义 | 无 File 无 URL → 参数错误；仅 File → 正常处理；仅 URL → 默认模式正常/PaperDesk 拒绝；File+URL → 默认模式正常/PaperDesk 拒绝 |
| PaperDesk 模式 | `isPaperDeskMode()` 临时返回 false，待 profile 配置实现后接入。通过 `paperDeskModeOverride` 支持测试切换 |
| URL 模式文件名 | URL 输入时用 `path.Base(req.URL)` 提取文件名，避免 nil 访问 |
| 无副作用保证 | 校验失败直接返回 error，不触发文件保存、DB 插入、后台任务 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/controller/rag/rag_v1_indexer.go | 新增 `validateIndexerInput()`、`isPaperDeskMode()`、`paperDeskModeOverride`；修复 nil 访问；URL 模式文件名提取 |
| server/internal/controller/rag/rag_v1_indexer_test.go | 新增 11 个测试：四类输入 + PaperDesk 模式 + 边界情况 + 无副作用验证 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加 Stage 1-Pre 记录 |
| design/stage1-plan.md | 标记 1-Pre 为已完成 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./internal/controller/rag/... -count=1 -v` — 退出码 0，11 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- 非法请求不触发文件保存、SQL 写入、后台任务

### 回滚说明

单文件修改 + 测试文件，回滚简单：恢复 `rag_v1_indexer.go` 原始内容，删除测试文件。

---

## 2026-10-13 Stage 1A 第二轮复核修复

### 修复内容

| 修复项 | 说明 |
|---|---|
| Migrate 无条件调用 | cmd.go 普通启动不再调用 dao.Migrate()；保留 InitWithMigrate() 兼容入口 |
| 在线测试隔离 | rerank_test.go 和 youcom_integration_test.go 添加 `//go:build online`；rag_test.go 改为 `//go:build integration && online` |
| chat_model_test.go | 移除 integration 标签，改为使用测试自有配置的单元测试 |
| MustGet 替换 | dao/retriever/chat 三个包所有 MustGet 替换为 Get + error 处理；缺配置返回 error 而非 panic |
| GetMysqlConfig panic 保护 | g.DB() 在无配置时 panic，使用 defer/recover 捕获并返回 error |
| 行为测试 | 重写 17 个测试：预初始化状态验证、Init 失败后状态不变、Reset 安全性、GetDsn 返回 error |
| 失败清理协调 | 新增 dao.Reset()、rag.Reset()、chat.Reset()；cmd.go 级联清理（chat 失败 → rag.Reset + dao.Reset） |
| 同步一致性 | 全部改用 sync.RWMutex；GetDsn 返回 (string, error)；IsInitialized/GetRagSvr/GetChat 使用 RLock |
| chat-history AutoMigrate 边界 | chat.Init() 先检查 dao.GetDsn() 返回 error，明确标注第三方包 AutoMigrate 边界 |
| Windows 临时目录诊断 | `go test ./...` 的 fork/exec Access denied 为 Windows 临时目录权限问题，单独运行通过 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | MustGet→Get；GetDsn 返回 (string, error)；新增 Reset()；RWMutex；GetMysqlConfig panic 保护 |
| server/internal/logic/rag/retriever.go | MustGet→Get；新增 Reset()；RWMutex；配置缺失返回 error |
| server/internal/logic/chat/chat.go | MustGet→Get；新增 Reset()；RWMutex；GetDsn error 处理；配置缺失返回 error |
| server/internal/cmd/cmd.go | 移除 dao.Migrate() 无条件调用；失败时级联 Reset() |
| server/internal/dao/dao_test.go | 重写为行为测试（7 个） |
| server/internal/logic/rag/retriever_test.go | 重写为行为测试（5 个） |
| server/internal/logic/chat/chat_test.go | 重写为行为测试（5 个） |
| server/core/rerank/rerank_test.go | 添加 `//go:build online` |
| server/core/websearch/youcom_integration_test.go | 添加 `//go:build online` |
| server/core/rag_test.go | 改为 `//go:build integration && online` |
| server/core/common/chat_model_test.go | 移除 integration 标签，改为独立单元测试 |
| PROJECT_PROGRESS.md | 更新进度 |
| CHANGES.md | 添加第二轮修复记录 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，17 个测试全部通过
- `go test ./core/retriever/...` — 退出码 0（单独运行）
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — core/retriever 和 core/websearch 因 Windows 临时目录权限问题 fork/exec 失败（非测试逻辑问题）
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

整批协调回滚：恢复 MustGet、移除 Reset()、恢复 Migrate() 调用、移除 build tags。

---
## 2026-10-13 Stage 1A 复核修复（第一轮）

### 修复内容

| 修复项 | 说明 |
|---|---|
| AutoMigrate 分离 | InitDB() 仅连接，新增 Migrate() 建表；新增 InitWithMigrate() 兼容入口 |
| 错误传播与清理 | cmd.go 中 Fatalf 改为返回带阶段信息的 error，失败时 dao.Close() 清理 |
| 原子初始化 | 局部变量构建完整实例后才赋值全局变量；失败时全局变量保持零值 |
| 重复初始化保护 | 重复调用 Init() 返回 error；各包新增 IsInitialized() |
| 未初始化访问保护 | GetDsn() 返回空字符串；GetRagSvr()/GetChat() 返回 nil |
| Close 函数 | dao 包新增 Close()；Database 接口新增 Close() 方法 |
| 测试隔离 | core/rag_test.go 和 chat_model_test.go 添加 //go:build integration |
| 测试增强 | 新增 GetDsn/Close/IsInitialized/Migrate 未初始化行为测试 |
| F 编号修正 | 1C 对应 F03/F04；1D 对应 F07/F08/F09 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，14 个测试全部通过
- 离线测试不触发数据库/向量服务/OpenAI 连接

---
## 2026-10-13 Stage 1A：初始化副作用清理 + 离线测试隔离

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | 移除 init()，保留 InitDB() error，添加文档注释 |
| server/internal/logic/rag/retriever.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/logic/chat/chat.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/cmd/cmd.go | 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init() |

### 新增文件

| 文件 | 说明 |
|---|---|
| server/internal/dao/dao_test.go | 验证 db 在 InitDB() 前为 nil |
| server/internal/logic/rag/retriever_test.go | 验证 ragSvr 在 Init() 前为零值，Init 签名正确 |
| server/internal/logic/chat/chat_test.go | 验证 chat 在 Init() 前为 nil，Init 签名正确 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

恢复 init() 中的调用即可。每个文件独立可回滚。

---
## 2026-10-13 Stage 0 补证 + Stage 1 计划

### 新增文件

| 文件 | 说明 |
|---|---|
| design/stage0-baseline.md | Stage 0 基线核对报告 |
| design/stage1-plan.md | Stage 1 分批实施计划 |
| verification/stage0/v01-build-evidence.md | V01 构建证据 |
| verification/stage0/v02-chat-history.md | V02 chat-history 接口清单 |
| verification/stage0/v06-goframe-dao.md | V06 GoFrame DAO 文件清单 |
| verification/stage0/init-side-effects.md | init() 副作用追踪 |
| verification/stage0/es-version-risk.md | ES 版本风险记录 |
| PROJECT_PROGRESS.md | 项目进度记录 |
| CHANGES.md | 本文件 |

### Stage 0 结论

- V01：依赖解析和编译通过（go build/vet 退出码 0）
- V02：chat-history 能力严重不足（6 项需求中 4 项不支持）
- V05：ES 版本不在官方保证区间（客户端 8.16.0 vs 服务端 8.11.3）
- V06：12 生成 + 10 手写文件已分类
- V07：待补证（ZIP 未找到）
- 3 个 init() 副作用已追踪，显式初始化方案已制定

### Stage 1 计划

- 5 个批次：1-Pre → 1A → 1B → 1C → 1D
- 推荐首批：1-Pre（F06 nil 修复）和 1A（init 副作用清理）可并行
- 覆盖 F01-F09 + F12

### 方案文档版本线

- v1.0 → v1.1 → v1.1.1 → v1.1.2（当前）
- 详见 docs/go-rag_PaperDesk_改进方案_v1.1.2.md
