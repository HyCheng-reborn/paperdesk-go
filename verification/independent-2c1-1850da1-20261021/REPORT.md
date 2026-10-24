# Stage 2C-1 独立复核报告

日期：2026-10-21（Asia/Shanghai）。结论：**存在阻塞**，仅指 2C-1。未更新任何验收状态。

## 审查对象与保护边界

- 基线：8d2c21225d4627a2668cad788cc254f78f2e04e4。
- 目标：1850da1fcff2e66c1ed61594139e7e2e068fd775；实现分支 codex/stage2c1-dimensions 指向该完整 SHA。
- git merge-base --is-ancestor 基线 目标 EXIT 0；累计 37 文件，2462 insertions、127 deletions；两个提交：5bb15e8 实现与必要测试、1850da1 文档及证据。
- 目标 server 树 fa3f1c76f7c558247a128efd1b0a49acbea65f1d，与实现者实际验证的 5bb15e8eb7c17070eaa29203efcf58fb008c0b53:server 完全相同。对象核实见 git-objects.log，不凭未提交文件推断交付内容。
- 先 git worktree list 定位：主仓 review/stage1a 脏树、实现树、历史 review-1d3。另建 detached 独立审查树 C:/Zhy/code/go-rag/.review-stage2c1-1850da1；没有 checkout/reset/clean/stash 或移动原工作树。测试前后脚本调用 git status --porcelain 均无条目输出；PowerShell 空管道未创建 clean-*.log，最终另存 final-review-tree-status.log（空文件）确认。
- 已检查适用祖先路径与目标内 AGENTS.md：未发现该文件，目标 Git 也无根 AGENTS.md。已读 design/stage2-plan.md（含完整 §5.8）、PROJECT_PROGRESS.md、CHANGES.md、verification/stage2c1/README.md、环境/执行脚本与相关测试和证据。实现者证据仅作溯源，不作为本次独立 PASS。
- 未改生产代码，未提交、推送、合并、修改验收状态或开始下一批。临时测试由 Go -overlay 注入，源文件仅保存在本报告目录。

## 阻塞 B1 / P1：真实配置解析出的合法整数被拒绝

位置：server/core/config/dimensions.go:17–35，尤其 reflect switch（23 行）；调用点 server/internal/logic/rag/retriever.go:719–734（728 行 v.Val）。测试缺口：server/internal/logic/rag/dimensions_test.go:23，dimensionGetter 人造 Go int。

触发：在 YAML 中配置 embedding.dimensions: 768，或 provider.embedding_dimensions: 768。通过本仓锁定 GoFrame v2.9.0 的默认 AdapterFile.SetContent → g.Cfg → readRagConfig 读取，合法数字的实际类型是 encoding/json.Number（底层 reflect.String），不是 Go int。ParseEmbeddingDimensions 只接受 int/uint，因而返回 ErrEmbeddingDimensions。

影响：旧模式唯一实际可启用的配置入口不能使用合法显式维度（包括显式 1024）；新 Provider 的合法维度也在配置层误拒绝。程序化 Config/RagConfig 路径能贯通，不能代替生产 YAML 路径。错误发生在设施副作用前，属于错误拒绝，不是泄漏门禁。

独立复现：supplemental-file-adapter.log，命令和完整目标 SHA/GOFLAGS 已记录，EXIT 1。TestIndependentDefaultFileAdapter/legacy 与 /provider 均输出 “must be a positive integer, got json.Number”。内存 AdapterContent 的独立复现结果相同（supplemental.log）。合法 768 的预期成功断言失败；0、负数、字符串、浮点、bool 的拒绝断言通过，但这不能证明当前实现对真实整数正确。临时源码 independent_test.go、overlay.json；首次源码另存 independent-first_test.go。

最小修复方向（仅建议，未实施）：在共享严格解析器中显式支持 json.Number，做整数语法、正值、平台 int 范围检查；保留普通字符串、浮点、bool、0、负数与溢出拒绝。补默认 GoFrame 文件适配器 → readRagConfig → normalizeDimensions 的测试，覆盖 legacy/provider 合法整数、缺失、非法、冲突和门禁。不要用宽松 .Int() 转换掩盖类型错误，也不要仅修改 fake getter。

没有发现其他本批阻塞。已有树内测试不是复制生产解析逻辑，但其配置 mock 绕过了真实反序列化表示，这是 B1 漏测原因。

## 独立测试与命令

实际 cwd 为干净目标的 server。Go 1.27.1 windows/amd64，CGO_ENABLED=1，CC/CXX 为现有 C:/msys64/ucrt64/bin/gcc.exe、g++.exe；进程级 PATH，GOFLAGS 空，GOPROXY=off、GOSUMDB=off；无 online/integration 标签、无真实设施或模型调用、无系统工具链修改。完整可重跑命令见 executed/run.ps1，单条日志均含目标/命令/退出码。

受影响包 P = ./core/config ./core/common ./core/vector ./core/indexer ./core/retriever ./core ./internal/logic/rag。

| 命令 | 结果 | 实际命中 |
|---|---|---|
| go test P -run ^TestDimensions_ -count=1 -v | EXIT 0 | 18 顶层 PASS / 41 RUN（含子项） |
| go test P -count=1 -v | EXIT 0 | 223 顶层 PASS / 260 RUN |
| go test -race P -count=1 -v | EXIT 0 | 223 顶层 PASS / 260 RUN，0 DATA RACE |
| go test -p 1 ./... -count=1 | EXIT 0 | 全量实际执行；无测试包单独标记，无 -v 不伪造用例计数 |
| go vet ./... | EXIT 0 | 静态检查 |
| go build ./... | EXIT 0 | 构建 |
| go test -overlay <本目录>/overlay.json ./internal/logic/rag -run ^TestIndependent -count=1 -v | EXIT 1 | 合法 YAML 768 被拒绝；默认文件适配器双入口复现 |

executed/ 是干净目标的独立执行。目录根部 focused/affected/race/full/vet/build.log 是最初沙箱无法访问现有 Go build cache 的失败尝试（均 EXIT 1、setup failed），不算源码失败或成功，不覆盖删除。获准使用现有缓存后重跑全部成功。补充测试另列，不把 overlay 测试说成目标内交付测试。没有新增并发生产逻辑或 race 报告，因此未机械重复全部 race。counts.log 为计数摘要。

## 真实生产维度流向与边界

1. readRagConfig → readEmbeddingDimensions → ParseEmbeddingDimensions 是实际配置读取链；目前在这里遇到 B1。若程序化输入合法 int，normalizeDimensions 在工厂前解析，复制 Provider 快照，给 RagConfig 和 VectorCfg 写入同一值，显式 VectorCfg 冲突拒绝；不会修改调用者 Provider。
2. defaultVectorStoreFactory → NewVectorStore → ES/Qdrant 构造器存维度；defaultRagFactory 再规范化并传入 core.Config；core.New ResolveDimensions、Copy 后使用私有配置。Config.Copy 保留维度并复制 Provider。旧模式缺失集中使用 DefaultEmbeddingDimensions=1024；新 Provider 优先声明，允许显式 legacy fallback、两者冲突拒绝、孤立 provider dimensions 拒绝，不给未知新模型任意默认值。
3. core.New 实际走 Config.IndexExists/CreateIndex，而非 vector.CreateIndex。Config.CreateIndex 和 vector 两种公开 CreateIndex 共用 ESMapping/QdrantCollection；common.CreateIndexIfNotExists 是无生产调用者的 deprecated 兼容入口，也改用共享 schema。没有遗漏第二套实际生产硬编码。保留的 1024 分属集中兼容默认、注释、测试/fake 和 rerank 非向量限制，不要求机械删除。
4. common.NewEmbedding 对支持请求尺寸的既有前缀 text-embedding-3-*、Qwen/Qwen3-Embedding-* 发送解析维度；固定协议模型不擅自增加 dimensions 参数。dimensionEmbedder 验证返回数量与每条长度，文档/查询共用；不匹配不会继续发送向量写入/搜索。响应错误链保留。
5. newIndexer/newAsyncIndexer 将同一维度交给 QdrantIndexerConfig.VectorDim，进一步进入锁定 Eino SDK 的 ensureCollection；实际 StoreWithNamedVectors 使用同一 embedder。ES sync/async SDK bulk 路径同样使用该 embedder。newRetriever 用同一配置为 ES script_score、Qdrant content/QA query 构造向量。提交内 httptest/bufconn 用例调用这些真实构造/Store/Retrieve，并检查实际请求向量 768，独立执行均通过。
6. ProviderProfile 由 buildRegistry 使用解析后的配置构造，缺少维度报错；本批不允许新 Embedding profile 生产启用。InitWith、core.New 提前拒绝；直接 indexer/retriever 也必经 common.NewEmbedding 的 ErrEmbeddingGuardNotReady。全仓非测试 GetEmbedder 无额外业务调用者；没有通过 Registry 绕开业务门禁的生产接线。
7. 本批没有对既有索引做 fingerprint 证明。存在索引仍沿旧兼容行为使用，不读回证明身份，不自动修改、删除、重建或补写身份；同维度不代表同模型。SDK ensureCollection 的第二创建点仍存在，是 2C-2 必须约束的既有生命周期问题，不将其误报为本批已解决。
8. 累计 diff 未改 Provider budget/lease/transport/流转发、Stage 1 cleanup。Config.Copy/初始化调整没有刷新预算 owner；目标受影响包 race 与全量回归通过。此为本批回归证据，不是全面重审 2A/2B 或证明真实 SQL/ES/Qdrant 取消行为。

## 2C-2 设计意见：仅评估，不实施，不因此阻塞 2C-1

已完整读 §5.8。整体方向可接受，但 Qdrant 是有条件方案，不能按现有代码已具备保证处理。

推荐接受：
- ES mapping _meta.paperdesk_embedding 随首次 CreateIndex 同请求写入，再读取比较；比自造 settings 合适。本地 v8.16.0 TypeMapping.Meta_ 有字段；官方说明可创建/读取/更新 _meta，所以它是可变应用元数据，不是不可篡改证明。应配权限控制且不以覆盖元数据“修复”冲突。依据：https://www.elastic.co/docs/reference/elasticsearch/mapping-reference/mapping-meta-field 。
- fingerprint 版本化、保存规范化原文与 hash，包含语义/model revision/dimensions/distance/named vectors，排除密钥、预算与并发；不能复用含凭据身份的 CacheKey。缺失/未知版本/不匹配/Initializing 均拒绝所有写入、异步任务与查询，禁止把当前配置补成旧索引身份。
- already_exists 只读回胜出者；失败不自动删别人资源，不自动迁移。旧资源换新物理名重嵌入与人工切换仅为未来另行授权流程。

需要调整为可验收规则：
- ES alias 应在校验时解析并固定物理索引（必要时 index UUID），本次读写使用同一物理资源；否则校验后 alias 改指向可绕过。对多索引 alias、write-index、读 alias 指向集合必须明确拒绝或逐个验证，不能只检查一个 mapping。同名删除重建也存在校验/操作之间竞态，需资源不可复用约束或可验证 generation，不能把一次 read-back 当永久证明。
- Qdrant manifest 绑定 backend/store namespace（实际集群身份，不只是任意 URL 文本）/不可复用物理名/generation，DB 唯一约束和 CAS owner fencing。Creating/Failed/Ready 的崩溃窗口、超时接管、部分成功、恢复流程需逐项规定；不能仅凭“collection 存在且维度对”把悬挂记录升 Ready，Ready 发布前应完成 schema/payload 配套索引等必需步骤。现有 Qdrant 创建后 field-index 失败会遗留 collection，必须有明确失败记录和拒绝语义。
- 当前 Config.IndexName 由配置直接指定，公开 vector CreateIndex 可接任意名，Qdrant SDK.ensureCollection 自动创建；现有入口没有强制唯一名和受控创建保证。2C-2 须覆盖全部创建入口，禁掉/约束 SDK 第二创建点。不能仅靠文档要求随机名就宣称已实现保证。
- Qdrant manifest 引入新表、迁移与 DB 生命周期依赖；后端服务和关系库没有原子事务。必须把数据库不可用、备份/恢复不一致、collection 已删而 manifest Ready、外部同名重建全部列为失败关闭或严格运维前提。sidecar collection 同样不解决双写，不推荐首版。

需要用户/设计确认：
- 是否接受 ES/Qdrant 分流和新增 manifest 表的阶段范围与成本；若不接受，Qdrant guard 继续关闭，另行选方案。
- 是否能实际保证独占管理、不可复用物理名、备份恢复 manifest 与资源同步；外部同名重建无法凭当前 manifest 可靠识别。未保证前不得解除门禁。
- fingerprint v1 必需字段、空 immutable revision、漂移模型 alias、私有 deployment namespace 的证明政策与规范化规则；稳定模型名称本身不能证明版本未漂移。
- alias/物理资源的固定规则、跨进程初始化与崩溃恢复责任、持久化表迁移所属批次。这些不能悄悄扩展成已批准的迁移/生命周期工程。

本地锁定 Qdrant go-client v1.15.2 的 CreateCollection/CollectionConfig 无 Metadata；官方 collection metadata 文档为后续服务器能力，不能据此推断当前部署或 SDK 已支持（https://qdrant.tech/documentation/manage-data/collections/#collection-metadata）。未连接任何真实资源核实。2C-2 意见独立于 B1，不要求在 2C-1 中实现。

## 停止点

2C-1 因 B1 暂不能通过；所需最小调整限定在真实配置数值表示及对应测试。未发现需要为本批推进 fingerprint、迁移或在线探针的理由。无风格或可选重构阻塞。本报告保存后停止。

