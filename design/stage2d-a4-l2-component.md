# Stage 2D A-4 / P-8 组件级（L2）验证入口与后续真实 ES 查询清单

- 状态：**离线入口已实现并通过本机测试（含 `-race`）**；**未连接**任何真实 ES、模型或数据库；**未创建/写入/删除** 任何资源；**未提交/推送/合并**；**未更新**任何验收状态。
- 后续时点：2026-10-31 已按用户单独授权执行过**一次** L2（真实本机 ES 8.11.3 + 本机 fake embedding，只读），证据 `verification/stage2d-a4-l2-38142d0-20261031/REPORT.md`；本文件其余章节仍为设计记录与离线结论，**不代表已由该次运行验证**。
- 第二轮修复（错误分类）：取消 `expected_4xx`，改为**预先声明的状态码 + error.type + root_cause.type 判定表**；只有 2xx 可解析与 `(400, search_phase_execution_exception, script_exception)` 可判定，其余一律 `incomplete`；新增 `observed` 标志防止“不完整”被写成完成观察；合成向量对齐 L0 的 `e0`（1024 维，非零下标 0）。
- 第三轮修复（UUID 精确比较）：目标 `uuid` 由 `EqualFold` 改为逐字节 `==`（区分大小写），不匹配即 fail closed，只发 1 条只读 GET、不继续 mapping/search；证据只写 `uuid_match` 布尔值，不写 UUID 原文。
- 对象：HEAD `38142d05d25e762e7058865213b9e667dee980db`；方案 `design/stage2d-a4-es-plan.md` v0.4（`FD5C624F…`）；L0 样本 `verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md`；复核报告 `stage2d-a4-l2-review/REPORT.md`（5 项阻塞）、`stage2d-a4-l2fix-review/REPORT.md`（错误分类 1 项阻塞）、`stage2d-a4-l2fix2-review/REPORT.md`（UUID 精确比较 1 项阻塞）。
- 本文件只描述 **L2 组件层**。**L2 ≠ L3**：L2 通过不等于 A-4 通过，更不等于 Stage 2D 通过。

## 1. 目的与已知事实边界

L0 样本已证明的事实仅有：本机 ES 8.11.3、单一合成文档、索引缺 `qa_content_vector` 时，**直接**发 `script_score` 查询返回 HTTP 400（`search_phase_execution_exception` / `root_cause` `script_exception`）；对照 `content_vector` 返回 200、1 命中、`max_score=2`。该样本同时写明"项目检索器对该真实 ES 响应的处理未验证"。

本入口回答的问题只有一个：**生产检索器组件（`newRetriever` + es8 + `EsHit2Document` + go-elasticsearch v8）在给定响应下如何返回、如何报错、是否释放响应体、是否响应取消、是否只发一次**。它**不**回答"应用全链会怎样"。

## 2. 入口与文件

| 文件 | 包 | 入口 | 覆盖的生产代码 | 在线开关 |
|---|---|---|---|---|
| `server/core/retriever/a4_component_entry_test.go` | `retriever` | `TestA4Component_NormalResponse` 等 4 个 | `newRetriever`、es8 `SearchModeDenseVectorSimilarity`、`EsHit2Document`、go-elasticsearch `search.Do` | 无（仅本机假 ES） |
| `server/core/retriever/a4_component_real_branch_test.go` | `retriever` | `TestA4Component_RealESTarget`（默认 skip）+ `TestA4Component_TargetGate`（13 子用例；其中 3 条 UUID 空白拒绝为 2026-10-31 新增）+ 16 个离线用例 | 同上 + 只读预检（`Client.Perform`）+ 结果分类 | 有（三重门 + 预期 UUID） |
| `server/core/a4_component_rag_test.go` | `core` | `TestA4ComponentRag_*` | `retriever.BuildRetriever`、`Rag.retrieve`（filter、`esTopK`、score 归一化、错误透传） | 无（仅本机假 ES） |

两份 `retriever` 入口共用一个 **发送传输层 `a4cSender`**：所有 ES 请求（含预检 GET 与两条查询）都在这里记账、限额、白名单过滤。

## 3. 默认安全模型（真实目标默认不可达）

**默认状态**：未设置任何 `A4_COMPONENT_*` 时只启动 `httptest` 假 ES；`TestA4Component_RealESTarget` 直接 `t.Skip`。本轮即此状态。

真实目标需同时满足下列全部条件，任何一条不满足都**显式报错拒绝**（不静默回退假 ES）：

| 变量 | 必须满足 | 拒绝条件 |
|---|---|---|
| `A4_COMPONENT_ALLOW_REAL_ES` | 等于 `1` | 其他值 |
| `A4_COMPONENT_AUTHZ` | 等于 `I-AUTHORIZE-A4-COMPONENT-READONLY` | 其他值 |
| `A4_COMPONENT_ES_URL` | `http`/`https`，不含 userinfo | 其他 scheme、URL 内嵌账号密码 |
| `A4_COMPONENT_INDEX` | 非空，且通过 `vector.ValidatePhysicalName` | 空、通配、别名表达式、多目标、date math |
| `A4_COMPONENT_INDEX_UUID` | 已确认的物理索引 uuid（≥8 字符），与运行时 GET 结果**逐字节精确相等**（区分大小写） | 空、**含任何首尾或内部空白**（输入链不做 `TrimSpace` 归一），大小写/前缀/后缀不同 → `uuid_mismatch` |
| `A4_COMPONENT_KB` | 非空（过滤值必须与生产一致） | 空 |
| `A4_COMPONENT_DIMS` | 正整数，默认 `1024` | 无法解析或 ≤0 |
| `A4_COMPONENT_ALLOW_REMOTE` | 非回环主机时必须为 `1` | 非回环且未设置 |
| `A4_COMPONENT_ES_USER` / `A4_COMPONENT_ES_PASS` | 可选 basic auth | 凭据写在 URL 中 |

**传输层硬约束**（全部在真正发送前生效）：

- 只读白名单：`GET /<index>`、`GET /<index>/_mapping/field/content_vector,qa_content_vector`、`POST /<index>/_search`；其他方法/路径一律拒绝且不发送。
- 并发安全请求上限 `a4cMaxRequests = 4`（2 次只读预检 + 2 次查询）；超限请求被拒绝、计数不增加、不会到达服务端。
- `elasticsearch.Config.DisableRetry = true`：SDK 默认 `MaxRetries=3` 且对 502/503/504 重试，此处显式关闭（用 503 用例证明只发一次）。
- 3xx 在传输层直接拒绝：`elastictransport` 直接调用 `RoundTrip`（不经 `http.Client`），因此本入口既不会跟随重定向，也会把 3xx 记为 `redirect` 且判为不完整。
- 凭据只从环境变量读，不读凭据文件；查询一律 `TopK=1`（`size:1`），把内存中接收的命中压到最少。

### 3.1 判定声明表（只有两类可判定）

分类由 `a4cClassifyError` 按固定优先级执行，**未命中即 `incomplete`**：

| 优先级 | 条件（状态码 + error.type + root_cause.type） | 类别 | 是否可判定 |
|---|---|---|---|
| 1 | 缺状态码或缺 `error.type` | `unparsable` | 否 |
| 2 | `404` 或 `index_not_found_exception` | `target_missing` | 否 |
| 3 | `401` / `403` / `408` / `429` | `auth_or_throttle` | 否 |
| 4 | `>=500` | `server_5xx` | 否 |
| 5 | `400` + `search_phase_execution_exception` + `script_exception` | `script_rejected` | **是**（仅记录观察） |
| 6 | 其余 4xx（未知 `error.type`、`422` 等） | `undeclared_4xx` | 否 |
| 7 | 其他 | `transport` | 否 |

- `2xx` 可解析为 `ok`（对照必须 `ok` 且至少 1 命中）。
- `script_rejected` 只记录 `status` / `error.type` / `root_cause.type`，并标 `note=cause_not_attributed`；**不得**把 400 当作缺字段的证据，证据字符串额外写 `qa_attribution=none`。
- `v.Observed` 仅在“预检通过 + 对照 `ok` + QA 可判定”时置真；它等价于 `!Incomplete`，单独输出以便复核直接看到“未完成观察”。

## 4. 断言归属：哪些调用生产共享实现，哪些仍是 fake 结论

| 断言 | 实际执行的生产代码 | 归属与限制 |
|---|---|---|
| 请求走 `POST /<index>/_search`，脚本为 `cosineSimilarity(params.embedding, '<field>') + 1.0` | `newRetriever` + eino es8 `dense_vector_similarity.BuildRequest` | **生产共享**（对假 ES 的请求体断言）；真实 ES 是否接受该 DSL 由 L0 回答 |
| 字段由 ctx 决定：`content_vector` / `qa_content_vector` | `newRetriever` 读 `coretypes.RetrieverFieldKey` | **生产共享** |
| `size` 等于 `er.WithTopK` 传入值；`Rag.retrieve` 固定 `esTopK=50` | eino es8 `BuildRequest`、`core.esTopK` | **生产共享**（仅假 ES） |
| 过滤形状 `bool.filter[0].bool.must[0].match._knowledge_name`；`excludeIDs` 生成 `must_not.terms._id` | `Rag.retrieve` 的 `esQuery` + `es8.WithFilters` | **生产共享**（仅假 ES）；真实分支的 filter 是测试复制（`a4cFilter`），不是 `Rag.retrieve` |
| 每条请求只发一次；4xx 不重试 | `elasticsearch.Config.DisableRetry` + `elastictransport.Perform` 的 `RoundTrip` 循环 | **生产共享**（503 用例证明 `served=1`） |
| 请求计数/上限/白名单在发送前生效 | 测试自建 `a4cSender`（包装 `http.DefaultTransport`，不修改生产代码） | **测试实现**；但它包住的正是 SDK 的真实发送出口 |
| 命中解析：`content`→`Content`、`content_vector`→`DenseVector`、`ext`/`_knowledge_name`→`MetaData`、白名单外字段报错 | `EsHit2Document` + `sonic.Unmarshal` | **生产共享**，对**假响应体**；不校验向量维度（长度 2 也接受） |
| score 透传 `1.42`（组件层不归一化）；`Rag.retrieve` 中 `score>1` 减 1 → `0.42` | eino 解析器 `WithScore`；`Rag.retrieve` | **生产共享**（仅假 ES） |
| 4xx 返回 `*types.ElasticsearchError`（`Status`/`ErrorCause.Type`） | go-elasticsearch v8 `search.Do` | **生产共享的错误类型**；“真实 ES 会产出该 JSON”来自 **L0 样本**，本入口当输入 |
| 4xx 是否“可判定”（声明表） | 测试自建 `a4cClassifyError` | **测试实现**：只是测试侧的声明假设；真实 ES 若返回其他形态 → fail closed |
| 正常/错误路径都关闭响应体（open==close==1） | `search.Do` 的 `defer res.Body.Close()` + 预检 GET 的 `defer Close` | **生产共享**；包装的 `RoundTripper` 只统计 `Close` 调用次数 |
| ctx 取消后调用有限时间返回 | typedapi `Do` → `transport.Perform` → `http.Client` 的 ctx 传播 | **生产共享**；服务端断开由 `net/http` 决定，未单独取证 |
| 查询向量为 1024 维非零 one-hot（下标 0，即 `e0`）且出现在请求体 `params.embedding` | `common.NewEmbedding`（OpenAI 兼容桩）+ eino 请求构造 | **生产共享的向量传递**；向量本身是合成值，不调用真实模型；下标 0 用于对齐 L0（见 §9） |
| 预检响应解析（uuid/aliases/dims）与 fail-closed 判定 | 测试自建解析（`a4cPrecheckIndex` / `a4cPrecheckMapping`） | **测试实现**，未在真实 ES 上验证响应形状 |

**仍是 fake 结论（不得写成真实行为）**：假 ES 不计算余弦、不校验向量维度、不返回真实 `took`/`_shards`；`200 + _shards.failed>0` 是否被项目忽略由既有 `a4_query_shape_test.go` 记录，不属于本入口结论。

## 5. 覆盖清单

| 覆盖点 | 用例 | 关键断言 |
|---|---|---|
| 正常响应 | `TestA4Component_NormalResponse`、`TestA4ComponentRag_RequestShapeAndScoreNorm`（3 子用例） | 1 次 `_search`；脚本字段；`size=50`；`_knowledge_name` 过滤；`must_not._id`；无 `_source`；分数 1.42/0.42；one-hot 向量；响应体 1/1 |
| ES 错误传播 | `TestA4Component_ESErrorPropagated`、`TestA4ComponentRag_ErrorPropagated` | `errors.As` → `*types.ElasticsearchError`；`Status=400`；`error.type` 保留；0 文档；1 次请求（无重试） |
| 取消 | `TestA4Component_ContextCancel`、`TestA4ComponentRag_CanceledContext`、`TestA4ComponentRealBranch_CancelIsIncomplete` | 已取消 ctx 不发请求；阻塞请求 5s 内返回；真实分支判 `incomplete` |
| 响应体释放 | `TestA4Component_ResponseBodyReleased`（normal/error） | 两条路径 `open/close = 1/1` |
| 真实分支请求记录 | `TestA4ComponentRealBranch_RecordsRequestInTransport`、`TestA4ComponentRealBranch_ReviewReproRecordsRequest` | 传输层记录 4 条：`meta,mapping,search,search`；`served=4`；verdict 完整；复核 repro 的断言（`real=true` 时 `obs.count()==1`）由 exit 1 变为 PASS |
| 发送前上限 | `TestA4ComponentRealBranch_BudgetRefusedBeforeSend` | 上限=2 时 `served=2`、`refused≥1`、判 `incomplete` |
| 无重试 | `TestA4ComponentRealBranch_NoRetryOn503` | 503 时 `served=1`（SDK 默认会重试 3 次） |
| 无重定向 | `TestA4ComponentRealBranch_NoRedirectFollow` | 302 不跟随（`served=1`），记录 `status=302 err=redirect`，判 `incomplete` |
| 非零 one-hot | `TestA4ComponentRealBranch_NonZeroOneHotVector` | 两条查询的 `params.embedding` 都是 1024 维、仅下标 0 为 1.0（`e0`） |
| 对照查询必须成功 | `TestA4ComponentRealBranch_ControlQueryMustSucceed`（3 子用例） | 0 命中/400/500 → `incomplete`，且不再发 QA 查询（`served=3`） |
| 物理索引预检 | `TestA4ComponentRealBranch_IndexPrecheckFailClosed`（7 子用例） | uuid 不符/大小写不同/别名/多目标/404/不可解析/uuid 缺失 → 只发 1 条 GET 即终止 |
| UUID 精确比较 | `TestA4ComponentRealBranch_IndexPrecheckUUIDExactComparison`（6 子用例） | 相同 UUID 继续 mapping/search；大小写/前缀/后缀/其他值/大写+尾随空格 → `uuid_mismatch`、`served=1`、`observed=false`、证据不含 UUID |
| 映射预检 | `TestA4ComponentRealBranch_MappingPrecheckFailClosed`（4 子用例） | 维度不符/`content_vector` 缺失/500/不可解析 → 只发 2 条 GET 即终止 |
| QA 缺字段形态（诊断） | `TestA4ComponentRealBranch_QAMissingFieldShapeIsObservable` | `qa_exist=false` + `script_rejected`（400 + 两种类型）→ `observed=true`，且 `note=cause_not_attributed`、`qa_attribution=none`；不归因、不写“A-4 通过” |
| 未知 404（复核 repro） | `TestA4ComponentRealBranch_Unknown404IsIncomplete` | `404 + index_not_found_exception` → `target_missing` → `incomplete`，`observed=false`（修复前该例 exit 1） |
| 未声明 4xx | `TestA4ComponentRealBranch_UndeclaredQAFailureIsIncomplete`（6 子用例） | 未知 `error.type`/缺 `root_cause`/错 `root_cause`/`422`/缺 `error.type`/404 其他类型 → 全部 `incomplete` |
| QA 正常例 | `TestA4ComponentRealBranch_QASuccessIsCompleteObservation` | 两条查询均 `200` 且 1 命中 → `observed=true`、`reason=""` |
| 不完整不成为观察 | `TestA4ComponentRealBranch_IncompleteNeverObserved`（8 子用例） | index 404 / mapping 500 / 对照 500 / 对照 0 命中 / QA 401 / QA 503 / QA 404 / QA 不可解析 → `incomplete=true` 且 `observed=false` |
| 认证/限流 | `TestA4ComponentRealBranch_AuthIsIncomplete`（401/403/429） | 判 `auth_or_throttle` → `incomplete` |
| 目标开关 | `TestA4Component_TargetGate`（13 子用例，即源码 `refused` 表 13 项；`go test -v` 实测 13 条） | 通配/别名表达式/缺 UUID/首尾或内部空白 UUID/凭据入 URL/非回环无开关等全部拒绝 |

## 6. 本入口**明确绕过**的生产路径（因此不能写成应用全链）

- `core.Rag.Retrieve` 入口的 `checkFingerprint`（Stage 2C-2 门禁）、`VerifyFingerprint`、`verifyBoundResource`、`vector.ValidateSchemaStrict`：**全部未执行**。本入口直接构造 `newRetriever`，没有 `vectorStore`，也不安装 `VectorIOGuard`/`PhysicalBinding`（缺 `PhysicalBinding` 时 `common.NewEmbedding` 的 guard 为 nil，故不会被拦）。
- `retrieveDoOnce` 的双路合并、`RRFFusion`、`doRerank`、BM25 路、`rankScore` 过滤、3 轮 rewrite 与首错取消：**全部未执行**（`core` 入口只调用单路 `Rag.retrieve`）。
- 因此：**组件层观察到 4xx 透传，不等于"应用会返回 4xx"**。真实应用可能先被 fingerprint/schema 门禁拒绝，也可能在 qa 路失败即整体失败（`retrieveDoOnce` L256–260）。
- 本入口**不判定** A-4/P-8 通过，**不判定** Stage 2D 通过；A-4 仍为"未执行、未授权"。

## 7. 后续真实 ES 查询清单（**本轮不执行，需另行授权**）

### 7.1 前置条件（全部满足才可执行）

1. 用户明确授权一次 L2 只读组件运行：**4 条请求**（2 GET + 2 `_search`），无重试、无重定向、`TopK=1`。
2. 目标为**已确认的单一具体物理索引**：名称通过 `ValidatePhysicalName`，且 `A4_COMPONENT_INDEX_UUID` 为 L0 只读清单已确认的 `settings.index.uuid`；运行时 GET 再比对一次，不符即 fail closed。
3. 已确认两个向量字段的 `dims`（用于 `A4_COMPONENT_DIMS`）；`content_vector` 维度不符会 fail closed。
4. 已确认一个真实存在的 `_knowledge_name` 值；过滤值错误会 0 命中并判 `incomplete`（不得记成"字段缺失"）。
5. 只读账号（索引 `read` + `view_index_metadata`，集群 `monitor`）；凭据只经环境变量注入。
6. 用户单独同意 §7.4 的"内存接收 `_source`"。

### 7.2 环境变量（值由用户在自己的终端注入，不写入仓库/文档/对话）

```
A4_COMPONENT_ALLOW_REAL_ES=1
A4_COMPONENT_AUTHZ=I-AUTHORIZE-A4-COMPONENT-READONLY
A4_COMPONENT_ES_URL=http://127.0.0.1:9200
A4_COMPONENT_INDEX=<index>
A4_COMPONENT_INDEX_UUID=<uuid>
A4_COMPONENT_KB=<knowledge_name>
A4_COMPONENT_DIMS=<dims>
# 非回环时才需要：A4_COMPONENT_ALLOW_REMOTE=1
# 需要认证时：A4_COMPONENT_ES_USER / A4_COMPONENT_ES_PASS
```

### 7.3 请求清单（只读，每条只发一次，合计 ≤4）

| 序 | 方法 | 路径 | 用途 | 上限 | 采集（仅白名单） | 判读与停止 |
|---|---|---|---|---|---|---|
| 1 | `GET` | `/<index>?filter_path=*.settings.index.uuid,*.aliases` | 单一物理索引 + uuid 预检 | 1 | 状态、目标键数、是否恰为该名、`aliases` 是否为空、`uuid_match` | 非 200/不可解析/多目标/别名/uuid 非逐字节相等（区分大小写） → **终止**（incomplete），不发后续请求 |
| 2 | `GET` | `/<index>/_mapping/field/content_vector,qa_content_vector` | 向量字段存在性与维度 | 1 | `content_exists`、`content_dims`、`qa_exists` | 非 200/不可解析/`content_vector` 缺失或维度不符 → **终止** |
| 3 | `POST` | `/<index>/_search` | 对照查询（`content_vector`，`size:1`，`_knowledge_name` 过滤） | 1 | 状态、`error.type`、命中数、`len(docs)`、`took`、`timed_out`、`_shards` | 200+≥1 命中 → 继续；0 命中/4xx → **终止**（incomplete）；401/403/408/429/5xx/超时 → **终止** |
| 4 | `POST` | `/<index>/_search` | QA 查询（`qa_content_vector`，`size:1`，同过滤） | 1 | 同上 + `root_cause.type`、`note` | 200，或**恰好** `400 + search_phase_execution_exception + root_cause script_exception`（`script_rejected`）→ 可判定；404/其他 4xx/5xx/不可解析 → **终止**（incomplete） |

**禁止**：重试、改查其他索引/文档/过滤值、追加请求（上限 4，超限测试失败且请求不外发）。

### 7.4 证据摘要与数据暴露边界

- 可写：`requests`、`refused`、`index_ok`、`aliases_empty`、`uuid_match`、`content_exist`、`content_dims`、`qa_exist`、`query_vec=e0/1024`、`control`/`qa` 的 `kind`/`status`/`hits`/`error.type`/`root_cause.type`/`note`、`observed`、`incomplete`、`reason`（仅类别词）。
- 可写但**不得**当成因果结论：`kind=script_rejected` 只说明该状态码+类型的组合被声明表接受；`note=cause_not_attributed`、`qa_attribution=none` 必须与其同时出现。
- 不可写：`_source` 任何值、正文、向量分量、完整 `_id`、完整 `uuid`、账号、凭据、`error.reason` 原文。
- **必须如实声明**：生产请求不带 `_source:false`，真实运行会在本机内存接收最多 2 条命中的完整 `_source`（含正文与向量）；测试只用 `len(docs)`，不打印、不落盘、不回传。
- 运行后把 `a4cVerdict.Summary()` 写入**新建**的 `verification/stage2d-a4-l2-<sha8>-<date>/REPORT.md`（本轮不创建该目录）。

### 7.5 命令形态（授权后才执行）

```
cd server
go test ./core/retriever -run 'TestA4Component_RealESTarget' -count=1 -timeout 120s -v
```

`core` 包的 `TestA4ComponentRag_*` **没有**真实目标开关，始终只连本机假 ES。

## 8. 本轮离线执行记录（本机，无网络）

| 命令 | 结果 |
|---|---|
| `go vet ./core/retriever ./core ./internal/testutil` | 通过，无输出 |
| `go test ./core/retriever -run TestA4Component -count=1 -timeout 150s` | PASS；`TestA4Component_RealESTarget` **SKIP**（未设 `A4_COMPONENT_*`） |
| `go test ./core -run TestA4ComponentRag -count=1` | PASS（3 用例 + 3 子用例） |
| `go test ./core/retriever -count=1`、`go test ./core/indexer -run 'Dimension\|Embedding' -count=1` | PASS（确认 one-hot 改动未破坏既有离线用例） |
| `go test -race -p 1 ./core/retriever ./core -run '^TestA4Component' -count=1`（MSYS2 UCRT64 gcc 16.2.0） | PASS，无 race 报告 |
| `go test ./core/retriever -run 'ReviewReproRecordsRequest' -count=1 -v` | PASS（复核 repro 同一断言：此前 exit 1，现在 `obs.count()==1`） |
| `go test -overlay …/stage2d-a4-l2fix-review/overlay.json -p 1 ./core/retriever -run '^TestReviewUnknown404MustBeIncomplete$' -count=1 -v` | **修复前 exit 1**（`incomplete=false kind=expected_4xx status=404`）；**修复后 exit 0**（`kind=target_missing`） |
| `go test -race -overlay … -run '^TestReviewUnknown404MustBeIncomplete$' -count=1 -v` | PASS（同一 overlay 在 `-race` 下也通过） |
| `go test ./core/retriever -run 'TestA4ComponentRealBranch_(Unknown404|UndeclaredQA|QAMissingFieldShape|QASuccess|IncompleteNeverObserved)' -count=1` | PASS（6 + 8 子用例） |
| `go test -overlay …/stage2d-a4-l2fix2-review/overlay.json -p 1 ./core/retriever -run '^TestReviewUUIDCaseMismatchFailsClosed$' -count=1 -v` | **修复前 exit 1**（`uuid_match=true requests=4 observed=true`）；**修复后 exit 0** |
| `go test -race -overlay …/stage2d-a4-l2fix2-review/overlay.json -run '^TestReviewUUIDCaseMismatchFailsClosed$'` | PASS（同一 overlay 在 `-race` 下也通过） |
| `go test ./core/retriever -run 'IndexPrecheckUUIDExactComparison\|IndexPrecheckFailClosed' -count=1 -v` | PASS（6 + 7 子用例） |

## 9. 剩余边界与不变项

- **未连接**真实 ES / 模型 / 数据库；**未**创建、写入或删除任何资源；**未**提交、推送、合并；A-4 与 Stage 2D 状态不变。
- 预检解析（`GET /<index>` + `GET /<index>/_mapping/field/...` 的真实响应形状、`filter_path` 行为）只在本机合成响应上验证过；真实 ES 的响应差异可能导致 fail closed（保守方向），但需真实样本确认。
- 真实 ES 的字段映射对"字段缺失"的具体表达（键缺失 vs 空对象）在本机合成样本中按"键或 `mapping` 为空即缺失"处理，未对真实 ES 取证。
- 假 embedding 返回固定 one-hot，维度只校验请求侧；真实模型的维度/身份/计费不在本入口。
- **向量对齐边界**：L0 只留下“1024 维 one-hot”的文字记录，**未留存向量字节**；本入口按复核报告所述把非零下标设为 `0`（`e0=[1,0,…,0]`）。若 L0 实际向量不是 `e0`，两者仍需再对齐；本文只声明“形状与下标一致”，不声称来自同一份字节记录（`query_vec=e0/1024` 会写进证据，便于复核区分）。
- **分类边界**：§3.1 的声明表是**测试侧假设**（源自 L0 观察到的那一组）；真实 ES 若返回其他 `error.type`/`root_cause.type` 形态，本入口会 fail closed（判 incomplete），而不是据此认为 A-4 不成立。`observed=true` 也只表示“完成了一次可判定观察”，不等于 A-4 或 Stage 2D 通过。
- **UUID 比较边界**：入口要求目标 `uuid` 与 `GET /<index>` 返回值逐字节相等（区分大小写），不做 `EqualFold`、前缀/后缀归一；也**不对输入做 `TrimSpace`**——环境变量值含首尾或内部空白一律 `refused`，不会“先归一后比较”。ES 当前返回小写 uuid，若真实设施出现大小写差异会被 fail closed（保守方向），需真实样本确认。证据只写 `uuid_match` 布尔值，不写 UUID 原文。
- 未覆盖 TLS 自签/CA、代理、HTTP/2、连接复用与 `EnableCompatibilityMode`。
- 组件层结论**不能**外推为：应用返回 0 条、参与 RRF、被 rerank 影响、或 P-8 "与检索器错误路径一致"。P-8 仍需 L2 真实 + L3 判断。
- 既有未提交修改、历史快照与历史证据保持只读，未覆盖。

## 10. 需要用户确认（本轮均未执行）

| # | 待确认 | 影响 |
|---|---|---|
| C1 | 是否授权 L2 真实 ES 只读组件运行（1 次，≤4 请求，`TopK=1`） | 不授权则 L2 保持"仅离线" |
| C2 | 目标索引名 + 已确认的 `settings.index.uuid` | 预检逐一比对；不符即 fail closed |
| C3 | 一个真实存在的 `_knowledge_name` 值 | 过滤值错误 → 0 命中 → incomplete |
| C4 | `content_vector` / `qa_content_vector` 的 `dims` | 维度不符 → incomplete（配置错误，非 A-4 结论） |
| C5 | 是否接受在内存接收 ≤2 条命中的完整 `_source`（不落盘、不回传） | 不接受则真实运行不可执行 |
| C6 | 认证方式（环境变量注入）与是否允许非回环目标（`A4_COMPONENT_ALLOW_REMOTE=1`） | 非回环默认拒绝；凭据不得入库 |
