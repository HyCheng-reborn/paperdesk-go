# Stage 2B 独立源码复核与离线验证

**结论：存在阻塞，不能验收 Stage 2B。** 干净提交原有测试、指定范围 race、全量、build、vet 均通过；但实际预算/业务接线、Embedding 启用门禁与原始响应映射、Registry 生命周期及 builder 注册契约存在具体缺陷。独立补测复现了 HTTP 超额发送、向量错配、无 guard 启用、静默旧模型回退、失败发布和真实 DATA RACE。

## 1. 固定对象与执行保护

- 日期：2026-10-19。
- 完整基线：`df260ddfb12694e7064d74fb63cc1c228f8ebbfe`。
- 完整目标：`a24cb8d609e6c4580b0726cb7de0ccd21d2efdd1`。
- 两个 Git 对象存在；merge-base --is-ancestor 成功。审查整个累计范围，26 文件、2727 insertions/10 deletions；不是只看摘要或末次提交。
- 原仓库 `C:/Zhy/code/go-rag`；复用 `C:/Zhy/code/go-rag-review-1d3`。git worktree list 与 rev-parse --git-common-dir 确认为 linked worktree，共用 `C:/Zhy/code/go-rag/.git`，没有 clone/fetch。
- 切换前将旧的 review_previous_test.go、review_remaining_test.go 移入本目录 preserved/，各保存 SHA256。历史独立报告和旧日志不覆盖。只切换审查 worktree，不操作主工作区分支，不清理 CRLF 或他人修改，不改 Git 配置。
- 未发现适用祖先或审查树中的 AGENTS.md。已读批准方案、2A 最终独立报告、进度/变更、go.mod/go.sum、本批全部生产及测试增量、verification/stage2b/；沿实际调用链读未修改的 core/rag.go、retriever、indexer、bootstrap/cmd、Reset 与锁定 SDK。依赖文件未改，不重审已关闭 Stage 1。
- 全部指定验证先在干净目标进行，结束后才加入5个审查方测试文件。最终 git diff --exit-code -- server 成功；仅5个新复现文件未跟踪。未改生产代码/方案/进度，未提交、推送或合并。

## 2. 阻塞问题（按严重度）

### B1 · P1：预算仍未接入实际 HTTP，Registry 所绑额度完全不生效

**目标位置**：`server/core/providers/bailian/chat.go:71`、`:80`；`bailian/embedding.go:59`；`core/providers/transport.go:125`；`internal/logic/rag/retriever.go:120`。

**实际调用链**：业务/Registry.GetChat → bailian.BuildChat → chatAdapter.Generate/Stream → 内层 Eino → HTTPClient.Do。Embedding/Rerank 同样直接调用内层实现。生产代码除 budget.go 本身没有 Reserve/ReserveCtx/Commit 调用，没有请求/任务 owner 创建、移交与 lease 结算接线。

**复现**：`TestReviewRegistryBudgetCoversHTTP` 以真实 NewRegistry 绑定 `ProcessBudget(MaxRequests=1)`，通过真实百炼构造与锁定 SDK 向 httptest 连续 Generate 两次：服务器收到 **2** 次，第二次无错误，进程 Snapshot 的 AttemptsUsed/Committed/InFlight/token 全为 **0**。

**后果**：尝试、token、slot、总 deadline 均不能保护实际调用；usage 缺失也没有任何保守账本结算。非流式 body/stream 终结没有关联 lease。`buildRegistry` 每次另建 `(0,0,0,0)` 的无限预算，连真正的进程 owner 共享和预算配置入口也未落地，不能以“2C配置化”推迟已批准2B预算集成。

**最小修正**：沿真实入口创建一次 request/task owner，共享进程预算；所有实际 sender 在发送前双作用域预留并 Commit，重试不刷新 owner；接入真实 body/stream 终结与一次性保守结算，保持请求/服务 root 的阶段取消语义及Stage 1 SSE/流关闭所有权。若不支持自动重试须明确禁用；若支持，实际每次尝试都须计入。测试用服务器实际计数、阻塞 body 和真实 stream Close/错误/取消观察，不把测试手写 Finish 当生产接线。本问题属于2B，不要求实现跨进程预算或精确金额计费。

### B2 · P1：新 Embedding profile 可经生产配置直接启用，缺少2C前门禁

**目标位置**：`internal/logic/rag/retriever.go:180`–`:195`；`core/common/embedding.go:16`–`:23`；实际调用方 `core/indexer/indexer.go:19`、`indexer_async.go:18`、`core/retriever/retriever.go:25`。

**链路**：provider 配置 Scan → ProviderConfig → buildRegistry 构造任意 Embedding Profile → common.SetRegistry → core.New 的两个 indexer/两个 retriever → common.NewEmbedding → Registry.GetEmbedder。没有模型身份/索引维度 guard 或生产启用拒绝分支。

**复现**：`TestReviewEmbeddingProductionGate` 使用与生产相同的 gvar.Scan，读取合法 `embedding_model=text-embedding-v4, embedding_dimensions=128` 等 snake_case 配置，调用生产 buildRegistry、common.NewEmbedding（保留 existing-1024-index/旧模型配置）。实际本机HTTP收到1次，返回128维成功，没有启用门禁错误。未连接或写入真实向量库。

**后果**：同维度新模型也能进入旧向量空间，不同维度则可直到实际写入/检索才失败。底层存储可能拒绝维度不符，不等于本层门禁成立；本轮没有声称已发生真实数据污染。

**最小修正**：2C guard 尚不存在时，生产入口对新 Embedding profile fail closed，保留离线适配器构造测试能力。可明确保留旧 Embedding 供 Chat/QA/Rewrite 先迁移；不能仅靠注释，也不要为此提前实现整个2C。当前 Provider 非nil但没有配置 Embedding 时 common.NewEmbedding 仍会调用未配置的 GetEmbedder，故应同时明确“未指定角色继续旧路径”与“明确配置新角色拒绝/启用”的区别。

### B3 · P1：Embedding 验证太晚，原始 index 丢失后向量静默关联错误文本

**目标位置**：`server/core/providers/bailian/embedding.go:50`、`:59`–`:67`、`:82`。

**源码证据**：BuildEmbed 仍调用 providers.BuildEmbedRole → 锁定 acl/openai v0.1.3 `embedding.go:151–169`。该 SDK 遍历 `resp.Data` 按数组位置生成 `[][]float64`，不使用 `d.Index`，usage 也已转成无 presence 的值；百炼包装层只剩数量、维度和有限值可检查。

**复现**：`TestReviewEmbeddingRawIndices` 通过实际 BuildEmbed→SDK→httptest：

- 乱序 index=[1,0] 被原样按数组顺序返回，第一个输入拿到约0.9而非index0的0.1。
- 重复 index=[0,0] 数量/维度合法，成功返回。
- 首项没有 index，同样成功返回。

**后果**：不同文本的向量可静默错配；计数与维度正确并不能保证索引正确性。ErrEmbedUsageMissing 虽已定义但没有使用，现有包装也无法可靠区分 usage 缺失；缺失本身可以允许，但须在B1中保留额度并标 unknown，不能自称已完成原始 presence 校验。

**最小修正**：在 SDK 丢失元数据前的共享响应解析边界验证 index presence/唯一性/范围/覆盖与顺序（严格拒绝或按 index 可靠重排），以及 usage presence；再暴露向量。保留现有数量、维度、有限值错误链。不要只分别测试 fake/helper 或在 `[][]float64` 返回后再试图还原 index。

### B4 · P1：Provider角色接线不完整，配置启用后仍可静默使用旧角色或旧模型

**目标位置**：`core/common/chat_model.go:48`、`:96`；`internal/logic/chat/chat.go:48`–`:59`；`core/retriever.go:255`–`:262`；`internal/logic/rag/retriever.go:202`、`:395`。

**触发、证据与后果**：

1. common.GetChatModel/GetRewriteModel 在 Registry 判断前先返回旧包级 singleton。`TestReviewLegacyCacheDoesNotOverrideRegistry` 先以显式旧配置创建模型，再设置新 Registry；cfg=nil 仍拿到旧模型，Registry被忽略。
2. defaultChatModelFactory.Create 吞掉 Registry.GetChat 错误并直接构造旧OpenAI模型。`TestReviewChatFactoryPropagatesRegistryFailure` 用生产工厂和返回 ErrBudgetExhausted 的Registry接口边界复现：返回 *openai.ChatModel、error=nil。显式非nil ChatConfig 的参数也会被成功的全局Registry路径忽略，配置选择边界未闭合。
3. 真正的 Rag.Retrieve 末段始终 `rerank.GetConf → rerank.NewRerank(...,nil)`，没有 Registry.GetReranker 调用。因此 provider.rerank_model/url 即使配置也没有影响业务排序。common.GetRerankModel 是旧 ChatModel 工具，并非这里的真实排序路径。
4. `readRagConfig` 对 provider 的 Get 错误在 `err==nil` 分支外被忽略，可能视为未配置走旧路径；旧 chat/embedding 必填字段的读取和验证仍先行，Provider块不是完整替代入口。
5. 如 RerankURL 留空，buildRegistry 用 Chat BaseURL+"/reranks"；当 Chat BaseURL 是官方 `/compatible-mode/v1`，得到的路径不符合 qwen3-rerank `/compatible-api/v1/reranks`。目前业务尚未接这一角色；接线修复时须显式处理，不能因为扁平请求体相似而直接拼接。

**最小修正**：明确默认与显式cfg的优先级，按不可变身份取角色；已配置Provider的失败必须传播，不静默切换。旧缓存不能覆盖新Provider。将 RerankRole 注入真实检索调用点，复用旧 rerank 校验内核，避免反向把 Registry 委派塞进旧 NewRerank 导致循环。配置读取错误传播；分角色明确未配置与配置无效的行为。排序 endpoint 单独校验/要求显式配置。

### B5 · P1：Registry 在 RAG 初始化成功前全局发布，失败和Reset后残留配置

**目标位置**：`internal/logic/rag/retriever.go:74`–`:82`；`core/common/chat_model.go:34`；同文件 Reset（原Stage1函数未作对应Registry生命周期扩展）。

**触发与证据**：defaultRagFactory.Create 调用 SetRegistry 后才调用 core.New；任何后续索引检查/构造失败都不会回滚该引用。`TestReviewRegistryPublicationOnInitFailure` 使用真实 ESVectorStore 构造和本机 httptest 的401索引HEAD响应，触发 core.New 已有失败分支，观察到之前 Registry 被失败尝试的新Registry替换并保留。无真实ES访问或索引写入。

**后果**：失败初始化可改变后续 Chat/common 的模型选择，破坏“成功后才发布”契约。Reset 不清除 Registry，后续 Provider=nil 的初始化也不清除它；旧 endpoint/凭据/角色仍可被复用。当前 per-build 新预算还会引入实例切换时的配额刷新问题（B1）。

**最小修正**：Registry/transport 先作为构建上下文或实例依赖，相关服务构建成功后再发布；失败回滚，不影响旧实例。将其纳入明确的生命周期、身份检查和清理所有权；成功Reset后处理关联引用/空闲连接，Shutdown失败时保持仍在使用的依赖。不要重新引入Stage1的持锁等待或超时强关问题。

### B6 · P2：builder注册表没有并发/冻结和校验契约；已复现真实race

**目标位置**：`core/providers/registry.go:221`、`:261`、`:296`、`:340`–`:358`。

**证据**：公开 Register*Builder 无锁写包级 map，构建时无锁读。`TestReviewHooksConcurrentRegistration` 使用公开API、独立provider名和真实Registry构造，在已可用的UCRT64环境执行 `-race`，捕获 mapaccess2_faststr（buildChat:221）与 mapassign_faststr（RegisterChatBuilder:348）的 **WARNING: DATA RACE**，exit 1。

此外，源码允许重复注册覆盖、注册nil后调用nil函数；没有“只能启动期注册”的强制或首次构建后的冻结。`TestReviewUnknownProviderRejected` 则确认未知 Provider 名可以经 buildRegistry/GetChat 成功构造通用OpenAI模型，而不是配置错误。

**最小修正**：为注册/读取使用同一同步机制，并定义只读冻结或不可替换策略；拒绝nil和不合约的重复注册，未知Provider不能静默落入通用协议。保留init仅内存注册的优点，不因为init本身重构框架。新增测试通过真实注册/构造API，不可用可变包级函数替换掩盖竞态。

## 3. 实际生产接线清单

| 角色/阶段 | 本目标真实行为 |
|---|---|
| 配置读取 | readRagConfig 在旧字段之后读取provider块；gvar.Scan能读入本批snake_case字段，已补测确认；ProviderConfig→RagConfig→Config.Copy中Provider深拷贝有实现 |
| Registry构建 | defaultRagFactory.Create 调用 buildRegistry，随后提前写入common全局；每次新建无限ProcessBudget和Transport；凭据从配置/环境解析，但缺失延迟至角色构建 |
| 注册init | logic/rag 显式空导入bailian；cmd导入logic/rag，正常主程序必然执行内存hook注册。init未连接设施/模型。仅直接使用providers而未导入bailian的调用方不会有该hook |
| Chat | core.New→common.GetChatModel(nil)及独立Chat服务factory有Registry分支；旧cache优先/错误回退会破坏选择 |
| Rewrite | core/retriever.go:76→common.GetRewriteModel(nil)，有Registry分支但旧singleton优先 |
| QA | core.New预热及qaFactory→common.GetQAModel(nil)，Registry优先。显式旧cfg仍走旧缓存；未建立任务累计预算 |
| Embedding | 三处组件构造都经common.NewEmbedding；Provider非nil即用当前全局Registry，不核对传入conf的Profile身份；无启用guard |
| Rerank | 真实检索末段仍是旧GetConf/NewRerank；新增Registry角色只在直接构造/测试中使用 |
| HTTP预算 | SDK/Rerank→共享HTTPClient，没有预留、Commit、终结接线；预算实例保存着但不使用 |

**Rerank循环专项结论**：当前 Registry.GetReranker→百炼hook BuildRerank→providers.BuildRerankRole→旧rerank.NewRerank，没有反向Registry调用，未发现循环委派/递归构造。`TestReviewRerankBuilderNoRecursion` 通过真实构造链和本机HTTP通过。不存在重复扣预算，实际是没有扣取。

## 4. 协议与锁定源码核实

核实日期2026-10-19，仅查官方资料/锁定模块，未调用真实模型。详见 SOURCE_EVIDENCE.md。

- 官方 [OpenAI Chat兼容接口](https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope) 支持 compatible-mode 的Chat路径；workspace域名按地域配置，旧dashscope域名仍支持。实现使用显式Profile.Endpoint，Chat路径由锁定SDK追加；不是因使用旧域名或qwen模型名称而判错。
- 官方 [qwen3-rerank](https://help.aliyun.com/en/model-studio/rerank) 明确提供 compatible-api/v1/reranks 和扁平请求体。因此本批选择qwen3-rerank及复用扁平Rerank内核本身成立；DashScope原生接口是另一套嵌套协议，不能混用。
- 官方 [Embedding同步接口](https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api) 的返回含data/index和usage。锁定 acl/openai 转换时丢弃index，是B3的源码依据，不能把统一transport当原始协议验证。
- 原Rerank内核保留index/score必填、非负范围/重复/有限值、结果数<=topN、10MiB响应限制、30s超时、输入隔离和%w错误链；已有包级与adapter测试通过。
- Chat/Embedding非流式body由锁定SDK关闭，流由其reader关闭；百炼包装没有新的裸goroutine。统一client默认60s超时；Chat/Embedding未新增显式响应体大小上限。没有证据证明本批实现了关联lease的body/stream终结或usage保守处理。
- 锁定go-openai单次sendRequest路径调用一次HTTPClient.Do，未看到SDK独立重试循环；transport默认重定向/未来重试仍需纳入B1实际发送边界。没有执行在线模型/区域能力探针，也未确认实际计费用量。
- `%w`保留部分SDK/取消错误链，HTTP状态错误沿SDK类型返回。错误映射靠字符串匹配，不等于已定义稳定Provider错误分类；未观察代码主动打印API key，未以真实凭据测试日志脱敏。

## 5. 干净目标独立实测

CWD：`C:/Zhy/code/go-rag-review-1d3/server`。Go1.27.1 windows/amd64，CGO=1，MSYS2 UCRT64 gcc16.2.0/binutils2.47.20260726；CC/CXX使用该目录绝对路径，GOFLAGS空。仅测试进程临时PATH/CC设置，GOPROXY=off/GOSUMDB=off。不使用online/integration标签，不调用真实DB/ES/Qdrant/模型API。

run-clean.ps1 和各日志记录完整命令、SHA、CWD、时间及真实EXIT；environment.log保存环境；results.json保存统计。

| 命令 | 退出码 | 独立结果 |
|---|---:|---|
| go test ./core/providers/... ./core/common ./core/rerank ./core/config ./internal/logic/rag ./internal/logic/chat -count=1 -timeout=180s -v | 0 | 207顶层/232含子项PASS，6个有测试包；ordinary.log |
| go test -race ./core/providers/... -count=1 -timeout=180s -v | 0 | 67顶层/81含子项PASS，两包；race-providers.log |
| go test -race ./core/common ./core/rerank ./internal/logic/rag ./internal/logic/chat -count=1 -timeout=180s -v | 0 | 140顶层/151含子项PASS，四包；race-wiring.log |
| go test -race ./core/providers/... -run 下列正则 -count=10 -timeout=180s -v | 0 | 16项各10次，160顶层/200含子项PASS；race-key-stress.log |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 15包ok；full.log |
| go vet ./... | 0 | vet.log |
| go build ./... | 0 | build.log |

关键正则（key-test-names.txt核实16项均恰好10次）：

```
^(TestBudget_(ConcurrentReserve_MaxRequests|DualScope_PartialFailure|IdempotentFinalize|ConcurrentFinalize|SlotRelease_NonStream|SlotRelease_StreamPaths)|TestRegistry_(ProfileIsolation|FailedInit|ResolverCannotMutateProfile|ResolverCannotMutateKeyOptions|ResolverHoldsCopy)|TestBuildChat_(NormalResponse|StreamNormal|StreamInterrupted)|TestBuildEmbed_NormalResponse|TestBuildRerank_NormalResponse)$
```

这是目标已有测试在其覆盖范围内通过，不证明缺失的预算/生产路径正确。提交中没有注册并发测试；补充层已单独覆盖并发现race。

## 6. 独立补充验证（不属于目标自带测试）

clean全部命令完成后新增5文件，副本与完整新增diff在本目录repros/；repro-hashes.txt保存SHA256；最终状态见final-worktree-status.txt。没有复制实现者未提交测试，也没有改生产代码使其通过。

| 测试文件及命中 | 结果与证据 |
|---|---|
| core/providers/bailian/review_protocol_test.go：RegistryBudgetCoversHTTP、EmbeddingRawIndices（3子项）、RerankBuilderNoRecursion | 预算/index复现失败；无递归成功；reproduction.log |
| core/common/review_common_test.go：LegacyCacheDoesNotOverrideRegistry | 失败，旧cache覆盖新Registry |
| internal/logic/rag/review_rag_test.go：EmbeddingProductionGate、RegistryPublicationOnInitFailure、UnknownProviderRejected | 3项失败，分别证明无门禁、提前发布、未知Provider回退 |
| internal/logic/chat/review_chat_test.go：ChatFactoryPropagatesRegistryFailure | 失败，吞掉可识别错误并回退 |
| core/providers/review_hooks_race_test.go：HooksConcurrentRegistration | 单轮race exit1，真实DATA RACE；reproduction-hooks-race.log |
| 同文件：RegistryConcurrentConstruction | 十轮race exit0，32并发共享同一实例、凭据解析一次；supplement-construction-race.log |

完整命令：

```
go test ./core/providers/bailian ./core/common ./internal/logic/rag ./internal/logic/chat -run ^TestReview -count=1 -timeout=60s -v
# EXIT 1：8顶层中7项失败、无递归1项通过；EmbeddingRawIndices含3失败子项。
go test -race ./core/providers -run ^TestReviewHooksConcurrentRegistration$ -count=1 -timeout=60s -v
# EXIT 1：注册/构建map读写竞争，非环境异常。
go test -race ./core/providers -run ^TestReviewRegistryConcurrentConstruction$ -count=10 -timeout=60s -v
# EXIT 0：10次实际命中。
```

所有复现都经生产API/共享实现；注入仅用公开Registry接口、SetRegistry或公开注册API，未替换生产包级函数。注册用私有provider名，不覆盖生产bailian hook。补充测试不与干净目标原有测试混跑；无外部设施连接，ES失败仅httptest本机401响应。

## 7. 实现者日志真实性与非阻塞建议

- 本批提交31个bailian顶层测试；providers顶层实际36个。报告将providers写31、全量写16不准确；本轮实测分别67顶层总计、15包全量。这些统计问题不另立阻塞。
- 提交的race日志实际是旧环境0xc0000139退出1，不能支持“该提交providers race成功”；本轮已用授权UCRT64独立补齐干净范围，但补测仍捕获真实hook竞态。旧环境根因继续待确认，不按其报告的确定平台解释免除代码审查。
- Chat_UsageMissing仅断言仍有内容，Embed_UsageMissing仅断言向量数量，不检查token保留/slot释放。
- openAISSEChunks每次都会追加[DONE]；StreamInterrupted也调用此helper，因此没有真正模拟缺少[DONE]的异常断流，且断言不区分EOF/错误，只验证不panic。StreamNormal只要求非空。它们不能作为实际流终结契约证据。
- NaN/Inf若直接测试共享validator是有效的数值单元验证，但创建未使用的httptest server不代表跑过原始HTTP解析。Rerank_Timeout30s实际验证1s调用方超时，server固定等待5s；不等于独立测得30s默认上限，源码可核实该上限。
- 后续测试应把最小真实业务装配、服务器实际计数、raw metadata解析和budget账本连起来，不增加仅复制编排的fake测试。

## 8. 2C/2D与未验证边界

本次不要求完成维度硬编码迁移、fingerprint持久化或旧索引迁移；要求的是新Embedding在guard之前保持生产禁用。实际区域/模型在线行为、真实usage、服务中断、A-4缺QA向量、ES启动兼容、SQL driver取消及付费探针均未验证，不写成通过，不连接设施补验。

R1/R2和Stage2A既有验收保持有效；本轮问题属于2B接线/解析/注册新变更，不宣布整个Stage2完成。完成集中报告后停止，等待新的完整修复SHA。
