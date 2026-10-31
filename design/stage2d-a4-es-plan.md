# Stage 2D A-4 / P-8 真实 Elasticsearch 验证方案（v0.4，待授权，未执行）

> 本文只制定方案：不连接任何真实 ES、不调用模型、不创建/写入/删除资源、不提交推送、不更新验收状态。
> 修订依据：`.../stage2d-a4-plan-review/REPORT.md`（v0.1→v0.2）；`.../stage2d-a4-v02-review/REPORT.md`（v0.2→v0.3）；`.../stage2d-a4-v03-review/REPORT.md`（v0.3→v0.4：S 类优先于 4xx 预期、`_alias`/目标 404 区分、5xx 可如实记录但不得归因、结果表因果->可能解释、未授权向量时阻塞全部依赖步骤）。
> **v0.5 状态注记（2026-10-31，仅状态，不改步骤/授权/预算数值）**：L2 组件层已于 2026-10-31 由用户在单独授权下执行一次（1 次只读 UUID GET + ≤4 条组件请求；真实本机 ES 8.11.3 + 本机 fake embedding；未创建/写入/删除、未调用模型），证据 `verification/stage2d-a4-l2-38142d0-20261031/REPORT.md`，离线入口与约束见 `design/stage2d-a4-l2-component.md`。L0 证据为 `verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md`。**L3 仍不纳入、未执行**；本版不更新 A-4 与 Stage 2D 验收状态。
> 依据对象：`verification/stage2d-snapshot-fix10-v14r1-38142d0-20261031/`（41 项自校验通过；`design/stage2d-plan.md` = `68A5F6CC…`，93933 B），基线 `38142d05d25e762e7058865213b9e667dee980db`。
> 离线骨架：`server/core/retriever/a4_query_shape_test.go`（本机 httptest，4 个用例）。

## 0. 本轮结论（供快速审阅）

1. **A-4 的验收口径是“记录行为”，不是“必须报错”**：`design/stage2-plan.md` §9.1 的 2D 行要求“A-4 QA 向量行为记录”，§8.8 只把“报错？空结果？”列为待确认。因此方案不预设 ES 的返回形态。
2. **项目对 ES 的向量检索不是原生 `knn`**，而是 `script_score` + `cosineSimilarity(params.embedding, 'qa_content_vector') + 1.0`、`match_all`/`bool.filter`、`size=TopK(=50)`。§8.8 中的“kNN”属概括性措辞，实施时必须按实际 DSL 构造等价请求。
3. **mapping 缺 `qa_content_vector` 的索引，在项目里到不了 ES 查询**：初始化绑定与每次检索入口都会先做 fingerprint/schema 校验（缺字段即报 `required vector field "qa_content_vector" missing`），因此“经过项目检索器”的可观测结果是门禁错误，而不是 ES 返回值。
4. **真正可能在生产中暴露的变体是“字段存在、文档无值”**（QA 关闭时异步索引不写 qa 向量）：此时查询会真实发出，且融合环节只按名次计分、不检查 `_shards`/`timed_out`，值得与 A-4 一并取证。
5. 上述 3、4 目前只有**静态代码依据**与**本机 fake 证据**；真实 ES 的两类行为（缺字段 / 无值）**均未验证**。
6. **响应默认不落盘**（v0.2 引入，v0.3 收紧）：R-6/R-7/R-8/R-9 一律加 `"_source": false`，只保留 §5.0 白名单摘要；**`error.reason` 默认不记录原文**（截断不能保证脱敏）。该形状与生产响应（无 `_source` 过滤）**不完全相同**，生产解析前提另由可选 R-6P 取证。
7. **两处例外必须单独授权**（v0.3 引入，v0.4 收紧）：R-5 在内存读 1 条 `content_vector`；R-6P 在本机**接收 1 条完整 `_source`（含正文与全部值）**，仅提取键名后丢弃，且只代表该 1 条样本。**未授权 R-5 的向量读取时，所有依赖该向量的查询（R-6/R-6P/R-7/R-8/R-9）均不能执行**，本轮止于 R-4，**不得**改用其他索引、其他文档或其他向量；R-6P 单独不授权只影响该步。

## 1. 验收口径与引用位置（只读核对）

| 文档 | 位置 | 要点 |
|---|---|---|
| `design/stage2-plan.md` | §8.8（781–785） | “QA 关闭时 `qa_content_vector` 字段在 ES mapping 中可能缺失；检索器 `retrieveDoOnce` 中 `x.retrieve(ctx, req, true)` 使用该字段做 kNN”，行为待探针确认 |
| `design/stage2-plan.md` | §9.1 2D 行（L804） | 2D 验收条件含“A-4 QA 向量行为记录”；回滚边界“不影响离线代码（探针为只读验证）” |
| `design/stage2d-plan.md` | §2 P-8（L89） | “不属于本次 S-1，但为原计划完整 2D 验收条件……**尚未执行或授权**”；其**通过标准**是“行为被观测并记录（报错/空结果/其他），与检索器错误路径一致” |
| `design/stage2d-plan.md` | §5.2（L270–271） | P-8/A-4 属阶段 2，**不随 S-1 自动授权** |
| `design/stage2d-plan.md` | §9 表（L359）、§9.3（L509） | 真实 ES/Qdrant/MySQL 未验证；P-8/A-4 仍是完整 2D 验收条件、不改为“可选” |

结论口径：本方案只服务于 A-4 的“行为记录 + 项目侧对应”，**不构成 2D 验收通过**，也不替代 E-1/E-2/I-1/I-2 与 D-7。

授权口径对齐：`design/stage2d-plan.md` §5.2（L271）允许“本阶段单独执行只读 A-4”，条件是“在授权文本中明确‘不写入’及其独立前提”（此时 D-1b 不适用，**A-4 原验收要求不作削减**），且 P-8/A-4“不与付费模型调用混排为同一授权”。本方案正对应这一“单独只读”分支：授权文本必须写明不写入、不创建、不删除，且不请求任何模型调用。

## 2. 静态代码事实（本轮只读核对，未运行）

| # | 事实 | 证据（file:line） |
|---|---|---|
| F1 | 每轮检索固定两路向量：先 `content_vector`，再 `qa_content_vector`；任一路失败即整体返回错误 | `core/retriever.go:249-260` |
| F2 | QA 检索器**无条件构建**（与 `QAEnabled` 无关），使用 `RetrieverFieldKey=qa_content_vector` | `core/rag.go:129-154`、`core/retriever/retriever.go:20-23` |
| F3 | ES 检索模式 = `SearchModeDenseVectorSimilarity(cosineSimilarity, vectorField)`；请求体为 `script_score`，**不是** `knn` 字段 | `core/retriever/retriever.go:33-43`；模块 `eino-ext/.../es8/search_mode/dense_vector_similarity.go:71-95` |
| F4 | 脚本常量：`cosineSimilarity(params.embedding, '<field>') + 1.0`；`size=TopK`；`min_score` 仅在有阈值时设置（项目未设） | 同上 `dense_vector_similarity.go:107-108`、`:90-93` |
| F5 | 过滤条件：`bool.filter[0].bool.must[0].match._knowledge_name`（字段名带下划线） | `core/retriever.go:327-345`、`core/types/consts.go:10` |
| F6 | 检索 `TopK` 常量 50 | `core/rag.go:36`、`core/retriever.go:320-322` |
| F7 | 响应解析白名单仅 `content/content_vector/qa_content_vector/qa_content/ext/_knowledge_name`；其他字段 → 直接报错 | `core/retriever/retriever.go:65-99` |
| F8 | 项目**不检查** `_shards.failed` / `timed_out` / `took`（全仓非 test 无引用）；命中数即为结果 | 模块 `es8/retriever.go:134-147` |
| F9 | 融合只按名次：`1/(60+rank)`，**不使用 ES 分值** | `core/retriever/rrf.go:9-12`（`RRFFusion`，`k = 60`） |
| F10 | 初始化顺序：`IndexExists` →（不存在则 `CreateIndex`）→ `BindPhysicalTarget`（`ReadFingerprint`+`VerifyFingerprint`）→ `core.New` → `VerifyFingerprint` | `internal/logic/rag/retriever.go:127-172` |
| F11 | `ReadFingerprint` 要求：目标解析为**恰好一个具体索引**、`mappings._meta["go-rag-fingerprint"]` 存在、`content_vector` 与 `qa_content_vector` 均存在且 dims/similarity 严格匹配 | `core/vector/es.go:419-517`、`core/vector/schema_validation.go:17-66` |
| F12 | 每次 `Rag.Retrieve` 前重新读取并校验 fingerprint（不缓存） | `core/retriever.go:64-67`、`core/rag.go:304-338` |
| F13 | 索引"受控生命周期"必须声明为 true，且必须有 `VectorIOGuard`，否则 `core.New` 拒绝 | `core/rag.go:110-115`、`internal/logic/rag/managed_lifecycle.go:11-22` |
| F14 | 项目自建 mapping **总是**包含两个向量字段（同为配置维度、cosine、`index:true`）；故“缺字段”只可能来自外部/旧版本索引 | `core/config/vector_schema.go:10-30` |
| F15 | ES 客户端仅支持单个 `Address` + 用户名/密码，无 CA/TLS/APIKey 选项 | `core/vector/es.go:41-45`、`core/vector/interface.go:70-74` |
| F16 | 版本组合：客户端 `go-elasticsearch v8.16.0`；compose `elasticsearch:8.11.3`；README `8.18.0` | `server/go.mod`、`docker-compose.yml:31`、`README.md:86` |

**由 F10–F13 推出的关键判断（静态）**：mapping 缺 `qa_content_vector` 的索引，在应用侧会在**初始化**或**每次检索前**被 fingerprint/schema 门禁拒绝；因此“应用对缺字段索引的检索行为”在现有代码里不可达，可观测的是门禁错误文案与错误链。若要在真实 ES 上观察**检索器组件**对该索引的行为，必须绕过门禁（组件级工具或测试骨架），这属于另一层取证，不能与“应用全链已验证”混写。

## 3. 覆盖范围矩阵（三层必须分开写证据）

| 层 | 内容 | 本方案是否纳入 | 能证明 | 不能证明 |
|---|---|---|---|---|
| **L0 直接 ES（等价 DSL）** | 用与 F3–F6 一致的 `script_score` 请求直接访问目标索引；R-7/R-8/R-9 为**诊断 DSL**，需前置或限定 | **纳入（只读，用户执行）** | 真实 ES 在“字段缺失 / 文档无值 / 维度不匹配”下的状态码、错误类型、分值与命中（无值需满足 R-8 前提） | 项目如何映射错误、如何融合、是否 fail-closed |
| **L1 项目门禁** | 初始化与检索入口的 fingerprint/schema 校验 | 只做**静态+离线 fake**取证（`schema_validation_test.go:26`、`core/vector/fingerprint_es_test.go`）；不联网 | 缺字段索引被拒绝这一行为与错误文案 | 真实 ES `GET <index>` 响应字段差异（如 uuid/别名形态）对门禁的影响 |
| **L2 检索器组件（stub 向量 + 真实 ES）** | 绕开 fingerprint/schema 门禁，用固定向量跑同一 `newRetriever` 构造 | **本方案不纳入**；后续已单独授权执行一次（见 v0.5 注记），执行约束由 `design/stage2d-a4-l2-component.md` 规定 | 见 v0.5 注记的 L2 报告 | 同右：任何“项目会返回 0 条 / 参与 RRF”的说法，在**受控入口上不可达**；L2 结论不能外推为应用全链 |
| **L3 应用全链（`core.Rag.Retrieve`）** | 含真实 embedding 调用与独占生命周期声明 | **不纳入**（需模型调用授权；共享测试索引不满足 F13 的独占陈述） | — | 同 L2；且缺字段索引在入口即被门禁拒绝 |

**硬约束**（三条，缺一即视为混层）：
1. L0 的 HTTP 返回**不得**被表述为“项目错误处理已验证”。
2. L1 的离线结论**不得**被表述为“真实 ES 行为已验证”。
3. **L2/L3 未执行时，任何“应用/检索器会怎样”的陈述只能标为假设**，不得写成受控应用的实际行为；且 **L0 + L1 静态不构成** P-8 的“与检索器错误路径一致”（缺字段索引在受控入口根本走不到检索器）。L2 已执行（v0.5）后，其结论仍**限组件层**：只说明该组件对真实 ES 响应的返回/报错/响应体释放行为，不构成 L3，也不解除本条对 L0/L1 的限制。
4. **目标唯一性**（v0.3）：L0 只能在“已确认的单一具体物理索引”上取证。一旦发现别名、通配、多目标或无法确认具体物理索引，本轮**终止且不改查其他目标**；更换范围需另行授权（§5.1 Q 类）。

## 4. 需要用户提供的最少信息（待确认清单）

| 项 | 是否必需 | 说明 / 形式 |
|---|---|---|
| ES base URL | 必需 | 形如 `http://host:9200` 或 `https://host:9200`；若为自签 HTTPS，需说明（项目客户端无 CA 覆盖，见 F15） |
| ES 版本 | 必需 | 执行 `GET /` 得到 `version.number`（用于判断 8.x 行为差异，见 F16） |
| 认证方式 | 必需 | 是否存在 basic auth（用户名/密码）；是否需要 API key（项目不支持，仅影响 L2/L3） |
| 只读账号权限 | 必需 | 建议最小集：目标索引上 `read` + `view_index_metadata`，集群级 `monitor`；**不要** `manage` / `create_index` / `delete_index` / `write` |
| 目标索引名 | 必需 | 已存在、已授权、**具体物理索引**（非别名、非通配），且 `^[a-z0-9][a-z0-9._-]{0,254}$`（`core/vector/target.go:39-44`） |
| 一个已存在的 `_knowledge_name` 值 | 必需 | 用于构造与 F5 一致的过滤。建议由用户直接提供；若改为从索引采样（`size:0` + `terms` 聚合），该请求**计入 §10 的 `_search` 上限**并挤占诊断步骤 |
| 索引来源 | 建议 | 是否由本项目创建（决定是否带 `_meta.go-rag-fingerprint`），以及创建时 QA 是否开启 |
| 数据敏感性 | 必需 | 分两项：① 是否允许**在内存中**读取 1 条 `content_vector`（仅用于 R-6 查询向量，只回传 `len` 与 `sha8`）；② 是否允许 **R-6P** 在本地**接收 1 条命中的完整 `_source`（含正文、向量与全部字段值）**，仅提取键名后丢弃。两项均默认**不落盘、不回传**原始值。未同意 ① → **BLOCK**：R-5 及 R-6/R-6P/R-7/R-8/R-9 均不可执行（止于 R-4，不得改用其他索引/文档/向量）；未同意 ② → 仅 **SKIP** R-6P（见 §5.0/§5.1） |

**凭据规则（本方案硬性要求）**：真实凭据不得写入仓库、文档、脚本、日志或对话；不得读取本机凭据文件。建议由用户在自己的终端以环境变量注入、命令历史禁用敏感值，或直接由用户执行、只回传脱敏响应。本方案中的命令一律使用 `$ES_URL` / `$ES_USER` / `$ES_PASS` 占位。

## 5. 只读验证步骤（L0，用户执行；每步只记录白名单摘要）

> 全部为只读：`GET` 与 `_search`/`_count`。若目标索引无权限或出现任何写入迹象，立即停止并记录。

### 5.0 响应处理与记录字段（默认不落盘、不回传原始数据）

本节对 R-1…R-9 全部有效，优先于各步骤的具体描述。

1. **默认只保留白名单摘要**：原始响应体（尤其 `_source`）不得在仓库、日志、报告或对话中落盘或回传。
2. **白名单字段字典**（只有下表字段可写入证据；**未列出的字段一律不记录**；各步骤实际要求记录的项均已在表中）：

| 类别 | 允许记录的字段 | 使用步骤 |
|---|---|---|
| 集群 | `version.number`、`version.build_flavor`、`tagline`、`cluster_name`（脱敏为 `<cluster>`） | R-1、§11 |
| 目标 | 解析出的索引键数、是否恰好等于 `<index>`（布尔）、是否别名（布尔）、`settings.index.uuid` 前 8 位、`mappings._meta` 是否含 `go-rag-fingerprint`（布尔） | R-2、§11 |
| mapping | 字段是否存在（布尔）、`type`、`dims`、`similarity`、`index`、探测字段识别到的字段数（0/n） | R-3、R-4、R-7 前置、§11 |
| 计数 | 候选总数、无值数、有值数、`总数 = 无值数 + 有值数` 校验（布尔） | R-5、§11 |
| 向量 | 维度长度（`len`）、`sha256`（对 JSON 数组字节）前 8 位 | R-5、§11 |
| 响应 | `status`、`error.type`、`error.root_cause[].type`、`took`、`timed_out`、`_shards.total/successful/skipped/failed`、`hits.total.value`、`hits.total.relation`、命中数、`hits.max_score`（3 位小数）、前 3 条 `_score`（3 位小数） | R-6、R-6P、R-7、R-8、R-9、§11 |
| 命中 | `_id` 前 4 位；`_source` 是否存在（布尔）；`_source` 键数；是否存在白名单外键（布尔）；键名集合（仅当键名不含个人信息） | R-5、R-6P、§11 |
| 流程 | `query_kind`（等价/诊断）、步骤编号、停止点、请求计数、是否获 C6 授权 | §10、§11 |

   **`error.reason` 默认不记录原文**：截断无法保证脱敏（reason 可能包含文档值、索引名、字段内容或查询片段）。只记录 `error.type` 与 `error.root_cause[].type`；如需额外说明，只允许写“是否属字段缺失/参数错误类”（布尔，由执行人判断）。
3. **明确禁止回传**：`_source` 任何值、向量任一分量、正文、metadata、完整 `_id`、完整 uuid、账号名与任何凭据。
4. **禁止整段重定向响应**：在同一命令/进程内完成“取值 → 计算摘要 → 打印摘要”，不得把 HTTP 响应用 `>` 直接写入文件；若终端必须暂存，先脱敏再落盘，并在报告中记录“暂存文件已删除”。
5. **`_source:false` 的用途与差别**（关键）：
   - 可用于 R-6/R-7/R-8/R-9 的**评分与错误形态**取证，从源头避免带出正文/向量/元数据。
   - 但项目生产请求**不带** `_source` 过滤（F7 的白名单解析依赖整份 `_source`）。因此 `_source:false` 版本**不覆盖**生产解析路径，响应形状与生产响应**不完全相同**，报告中必须逐条标注。
   - 需要覆盖“生产解析前提”时，另做 **R-6P**；该步骤会让本地**接收 1 条命中的完整 `_source`（含正文与其他全部值）**，只是在提取键名后丢弃，必须单独获得 C6 同意，否则跳过。
6. **两处例外（证据中必须写明“已在内存接收原始值”）**：
   - **R-5**：在内存中读取 1 条 `content_vector` 作为查询向量；只回传 `len` 与 `sha256` 前 8 位。**未获授权时不得以任何替代方式伪造/借用向量**，R-6/R-6P/R-7/R-8/R-9 一并不可执行（见 §5.1 BLOCK）。
   - **R-6P**：在内存中接收 1 条完整 `_source`；只回传键数/键名集合/白名单外键布尔值。
   - 两处均不得把原始值写入任何文件、日志或对话，处理完立即丢弃。

### 5.1 判定优先级、逐步骤预期、停止规则与计数

每条请求**只发一次，禁止自动重试**（不得用循环、重试脚本或幂等重放）。

**判定优先级（顺序不可颠倒；S 优先于一切“4xx 可继续”的表述）**：

| 顺序 | 判据 | 处理 |
|---|---|---|
| 1（最先） | **S 类**：401、403、408、429、**任何 5xx**、超时、连接失败、非 JSON/无法解析、写入迹象 | 记录安全摘要后**立即终止**；**不得**被任何步骤的“4xx 预期”覆盖 |
| 2 | **Q 类**：可解析但不属该步白名单（别名/多目标、字段数不符、等式不成立等） | 记录，**终止本轮**，不改查 |
| 3（最后） | **P 类**：仅限该步**白名单内**的 200 或 4xx | 记录摘要，**继续**下一步 |

**4xx 归 P 的三条门槛（全部满足，否则归 S 或 Q）**：① 状态码**不是** 401/403/408/429；② 响应可解析为 JSON 且含 `error` 结构；③ `error.type`（或 `root_cause[].type`）**落在该步白名单内**。**不得仅凭状态码**把任意 4xx 归因为“字段缺失”“维度不匹配”或“未映射”。

| 步骤 | P（200） | 4xx 白名单（须匹配 `error.type`） | Q（→终止，不改查） | S（→终止，优先于 P） |
|---|---|---|---|---|
| R-1 | 200 且 `version.number` 可读 | 无 | 非 Elasticsearch 实现（OpenSearch 等）→ 记录后终止 | 401/403/408/429、5xx、超时、非 JSON |
| R-2 | `GET <index>` 200 且顶层键恰为 `<index>`、`aliases` 可读 | **`_alias` 404 仅在请求 2 已 200 且 `aliases` 为空时**可作“无别名”辅证；其他 404 不可判读 | 别名/通配/多键/键名不等/无法确认唯一索引 | 403/408/429、5xx、超时；`GET <index>` 的 404 |
| R-3 | 200（含字段不存在时的空 mapping） | 无 | 字段存在但 `dims` 缺失或 `similarity` 非 cosine → 记录后终止 | 404/403/408/429、5xx |
| R-4 | 与本地配置可比对 | 无 | 维度不一致 → 记录后终止 | — |
| R-5 | 200 | 无 | 计数等式不成立、候选为 0、无 `content_vector` → 终止 | 5xx、超时、401/403/408/429 |
| R-6 | 200（含 E 的分片/超时标志） | `search_phase_execution_exception`、`illegal_argument_exception`、`query_shard_exception`（可在 `root_cause`） | — | 401/403/408/429、5xx、超时 |
| R-6P | 200 且有 ≥1 命中 | 同 R-6（结果仅记“未取证”） | 200 + 0 命中 → “未取证”，跳过后续可选步骤 | 同上 |
| R-7 | 200（未映射字段被拒） | 同 R-6 | — | 同上 |
| R-8 | 200 | 同 R-6 | — | 同上 |
| R-9 | 200 | `illegal_argument_exception`（维度不匹配） | — | 同上（5xx 时**不做**正确长度复核） |

- **SKIP（可跳过，不阻塞）**：未获**该步专属**授权或前提不满足时，不发请求、标“未取证”、不计数，继续不受影响的步骤。仅适用于：R-6P 未获 C6-②（`_source` 接收）、R-8 前提不满足、R-7 前置字段数非 0。
- **BLOCK（阻塞型，终止依赖链）**：**未获 R-5 的 C6-① 向量读取授权**时，R-5 本身及**全部依赖向量的查询（R-6/R-6P/R-7/R-8/R-9）不可执行**，本轮止于 R-4 并终止；**不得**改用其他索引、其他文档、其他字段或凭空构造的向量。若“前置失败”源于 404/403/5xx 等不可判读状态，归 **S 类终止**。
- **计数**：每条实际发出的请求各计 1 次（含诊断步骤、含 R-9 的复核、含返回错误的请求）；未发出（SKIP/BLOCK）的不计数；计入 §7.1/§10 的既定上限（GET ≤6、`_count` ≤3、`_search` ≤7，合计 ≤16），**上限不因重试或补测而提高**。
- **5xx 的记录与归因边界**：5xx 属 S 类，但**必须如实记录**“该请求确已发出、时刻、`status` 与可解析的 `error.type`”，并在证据中标注“**原因未定**”“本次观察不完整”；**不得**仅凭状态码认定原因是“字段缺失/维度不匹配”，不得映射为 §6 的 A/B/C/D/E 任一行，也不得据此声称 A-4 通过或完成。终止是本方案的**保守选择**（会导致该步观察不完整），用户可另行决定是否调整范围，但**不得自动重试**。
- **缺失样本**：R-6P/R-8 若因无样本或前提不满足而跳过，报告中必须写“未取证”，且**不得**在产品结论中引用其预期值。

### R-1 连通与版本
- 请求：`GET $ES_URL/`
- 记录：`version.number`、`version.build_flavor`、`cluster_name`（可脱敏为 `<cluster>`）、`tagline`
- 判定线索：8.x 且 `tagline` 为 `You Know, for Search`；若为 OpenSearch/兼容实现需单独标注（不得按 ES 结论写）

### R-2 确认目标是具体物理索引（对齐 F11）
- 请求 1（交叉验证）：`GET $ES_URL/_alias/<index>`
- 请求 2（**主判据**）：`GET $ES_URL/<index>?filter_path=*.settings.index.uuid,*.settings.index.creation_date,*.aliases,*.mappings._meta`
- 记录：请求 2 的**顶层键**是否恰好为 `<index>`、`aliases` 是否为空；`settings.index.uuid` 前 8 位；`mappings._meta` 是否含 `go-rag-fingerprint`；请求 1 的状态码与（若可解析）`error.type`
- 判定（必须**联合**，不得单凭任一 404 下结论）：
  1. **主判据**：请求 2 返回 200、顶层键恰好为 `<index>`、`aliases` 为 `{}` → 可记“该名指向具体物理索引、当前无别名指向”，继续。
  2. 请求 2 返回 200 但键多于/不等于 `<index>` 或 `aliases` 非空 → 别名/通配/多目标 → **Q 类终止**，不改查。
  3. **请求 1 的 404 不能单独证明“无别名”**：索引不存在、权限受限或路径错误也可能返回 404（可能含 `index_not_found_exception`）。仅当请求 2 已 200 且 `aliases` 为空时，请求 1 的 404 才可作为“无别名”辅证；否则只记“不可判读”。
  4. 请求 1/2 出现 403/401/408/429/5xx/超时/非 JSON → **S 类终止**；请求 2 的 404 也属 S（目标不存在或不可见），**不得**据此推断“字段缺失”或“别名不存在”。
  5. 该项目路径对非具体索引本就不接受（`ErrPhysicalTargetRequired`），该静态事实只用于 L1 解释，不构成继续查询其他目标的理由。
- 不记录：uuid 完整值（只留前 8 位）；不记录别名指向的索引名

### R-3 字段存在性与维度
- 请求 1：`GET $ES_URL/<index>/_mapping/field/qa_content_vector`
- 请求 2：`GET $ES_URL/<index>/_mapping/field/content_vector`
- 记录：字段是否存在；`type`、`dims`、`similarity`、`index`
- 判定线索：
  - 两者都存在且 `dims` 相同 → 本索引**不满足** A-4 的“mapping 缺字段”前提，R-6 的“缺字段”只能用 R-7 近似或按 §7 另建临时索引
  - `qa_content_vector` 缺失 → 直接满足 A-4 前提，继续 R-5/R-6
  - 请求返回 404/403**不等于**“字段缺失”：404 表示索引不可见/不存在（或路径错误），403 可能掩盖真实存在性 → 均属 S 类，**终止本轮**，不得写“字段未映射”

### R-4 维度单源对照
- 记录：`dims`（来自 R-3）与项目配置 `EmbeddingDimensions`（`design`/配置或 `server/manifest/config/*.yaml`，只读）
- 判定线索：二者不一致时，项目的 fingerprint 门禁本就会拒绝（F11），A-4 结论需注明“该索引在项目侧不可用”

### R-5 候选集与取样向量（避免“空结果假象”）
- 请求 1（计数）：`POST $ES_URL/<index>/_count`，body `{"query":{"match":{"_knowledge_name":"<kb>"}}}`
- 请求 2（取样向量，**全响应不得落盘**）：`POST $ES_URL/<index>/_search`，body
  ```json
  {"size":1,"_source":["content_vector"],"query":{"match":{"_knowledge_name":"<kb>"}},"sort":["_doc"]}
  ```
- 请求 3（计数，R-8 前提；两条各一次）：
  - 无值：`{"query":{"bool":{"must":[{"match":{"_knowledge_name":"<kb>"}}],"must_not":[{"exists":{"field":"qa_content_vector"}}]}}}`
  - 有值：`{"query":{"bool":{"must":[{"match":{"_knowledge_name":"<kb>"}},{"exists":{"field":"qa_content_vector"}}]}}}`
- 记录（**只按 §5.0 白名单**）：候选总数、无值数、有值数（应满足：总数 = 无值数 + 有值数）；样例 `_id` 前 4 位；`content_vector` 的**长度**与 `sha256` 前 8 位
- 判定线索：
  - 候选总数为 0 → R-6 的“0 命中”不可区分“字段缺失”与“无候选”，本轮**显式标注无效并终止**；更换索引需另行授权，**不得**自动改用其他索引
  - 无值数 = 候选总数 → 可对**完整候选集**执行 R-8（满足“全候选无值”）
  - 0 < 无值数 < 候选总数 → 混合数据，R-8 **必须**用 `must_not exists` 限定（见 R-8）
  - **注意**：若 R-3 显示 `qa_content_vector` 未映射，`exists` 查询恒为 0，请求 3 的“无值数”等于全体，不能当作“字段存在但无值”（R-8 前提 1 因此不可省）
- **BLOCK 前置**：请求 2 需 C6-① 授权（内存读 1 条 `content_vector`）。未获授权 → R-5 及 R-6/R-6P/R-7/R-8/R-9 均不可执行，本轮止于 R-4；**不得**改用其他索引、文档或凭空构造的向量（§5.1 BLOCK）
- 隐私：请求 2 的 `_source` 仅指定 `content_vector`，因此不接收 `content` 正文；向量只回传长度与 sha8（§5.0 第 6 条）

### R-6 等价查询（＝项目实际 DSL）
- 请求：`POST $ES_URL/<index>/_search`，body 如下（向量取 R-5 的 `content_vector`，元素个数与 `dims` 相同）
  ```json
  {
    "size": 50,
    "query": {
      "script_score": {
        "script": {
          "source": "cosineSimilarity(params.embedding, 'qa_content_vector') + 1.0",
          "params": { "embedding": [<R-5 的向量>] }
        },
        "query": {
          "bool": {
            "filter": [
              { "bool": { "must": [ { "match": { "_knowledge_name": { "query": "<kb>" } } } ] } }
            ]
          }
        }
      }
    }
  }
  ```
- 建议 body 增加 `"_source": false`（避免带出原始 `_source`；与生产响应的差别见 §5.0 第 5 条）
- 记录（**只按 §5.0 白名单**）：HTTP 状态、`error.type`、`error.root_cause[].type`、`took`、`timed_out`、`_shards`（total/successful/skipped/failed）、`hits.total.value`、`hits.total.relation`、命中数、`hits.max_score` 与前 3 条 `_score`、`_id` 前 4 位。**不记录 `error.reason` 原文**（§5.0 第 2 条）。
- 说明（与生产的差别，必须写进证据）：
  - 本步覆盖**查询形状 + 评分 + 错误形态**；`_source:false` 使响应形状与生产响应（无 `_source` 过滤）**不完全相同**，故本步不覆盖 F7 的解析路径。
  - 若目标索引文档含白名单外字段，项目侧解析会先失败，这属独立发现；判断该项需 R-6P。
- 判定线索：见 §6 表；本步只给 L0 结论。按 §5.1：200 对应 B/C/D/E；**仅当 4xx 同时满足“非 401/403/408/429 + 可解析 + `error.type` 在白名单内”**时归 A 行；其他 4xx 归 S/Q；**任何 5xx 按 S 终止，但必须如实记录该请求确已发出、时刻、status 与可解析的 error.type，并标“原因未定/观察不完整”**，不得仅凭状态码认定原因，也不得据此声称 A-4 通过。

### R-6P（可选）“生产解析前提”取证（**会在本地接收完整 `_source`**，需单独授权）
- **实际行为（必须如实告知用户）**：本步不使用 `_source:false`，因此响应体会包含 **1 条命中的完整 `_source`——包括正文 `content`、向量数组、`ext` 及其他全部字段值**，这些值会进入本地进程内存；只是在提取键名后立即丢弃。**不得**描述为“只读取键名”。
- **前提**：用户在 §9 C6 **单独、明确**同意上述行为；未获同意 → **跳过**，报告中写“生产解析前提未取证（未获 `_source` 接收授权）”。
- 请求：同 R-6 的 query，`"size":1`，**不加** `_source:false`。
- 处理：仅在本地进程内提取 `_source` 的**键名集合**（排序），与 F7 白名单（`content`、`content_vector`、`qa_content_vector`、`qa_content`、`ext`、`_knowledge_name`）比对，随后立即丢弃响应体与全部值。
- 记录（仅 §5.0 白名单）：键数、是否存在白名单外键（布尔）、键名集合（仅当键名不含个人信息时）；以及 `status`/`error.type`/命中数。
- 禁止：记录任何键值；不得把响应体写入文件、日志、剪贴板或对话；若键名本身可能含个人信息，则只记“键数 + 是否全部在白名单内”。
- **失败与无样本边界**：若返回**白名单内 4xx**（如 `search_phase_execution_exception`）或 0 命中，则**根本拿不到键名**，只能写“未取证（无样本/查询失败）”；**不得**据此推断“整索引不会解析失败”——缺字段场景下的报错响应本身不含命中。非白名单 4xx 或任何 5xx → 按 §5.1 归 S 终止（仍需如实记录 status 与 error.type）。
- **样本边界**：`size:1` 只代表**该 1 条文档**。即使其键全部在白名单内，也只能写“该样本的解析前提成立”，**不能**外推为整索引/全部候选。
- 分层：本步只回答“该样本的解析前提”，**不**证明项目门禁路径（L1），也不证明检索器行为（L2/L3）。

### R-7 缺字段近似（只读替代，不等于 A-4 定论）
- **前置（三条全部满足才可执行 R-7）**：
  1. R-2 已确认目标为**唯一具体物理索引**，且 `GET <index>` 返回 200 且可解析；
  2. `GET $ES_URL/<index>/_mapping/field/a4_probe_absent_field` 返回 **200 且识别到的字段数为 0**（空 mapping）——这才是“未映射”的可判读证据；
  3. 该响应中无 `error` 字段、无权限掩盖迹象（403 一律视为不可判读）。
- **不得仅凭 404 下结论**：404 可能来自索引不存在、别名失效、权限掩盖或路径错误，均不能证明“字段未映射”。按 §5.1：**404/403/408/429/5xx/非 JSON 均属 S 类，终止本轮**（不是“跳过”）；仅当响应为 200 但字段数非 0 时才是 SKIP（禁用 R-7、继续其他步骤）。
- **分类汇总**：字段数非 0 → SKIP；前置请求不可判读（404/403/408/429/5xx/非 JSON）→ **S 类终止**。
- 字段名固定为 `a4_probe_absent_field`（不含随机后缀，便于复核）；证据中记录前置请求的状态码与识别到的字段数（应为 0）。
- 请求：同 R-6（含 `"_source": false`），仅把脚本字段替换为 `'a4_probe_absent_field'`
- 目的：在**不创建任何资源**的前提下观察 ES 对“未映射字段”的处理（未映射字段的解析语义与缺字段一致）
- 限制：字段名不同、且可能命中与 `qa_content_vector` 不同的内部路径；**只能作为近似证据**，报告中必须写明“近似，非 A-4 定论”。若 R-3 显示该索引确实缺 `qa_content_vector`，则以 R-6 为准，R-7 可跳过
- 分层：本步是**诊断 DSL**，与 R-6 的生产等价 DSL 分列，**不得**合并推断

### R-8 “字段存在、文档无值”变体（**受控诊断 DSL**，非生产等价）
- 前提（三条全部满足，缺一即跳过并标“未取证”）：
  1. R-3 显示 `qa_content_vector` **存在**（否则走 R-6/R-7，不适用本条；未映射时 `exists` 恒为 0，会把“字段缺失”误判为“全候选无值”）；
  2. R-5 候选总数 ≥ 1；
  3. 满足下列任一：
     - **(a) 全候选无值已证明**：R-5 的“无值数 = 候选总数”（且“有值数 = 0”）；
     - **(b) 显式限定到无值文档**：在 R-6 的 query 上追加 `must_not: [{exists: {field: qa_content_vector}}]`。
- **禁止**：对“混合有值/无值”的完整候选集直接执行并解读分值——有值文档的正常得分会被误读成无值行为，属结果污染。
- 请求：R-6 的 DSL + （仅当 (b) 时）`must_not exists qa_content_vector` + `"_source": false`
- 记录：同 §5.0 白名单；特别记 `hits.max_score` 与命中 `_score` 是否恒为 `1.000`（**可能解释**：cosine 缺失计 0 再 +1.0；此为推论、未验证，不得写成事实），或是否报错；4xx 需匹配 §5.1 白名单才可归 P
- 分层（三点必须同时写明）：
  1. 本条是**诊断 DSL**（可能带 `must_not exists`），R-6 才是**生产等价 DSL**；两者结果分列，不得合并推断。
  2. 即使结果命中集为空，也不能反推“生产上会 0 命中”，因为生产不带该过滤。
  3. 本条**不替代** A-4 的“缺字段”结论；它只记录 F2/F9 指向的“字段存在但无值”风险面。

### R-9（可选）维度不匹配对照
- 请求：同 R-6（含 `"_source": false`），向量长度改为 `dims ± 1`
- 目的：拿到“维度不匹配”的错误形态，用于在解读 R-6/R-7 时排除“其实是维度错”的误判
- 记录：状态码、`error.type`；**仅当结果为 200 或白名单内 `illegal_argument_exception`** 时，才**立即**回到正确长度复核一次（计入请求上限），避免把诊断请求当成结论；**5xx 或其他非白名单 4xx 按 §5.1 终止，不做复核**
- 分层：**诊断 DSL**，仅用于排除误判，不作为 A-4 结论

## 6. 结果判定表（不预设；逐行填写，不做合并推断）

> 下表“可能含义”列**只列可能的解释**，任一解释都需要 `error.type`、R-3/R-5 计数等前提支持，**不得当作因果结论**；若前提不足，写“原因未定”。

| # | L0 真实 ES 观察（本方案可直接产生） | 可能含义 | L1 门禁（静态/离线，已有证据） | L2/L3（**未验证**，不得外推为应用行为） | 结论允许写法 |
|---|---|---|---|---|---|
| A | 4xx（白名单内）+ `error.type` | **可能是**与 `qa_content_vector` 缺失相关的查询失败；也可能来自查询构造/字段类型/其他原因，需 `error.type` 与 R-3 联合判断 | 受控入口在缺字段索引上**先**被 fingerprint/schema 门禁拒绝（F10–F12 静态；`TestSchema_MissingQAContentVector_Error` 离线） | 假设（绕开门禁）：组件会把 4xx 向上返回、不静默成空结果（离线 fake：`TestA4_ESErrorIsPropagated`；**真实 ES 未验证**） | “ES 返回该 4xx（`error.type` 见记录）；受控应用在该索引上先被门禁拦截，**不是** ES 错误透传” |
| B | 200 + `hits=0` + `_shards.failed=0` | **可能是**缺字段被当作“无匹配”静默处理（未证明）；也可能是过滤/路由/分片等原因 | 不适用（受控入口不可达） | 假设（未验证）：绕开门禁时可能返回 0 条且无错误 | “ES 侧 0 命中（**原因未定**）；应用侧行为**未验证**” |
| C | 200 + 命中 + 分值恒为 `1.000` | **可能是**缺失值按 0 计后 `+1.0` 打平（推论，未验证）；也可能是候选本身同分 | 不适用（受控入口不可达） | 假设（未验证）：F9 仅能说明融合**不使用** ES 分值，不能得出实际排序结果 | “ES 侧分值打平（**原因未定**）；融合影响**未验证**” |
| D | 200 + 命中 + 分值分化 | **可能是**部分文档参与评分；**若 R-3 已确认该字段缺失，则不能解释为“部分文档有值”**（字段不存在时不存在“有值”文档），应记“原因未定” | 不适用（受控入口不可达） | 假设（未验证）：仅命中子集参与融合 | “分值分化（**原因未定**）；覆盖率与融合影响**未验证**” |
| E | 200 + `_shards.failed>0` / `timed_out=true` | **可能是**分片级失败或超时；具体原因未定 | 不适用 | F8 静态：组件不读取 `_shards`/`timed_out`；离线 fake 已固化该现状（`TestA4_PartialShardFailureNotSurfaced`）；对真实 ES 与受控应用的实际影响**未验证** | “不可见的分片/超时标志（**组件静态特征**）；应用影响**未验证**” |

**判定规则（七条，逐条核对）**：
1. 一条 A-4 记录 = 一行 L0 观察 + L1 列（必须标注“静态/离线”）+ L2/L3 列（必须写“未验证”或显式 `bypass-gate` 标注）。
2. **禁止**将“项目会返回 0 条 / 参与 RRF”写成已观察行为：缺字段索引在受控入口不可达，这句话只能出现在 L2/L3 列并带“假设/未验证”。
3. **禁止**用 L0 + L1 静态声称“与检索器错误路径一致”（P-8 通过标准）；该结论只能由 L2 或 L3 的实际取证回答。
4. 任何绕过门禁的执行必须显式标注 `bypass-gate`，且**不改变** A-4 的“未执行/未授权”状态；不得单行写成“A-4 通过”。
5. 本表所有“L0 观察”只能填写 §5.0 白名单字段（优先 `error.type` / `error.root_cause[].type`），**不得引用 `error.reason` 原文**；也不得引用 `_source` 任何值。A 行只需 `error.type` 即可成立。
6. “可能含义”列**只能是可能解释**；**5xx 与不可判读响应**可如实登记为“服务侧失败（原因未定）”，但**不得归入 A–E 任一行**，也不得据此写“A-4 通过”；同时必须注明“本次观察不完整”。
7. 若 R-5 因未授权向量而 BLOCK，必须在报告中写“**未授权向量读取，R-5 及 R-6/R-6P/R-7/R-8/R-9 均未执行**”，不得以其他索引/文档/向量代替。

**与 P-8 通过标准对接**：P-8（`design/stage2d-plan.md` L89）要求“与检索器错误路径一致”。该结论只能由 L2 或 L3 的实际取证回答；本方案（L0 + L1 静态/离线）**无资格回答它**。具体到 A 行：ES 的 `error.type` 属检索失败，而受控应用在缺字段索引上先触发 fingerprint/schema 门禁（F10–F12），二者**不是同一条路径**，不得据 L0 声称“一致”；若 L0 为 B/C/D 行（静默），需按 P-8 的“行为导致静默错误结果 → 记录为缺陷候选，不自行修复”登记，同样不构成修复授权。

## 7. 前提条件与替代路径（含单独授权项）

### 7.1 只读方案的前提（全部满足才可执行）
1. 用户书面确认只读授权范围：集群地址、**单一索引名**、时间窗、请求上限（**GET ≤6、`_count` ≤3、`_search` ≤7，合计 ≤16**；见 §10），且授权文本明确“不写入、不创建、不删除”（对齐 `design/stage2d-plan.md` §5.2 L271）。
2. 账号为只读角色（§4 最小集），且不含 `manage`/`write`/`create_index`/`delete_index`。
3. 目标索引为**具体物理索引**，且允许 `GET <index>`、`GET <index>/_mapping/*`、`POST <index>/_search`、`POST <index>/_count`。
4. 索引中存在至少 1 条候选文档；用户同意**在内存中**读取 1 条 `content_vector`（只回传 `len` 与 sha8）——这是**执行前置**，未同意则 R-5 及 R-6/R-6P/R-7/R-8/R-9 **均不可执行**（BLOCK，本轮止于 R-4）。若要做 R-6P，另需**单独同意在本机接收 1 条命中的完整 `_source`（含正文、向量与全部字段值，仅提取键名后丢弃）**；未同意则仅 R-6P 跳过。
5. 明确“L0 结论 ≠ 项目错误处理已验证”，报告按 §3/§6 分层写，L2/L3 保持“未验证”。
6. **失败即停且不重试**：按 §5.1 **优先级**执行——先判 S（401/403/408/429、任何 5xx、超时、非 JSON、写入迹象）立即终止，**S 优先于任何“4xx 预期”**；再判 Q（越界可判读）终止；仅白名单内 4xx 可继续。5xx 可如实记录（请求已发出、时刻、status、error.type），但须标“原因未定/观察不完整”，不得归因。**禁止自动重试**（不得用循环/重试脚本/幂等重放），每条请求只发一次；目标为别名/多目标或无法确认唯一具体索引时，**终止而不改查**。

### 7.2 若没有合适的现成索引（需**另行**授权，本轮不执行）
| 替代方案 | 需要的新授权 | 主要副作用 |
|---|---|---|
| 新建临时索引（无 `qa_content_vector`）+ 写入 1 条文档 | `create_index` + `write`（仅限一次性命名） | 占用集群资源与命名；写入会进副本/刷新；**必须约定删除**，删除不可逆；可能与"名称不复用"约束冲突（`D-3`） |
| 复用现有索引但**临时删除/重建 mapping** | **不建议**：破坏性、不可逆 | 直接损坏现有数据，本方案不提出该路径 |
| 仅用 R-7 近似 | 无（只读） | 结论只能标“近似”，A-4 仍为未验证 |

命名建议（若获授权）：`a4-probe-<sha8>-<yyyymmdd>`，且**不与任何生产名复用**；创建前记录集群已有同名检查结果。

## 8. 离线骨架（本轮已实现，仅本机）

- 文件：`server/core/retriever/a4_query_shape_test.go`（新增，未改生产代码；**v0.2/v0.3/v0.4 均未修改此文件**，SHA256 仍为 `ED77739A…`）
- 用例：
  1. `TestA4_QAFieldRequestShape`：固定请求形状——`POST /<index>/_search`、`script_score`、脚本文本 `cosineSimilarity(params.embedding, 'qa_content_vector') + 1.0`、`size=50`、`bool.filter → bool.must → match._knowledge_name`
  2. `TestA4_ESErrorIsPropagated`：假 ES 返回 400（`search_phase_execution_exception`）→ 检索器返回错误、0 文档（**不**静默成空结果）
  3. `TestA4_PartialShardFailureNotSurfaced`：假 ES 返回 200 + `_shards.failed=1` + `timed_out=true` + 0 命中 → 检索器返回 `nil` 错误、0 文档（**记录现状**，非期望行为）
  4. `TestA4_HitParsing`：`_source` 缺 `qa_content_vector` 可正常解析；出现白名单外字段（如 `title`）整体报错
- 安全约束：仅使用 `httptest` 本机地址；辅助函数 `a4RequireLoopback` 显式拒绝非回环主机；不读取任何密钥；不发起外部请求
- 运行：`cd server; go test ./core/retriever -run TestA4 -count=1 -v`（本轮结果见交付说明）
- 边界：以上**不证明**真实 ES 的任何行为；也不覆盖 L2/L3

## 9. 需要用户确认的最少事项

| # | 待确认 | 影响 |
|---|---|---|
| C1 | 是否授权 L0 只读（单索引 / 时间窗 / 请求上限：GET ≤6、`_count` ≤3、`_search` ≤7，合计 ≤16） | 不授权则 A-4 维持“未执行” |
| C2 | ES 版本与 base URL（§4） | 影响结论适用范围（8.11/8.18 等） |
| C3 | 只读账号与最小权限是否满足 | 不满足则无法执行 R-1..R-9（含可选 R-6P） |
| C4 | 目标索引名（具体物理索引）与是否已授权 | 决定 R-2 是否可直接通过项目门禁口径解读 |
| C5 | 该索引是否缺 `qa_content_vector`（若已知） | 决定走 R-6（定论）还是 R-7（近似）+ §7.2 |
| C6 | 是否允许**在内存中**读取 1 条 `content_vector`（仅回传 `len`/sha8）——**执行前置**；**另外单独**是否允许 R-6P 在本机**接收 1 条命中的完整 `_source`（含正文、向量与其他全部值）**，仅提取键名后丢弃 | **不授权前者 → R-5 及 R-6/R-6P/R-7/R-8/R-9 全部不可执行（BLOCK，本轮止于 R-4）**；不授权后者仅跳过 R-6P（标“未取证”） |
| C7 | 是否同时取证 R-8（字段存在但无值） | 需先由 R-5 计数证明“全候选无值”，或同意在**诊断查询**中附加 `must_not exists`（非生产等价） |
| C8 | 如需临时索引：是否单独授权创建/写入/删除 | 不授权则 A-4 缺字段情形只能标“近似” |

## 10. 精确在线执行清单（**需另行授权**；本清单本身不构成授权）

执行人：用户。助手不执行、不代持凭据。所有命令在用户终端以环境变量注入；报告写入 `verification/stage2d-a4-<sha8>-<date>/`（本轮**不创建**该目录），且**只存白名单摘要**（§5.0/§11）。

| 序 | 类别 | 操作 | 采集（仅 §5.0 白名单） | 预期可判读（P：记录后继续） | 越界/故障（终止本轮：Q/S） |
|---|---|---|---|---|---|
| 1 | GET | `GET $ES_URL/` | `version.number`、`version.build_flavor`、`tagline`、`cluster_name`（`<cluster>`） | 200 | 401/403、5xx、超时；非 ES 实现 |
| 2 | GET | `GET $ES_URL/_alias/<index>`（交叉验证） | 状态码；若可解析记 `error.type` | 200；或 404（**仅在步骤 3 已 200 且 `aliases` 为空时**可作“无别名”辅证） | 5xx/403/408/429/超时 → 终止；404 且不满足联合条件 → 不可判读（按 S 终止） |
| 3 | GET | `GET $ES_URL/<index>?filter_path=*.settings.index.uuid,*.aliases,*.mappings._meta` | 顶层键是否恰为 `<index>`、`aliases` 是否为空、uuid 前 8 位、`_meta` fingerprint（布尔） | 200 且顶层键恰为 `<index>`、`aliases` 可读 | **别名/多键/键名不等/无法确认 → 终止且不改查**；404/403/408/429/5xx |
| 4 | GET | `GET $ES_URL/<index>/_mapping/field/qa_content_vector` | 存在性、`type/dims/similarity/index` | 200（含空 mapping = 字段不存在） | 404/403/408/429/5xx；字段存在但 `dims` 缺失/`similarity` 非 cosine |
| 5 | GET | `GET $ES_URL/<index>/_mapping/field/content_vector` | 同上 | 200 | 404/403/408/429/5xx；同上维度异常 |
| 6 | GET | `GET $ES_URL/<index>/_mapping/field/a4_probe_absent_field` | 字段数（应为 0） | 200 且字段数为 0 | 字段数非 0 → **禁用 R-7（SKIP）**；404/403/408/429/5xx → 不可判读，**终止本轮** |
| 7 | `_count` | `POST .../_count`（`match _knowledge_name`） | 候选总数 | 200 且 >0 | 5xx/401/403/408/429；候选=0 → 终止（A-4 不可判定） |
| 8 | `_count` | `POST .../_count`（无值 / 有值各一次） | 无值数、有值数、等式校验（布尔） | 200 且 总数=无值数+有值数 | 5xx/401/403/408/429；等式不成立 → 终止 |
| 9 | `_search` | `POST .../_search`（`size:1`，`_source:["content_vector"]`） | 向量 `len`、sha8、`_id` 前 4 位 | 200 且命中含 `content_vector` | **未获 C6-① → BLOCK 终止**；5xx/403/408/429；无 `content_vector` → 终止 |
| 10 | `_search` | `POST .../_search`（R-6，`_source:false`） | status、error.type、root_cause.type、took、timed_out、_shards、hits、max_score、前 3 分 | 200（对应 B/C/D/E）；或**白名单内 4xx**（对应 A，§5.1） | **5xx：如实记录（请求已发出/时刻/status/error.type）并标“原因未定”，终止**；非白名单 4xx、401/403/408/429、超时 → 终止 |
| 11 | `_search` | 可选 R-6P（`size:1`，**不加** `_source:false`） | 键数、白名单外键（布尔）、键名集合、命中数 | 200 且 ≥1 命中 | 未获 C6-② → **SKIP**；200+0 命中或白名单 4xx → “未取证”，**跳过 12–14**；5xx/非白名单 4xx → 终止 |
| 12 | `_search` | 可选 R-7（前置三条全过） | status、error.type、hits | 200 或白名单 4xx | 前置字段数非 0 → SKIP；前置 404/403/5xx → 终止；非白名单 4xx/5xx → 终止 |
| 13 | `_search` | 可选 R-8（全候选无值或 `must_not exists`） | status、max_score、scores | 200 或白名单 4xx | 前提不满足 → SKIP；非白名单 4xx/5xx → 终止 |
| 14 | `_search` | 可选 R-9（`dims±1`）+ 正确长度复核 1 次 | status、error.type | 200 或白名单内 `illegal_argument_exception` → 才做复核 | 5xx/非白名单 4xx → 终止且**不复核** |
| 15 | 归档 | 仅写白名单摘要（§11） | 见 §11 | — | — |

**上限与重试**：GET ≤6、`_count` ≤3、`_search` ≤7，合计 **≤16**（含可选步骤与 R-9 的复核请求）；超限必须再次确认。跳过（SKIP）与阻塞（BLOCK）的步骤不计数，已发出的请求（含返回错误的）各计 1 次。**所有请求只发一次，禁止自动重试**（含脚本循环、幂等重放）；任一步命中“越界/故障”列即终止整轮，并在报告中记录停止点、已消耗计数与是否属“原因未定”。

## 11. 脱敏证据模板（建议直接复制到 `verification/stage2d-a4-<sha8>-<date>/REPORT.md`）

```
# A-4 真实 ES 只读验证记录（<date>）
- 对象：ES <version>（build_flavor=<x>）@ <host:port 脱敏>；索引 <index>（uuid 前 8 位 <uuid8>）
- 执行人/授权：<user>；授权范围：只读、单索引、<window>、请求上限 <n>；C6：向量=是/否；R-6P `_source`=是/否
- 项目对象：HEAD 38142d05…；design/stage2d-plan.md 68A5F6CC…；方案 v0.4
- 前提核对：具体物理索引=是/否（**依据步骤 3**：顶层键数=<n>、是否恰为该名=是/否、`aliases` 是否为空=是/否；步骤 2 `_alias` 状态=<code>，是否作“无别名”辅证=是/否/不可判读）；`qa_content_vector`=<存在/缺失>（type=<t>、dims=<d>、similarity=<s>、index=<i>）；`content_vector`=<存在/缺失>（type/dims/similarity/index）；探测字段数=<0/n>；候选总数=<n>；无值数=<n>；有值数=<n>（等式成立=是/否）；向量 len=<n>、sha8=<x>
- 响应处理：**仅白名单摘要**；未落盘原始响应；R-6/R-7/R-8/R-9 均使用 `_source:false`；**R-6P 未使用 `_source:false`**：本机接收 1 条完整 `_source`（含正文与全部值），仅提取键名后丢弃（未授权则未执行）
- 环境：客户端不做任何写入；仅 GET/_search/_count；**每请求只发一次，无重试**；未记录 `error.reason` 原文
- 请求计数：GET=<n>/6、_count=<n>/3、_search=<n>/7，合计 <n>/16；SKIP=<步骤号或 无>；BLOCK=<是/否（C6-① 未授权时注明“止于 R-4”）>；停止点=<无/步骤号 + Q/S 类别 + 是否“原因未定”>

## 观察
| 步骤 | query_kind | 请求摘要 | 状态 | error.type | _shards t/s/f | timed_out | hits | max_score | 前 3 条 _score | 备注 |
|---|---|---|---|---|---|---|---|---|---|---|
| R-2 | — | GET _alias/<index> | | | — | — | — | — | — | 仅作交叉验证；404 需与 R-2 主判据联合，不得单独断言 |
| R-3 | — | GET _mapping/field/qa_content_vector | 200 | — | — | — | — | — | — | 存在/缺失；type/dims/similarity/index |
| R-5 | — | POST _search（`size:1`，`_source:["content_vector"]`） | | — | — | — | | — | — | len=<n>；sha8=<x>；_id 前 4 位 |
| R-6 | 等价 | POST _search（script_score，`_source:false`） | | | | | | | | |
| R-6P | 等价（解析前提） | POST _search（`size:1`，**不加** `_source:false`） | | | | | | | | 样本=1 条；键数=<n>；白名单外键=是/否（仅该样本） |
| R-7 | 诊断 | POST _search（未映射字段，前置=200 且字段数 0） | | | | | | | | 近似，非定论 |
| R-8 | 诊断（全候选无值 / must_not exists） | POST _search（无值文档） | | | | | | | | 非生产等价 |
| R-9 | 诊断 | POST _search（dims±1）+ 正确长度复核 | | | | | | | | 仅 200/白名单 4xx 时复核；否则终止不复核 |

（`took`、`hits.total.relation` 如需记录，写入“备注”列，不新增列。）

## 结论（只写观察到的事实与可能解释）
- L0：<按 §6 选行，写明 A/B/C/D/E 与 `error.type`/`root_cause.type` 证据；不引用 `error.reason`；“可能含义”只能写“可能是…”或“原因未定”>
- 5xx 或其他不可判读：<如实写“步骤 n 请求已发出、时刻、status、error.type；原因未定；观察不完整”，不归入 A–E、不写 A-4 通过>
- L1：<门禁先行/不可达，引用 F10–F12 与离线用例名；标注“静态/离线”>
- L2/L3：<未执行；禁止写“项目会返回 0 条/参与 RRF”>
- 不声称：项目错误处理已验证 / 与检索器错误路径一致 / 2D 通过 / 真实账单；R-6P 不得外推为整索引
- 剩余未验证：L2（组件级真实 ES）、L3（应用全链）、R-6P/R-8（若跳过）、向量依赖步骤（若 BLOCK）、临时索引定论（若未授权）

## 脱敏检查
- [ ] 无凭据、无完整 uuid、无完整 _id、无 content 正文
- [ ] 无向量分量、无 `_source` 任何值、无原始响应文件残留
- [ ] 未记录 `error.reason` 原文（仅 `error.type` / `root_cause.type`）
- [ ] R-6P 若已执行：仅记录键数/键名集合/布尔，无任何 `_source` 值
- [ ] 5xx/不可判读记录已标“原因未定/观察不完整”，未归入 A–E
- [ ] 索引名与集群名按需脱敏；暂存文件（如有）已删除
```

## 12. 剩余边界与不变项

- 本轮**未**连接真实 ES/模型/Qdrant/MySQL；**未**创建、写入或删除任何资源；**未**提交、推送、合并。
- 既有未提交修改、历史快照（`…-v14`、`…-v14r1`）与历史证据保持只读，未覆盖。
- A-4/P-8 仍为**完整 2D 验收条件且未执行、未授权**；本方案不改变该状态。
- 本轮（v0.2）按独立复核报告修订：R-8 限定无值候选并区分诊断/等价 DSL、响应白名单与默认不落盘、L0/L1/L2-L3 严格分层、统一失败即停与请求总上限、R-7 前置未映射确认；离线测试文件**未改**（SHA256 仍为 `ED77739A…`）。
- 本轮（v0.3）按第二份复核报告修正执行一致性：§5.0 白名单改为字段字典并与各步骤/结果表/模板逐项对齐；`error.reason` 默认不记录原文；R-6P 如实声明**会在本机接收完整 `_source`（含正文与全部值）**并需单独授权，且仅代表 1 条样本；R-2 遇别名/多目标/无法确认唯一具体索引时**终止而不改查**（范围变更需另行授权）；R-7 不以 404 作为“未映射”证据；新增 §5.1 逐步骤 P/Q/S 分类、SKIP 与计数规则。
- 本轮（v0.4）按第三份复核报告限定执行规则：§5.1 新增**判定优先级**（S 类 401/403/408/429/5xx/超时/不可判读**优先于**任何“4xx 预期”）与**四项 4xx 白名单**（须匹配 `error.type`），不再把任意 4xx 当预期诊断错误；R-2 区分 `_alias` 与目标索引查询的 404（`_alias` 404 单独不构成“无别名”证据，需与主判据联合）；5xx 改为“可如实记录发出/时刻/status/error.type + 原因未定”，仍禁止归因与声称 A-4 通过；§6 “可能含义”全部改为**可能解释**（并把 D 行在字段缺失前提下的推断列为禁止），判定规则增至 7 条；未授权向量读取改为 **BLOCK**（R-5 及 R-6/R-6P/R-7/R-8/R-9 均不可执行，不得改用其他索引/文档/向量）。
- 方案仍为 **v0.4 草案**，未经用户授权、未产生任何真实 ES 证据；A-4 证据栏保持空白，不得以本方案代替执行。
- 遗留术语（不在本轮改历史计划）：`stage2-plan.md` §8.8 的“kNN”实为 `script_score` 近似检索；2D 结项时应以实际 DSL 表述，避免“kNN 已验证”的误读。
- L2/L3 若要做，需要新增“可配真实目标”的组件级工具（默认拒绝非本机、不读密钥、需显式开关），与本方案的只读 REST 取证分开授权。
