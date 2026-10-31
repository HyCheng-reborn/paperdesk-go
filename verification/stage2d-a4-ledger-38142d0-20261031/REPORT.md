# Stage 2D A-4 证据台账与判定口径（离线整理）

- 性质：**只读核对 + 文档记录**。本文件由离线整理产生，**未**连接 ES、**未**调用模型、**未**执行 docker、**未**创建/写入/删除任何资源、**未**提交/推送/合并。
- 对象：HEAD `38142d05d25e762e7058865213b9e667dee980db` 加当前未提交工作区；`git status --porcelain` 实测 62 条（12 条 tracked 修改 + 50 条 untracked，含历史 `verification/` 快照目录）。**非干净提交，也未冻结整个工作区。**
- 日期：2026-10-31（Asia/Shanghai）。
- 上游证据：`verification/stage2d-a4-local-es-38142d0-20261031/REPORT.md`（L0）、`verification/stage2d-a4-l2-38142d0-20261031/REPORT.md`（L2）、`design/stage2d-a4-es-plan.md`（v0.5）、`design/stage2d-a4-l2-component.md`。

## 1. 对象与基线（可核实项）

| 项 | 值 |
|---|---|
| HEAD | `38142d05d25e762e7058865213b9e667dee980db` |
| `core/retriever` 包内 tracked 修改 | **无**（该包真实分支只由 HEAD + 未跟踪测试文件决定） |
| `design/stage2d-plan.md` | tracked 修改中（P-8/§9.3 条款取工作区版本 v1.4） |
| L2 运行时冻结的 3 个文件 | 见 §7 哈希对照（运行对应"改前"列） |
| L0 运行时冻结 | 仅记录 HEAD；未记录工作区 diff 摘要，未冻结文件哈希 |

## 2. 已观察（真实 ES 8.11.3，本机合成索引，非生产）

来源 L0（直接 ES `script_score`，用户授权创建/写入一次后只读）：

- 对照 `content_vector`：HTTP 200，1 命中，`failed_shards=0`、`timed_out=false`、`max_score=2`。
- QA 查询仅把字段换成 `qa_content_vector`：HTTP 400，`error.type=search_phase_execution_exception`，`root_cause.type=script_exception`。
- mapping：`content_vector` = `dense_vector`/`dims=1024`；`qa_content_vector` **不存在**；单分片、0 副本。
- 请求计数：GET 3、`_count` 1、`_search` 2（另有一次创建 200 与一次写入 201，均属授权内的非只读操作）。

来源 L2（绕开 fingerprint/schema 门禁，生产 `newRetriever` + es8 + 本机 fake embedding）：

- 组件报告：`requests=4 refused=0 index_ok=true aliases_empty=true uuid_match=true content_exist=true content_dims=1024 qa_exist=false emb_calls=2 query_vec=e0/1024 observed=true incomplete=false`。
- 对照 `content_vector`：`kind=ok`、`status=200`、`hits=1`。
- QA 查询：`kind=script_rejected`、`status=400`、`hits=0`、`error.type=search_phase_execution_exception`、`root_cause.type=script_exception`、`note=cause_not_attributed`。
- 错误**透传为** `*types.ElasticsearchError`（`Status`/`ErrorCause.Type` 保留），**未静默变成空结果**。
- 本轮 ES 请求合计 5（前置 UUID GET 1 + 组件 2 GET + 2 `_search`），无重试，未写入。
- **UUID 来源与一致性（更正）**：L2 本轮**先发一次只读 GET 取得完整 UUID**（仅进程内使用，不输出、不归档），再与组件预检那次 GET 的结果逐字节比较，得 `uuid_match=true`。报告记录了两者的先后顺序与请求计数，因此 **L2 内部“前置 GET ↔ 组件预检 GET”的一致性有记录**。**真正不可证的只是 L0 与 L2 是否同一 incarnation**（L0 未保存完整 UUID），见 §5-G1。

## 3. 离线/静态事实（不是真实行为）

| 结论 | 依据 |
|---|---|
| 请求形状为 `script_score` + `cosineSimilarity(params.embedding,'<field>')+1.0`、`size=TopK`、`bool.filter` `_knowledge_name`，**不是**原生 `knn` | `core/retriever/retriever.go`（es8 `SearchModeDenseVectorSimilarity`）、`core/retriever.go#L318-L423` |
| 每条请求只发一次、4xx 不重试、3xx 不跟随、响应体 open/close=1/1 | `elasticsearch.Config.DisableRetry` + 传输层白名单/计数（测试侧 `a4cSender`） |
| QA 路失败即整轮回错误（不静默降级） | `core/retriever.go#L249-L260` |
| 入口门禁：每次 `Rag.Retrieve` 先 `checkFingerprint` | `core/retriever.go#L64-L67` |
| `ReadFingerprint` 走 `ValidateSchemaStrict`，`RequiredVectorFields` 含 `qa_content_vector` → 缺字段即 `required vector field "qa_content_vector" missing` | `core/vector/es.go#L494-L515`、`core/vector/schema_validation.go#L17` |
| 项目自建 mapping **总是**含两个向量字段 | `core/config/vector_schema.go#L10-L31` |
| 缺 `_meta["go-rag-fingerprint"]` → `ErrFingerprintMissing`（更早失败） | `core/vector/es.go#L473-L480` |

**关键推论（静态）**：mapping 缺 `qa_content_vector` 的索引在应用全链（L3）**到不了 ES 查询**，可观测的是门禁错误；因此“经过项目检索器的缺字段行为”只能在组件层（L2）取证。注：另一个**不同样本**——字段存在但**文档无值**（`indexer_async.go` 条件写入 QA 字段）——与本次缺字段样本不是同一情形，**不在本台账已观察范围**，也不用于补强现有结论；是否新增该变体及其写入/查询范围，另行决定（见 §5-G3、§8）。

## 4. 验收条款逐条映射

| 条款 | 要求 | 现状 | 依据 / 缺口 |
|---|---|---|---|
| `design/stage2-plan.md` §8.8 | 缺 `qa_content_vector` 时 ES 行为待确认（报错？空结果？） | **部分覆盖** | 已观察 400 `script_exception`（且为 `script_score` DSL，非 `knn` 字段）；本样本为“缺字段”，**不含**“字段存在、文档无值”变体（属另一情形，是否验证另行决定） |
| `design/stage2-plan.md` §9.1（2D 行） | "A-4 QA 向量行为记录" | **已记录** | L0 + L2；但记录 ≠ 2D 通过 |
| `design/stage2d-plan.md` §2 P-8 通过标准 | 行为被观测并记录；**与检索器错误路径一致** | **组件级满足**；应用侧按 §6 口径判读 | L2 错误透传无静默；L3 不可达（门禁先拒，静态+离线 fake） |
| P-8 失败条件 | 静默错误结果 → 缺陷候选 | **未触发** | QA 查询返回错误而非 0 条成功结果 |
| `design/stage2d-plan.md` §5.2 | P-8 属阶段 2，单独授权，不与付费模型混排 | 满足 | L0 准备阶段经授权创建索引并写入一条合成文档；L0 查询阶段、本轮整理与 L2 均只读，均无模型调用 |
| `design/stage2d-plan.md` §9.3 | 不得写成"真实 ES 已验证通过" | 维持 | A-4 仍有缺口 G1–G7（其中 G3 属另一变体，是否验证另行决定） |
| `PROJECT_PROGRESS.md` L88 | 缺字段时返回空/错误且 **panic-safe**、行为可解释并文档化；ES mapping + **检索往返**；**不以离线 fake 替代** | **部分覆盖** | 检索往返与非 fake：满足（组件层）；panic-safe：无专门断言（G6）；"可解释"：`cause_not_attributed` 未归因（G7）；应用侧为门禁先拒 |

## 5. 证据缺口（本轮均不执行）

| # | 缺口 | 关闭所需（在线操作需另行授权；离线项按任务范围执行） |
|---|---|---|
| G1 | **跨运行 incarnation** 不可证：L0 未保存完整 UUID，无法证明 L0 与 L2 指向同一物理索引。**L2 自身的“前置 GET ↔ 组件预检 GET”已逐字节一致（`uuid_match=true`），该一致性有记录，不属缺口** | 若要尝试关联 L0：需 1 次只读 `GET /<index>` 并接受“UUID 不落盘”；但 L0 时点已无法回溯，最多只能说明“当前值”，不能回填 L0 |
| G2 | 无逐字运行日志归档；L2 单次 `-count=1`，无重复一致性 | 重跑一次 L2：1 次 UUID GET + ≤4 条组件请求，脱敏 `go test -v` 输出落盘 |
| G3 | **另一变体**（字段存在、文档无值）零证据；与本次缺字段样本情形不同，既不用于否定也不用于补强本次观察 | 是否验证、以及所需写入/查询范围，需单独决定与授权（可能涉及新建索引或写入文档） |
| G4 | 服务端侧请求计数未取证（只有测试侧计数） | ES 日志/`_stats` 只读访问，或授权的额外只读请求 |
| G5 | 容器 `stage2d-a4-es` 与索引/文档存活状态未核实 | 只读 `docker ps`/`GET /<index>/_count`（本轮明确未执行） |
| G6 | `panic-safe` 无专门断言（仅"未发生 panic 导致失败"） | 离线可补：组件层 400 路径加 recover/panic 断言（无网络） |
| G7 | 400 与“缺字段”的因果未归因（`cause_not_attributed`）：不影响“已报错且未静默”这一已观察事实，只影响对成因的措辞 | 若需归因，需对照样本（如字段存在但文档无值，或补齐字段后同查询）；通常与 G3 同批，属可选增强 |

## 6. 判定口径与状态声明

- **判定口径（用户 2026-10-31 确认）**：P-8 的"与检索器错误路径一致"按**组件级**判读——L2 已观察到错误透传、无重试、无静默空结果；同时"缺字段索引在应用侧被 fingerprint/schema 门禁先拒、L3 不可达"作为**设计事实**（静态 + 离线 fake 依据）记录在案。该口径**不**等于 L3 已验证，也**不**改写任何通过状态。
- **不得外推**：不得写成"A-4 通过""Stage 2D 通过""应用返回 0 条""参与 RRF/rerank 已验证""其他 ES 版本/多分片/真实数据量下同样行为"。
- **状态**：`PROJECT_PROGRESS.md` / `CHANGES.md` 中 A-4 仍为"未验证 / 保留边界"；Stage 2D 其他项（E-1/E-2/I-1/I-2、D-7）未执行，本轮不改变。
- 资源：合成索引 `a4-probe-38142d0-20261031` 与容器 `stage2d-a4-es` 按此前授权**保留**；本轮未访问、未删除。

## 7. 本轮离线变更与验证结果

### 7.1 代码（测试侧，唯一代码改动）

`server/core/retriever/a4_component_real_branch_test.go`：

- `a4cResolveRealTarget`：`A4_COMPONENT_INDEX_UUID` **不再 `TrimSpace` 后使用**；原始值含首尾空白立即 `refused`（fail closed），内部空白沿用原拒绝分支。比对仍是逐字节 `==`。
- `TestA4Component_TargetGate`：新增 `uuid leading space`、`uuid trailing space`、`uuid internal whitespace` 三个拒绝子用例。现为 **13 条**（源码 `refused` 表 13 项，`go test -v` 实测 13 条）；减去本轮新增 3 条可推得本轮前为 10 条，故原记的“11”与新记的“14”**均与源码不符**（旧字节未留存，无法独立核验），口径更正见 §9。
- **未改动** `TestA4ComponentRealBranch_IndexPrecheckUUIDExactComparison`：该用例通过 `a4cOfflineTarget` 直接构造 `a4RealTarget`，不经过环境变量开关，其"大写+尾随空格"子用例断言的是**预检比对**（`uuid_mismatch`、`served=1`），与开关阶段无关，保留原断言覆盖不被削弱。
- 未触及真实分支的分类表、限额 4、白名单、`DisableRetry`、`_source` 行为。

### 7.2 离线验证（无网络、无 ES、未设置任何 `A4_COMPONENT_*`）

| 命令 | 结果 |
|---|---|
| `go vet ./core/retriever` | exit 0，无输出 |
| `go test ./core/retriever -run '^TestA4Component' -count=1 -timeout 150s -v` | PASS；`ok ... 1.316s`；`TestA4Component_RealESTarget` **SKIP** |
| `go test ./core/retriever -run '^(TestA4Component_RealESTarget\|TestA4Component_TargetGate\|TestA4ComponentRealBranch_IndexPrecheckUUIDExactComparison)$' -v` | `--- SKIP: TestA4Component_RealESTarget`；`TargetGate` 13/13 PASS（`-v` 实测 13 条子用例，含 3 个新增空白用例）；UUID 精确比较子用例 PASS |
| `go test -race -p 1 ./core/retriever ./core -run '^TestA4Component' -count=1 -timeout 300s`（MSYS2 UCRT64 gcc） | exit 0；`ok .../core/retriever 2.790s`、`ok .../core 2.652s`，无 race 报告 |

### 7.3 哈希对照（SHA256）

| 文件 | 改前（L2 运行时 / 冻结值） | 改后（本轮） | 差异性质 |
|---|---|---|---|
| `server/core/retriever/a4_component_real_branch_test.go` | `FDA247370C6FFA47028771978B974AF0E8D8CAD8BFB6841E67AFBA5C9CF0E834` | `5E305791055D259CDD168B41160A75716B2F7785A94B8B57A796F6F4C6FA43C3` | 仅目标开关 UUID 空白拒绝 + 3 个拒绝子用例 |
| `design/stage2d-a4-l2-component.md` | `99D512CF21F95A17E02F34EBE9185B1AC684547363064EA1DCED5BB504B3BAD3` | `4B82C5448DE7AF9FA4C66CCA1DE47F66080CCE61CF2BDB9F637764435F0EF6D5` | 仅状态注记 + UUID 口径措辞 + 子用例计数 |
| `design/stage2d-a4-es-plan.md` | `FD5C624F5D2899745F81B8FC19FF68A7E771FF4536BF3B03FA186DBF85B3893B` | `8D31069E811690B58C5E4542D0984182C1AB5CC9BA0C0DE4DB61DD504BDE3921` | 仅 v0.5 状态注记 + L2 行/硬约束第 3 条改写 |
| `server/core/retriever/a4_component_entry_test.go` | `23F903E1CB04D0663EF92EF752C47FA0A7EABF1A24194804AD65FDF5F72C239F` | 未改动（同值） | — |

**绑定关系说明**：L2 那次真实运行对应上表"改前"列；本轮**未重跑真实 L2**，因此不产生新的"运行—文件"绑定。改动限测试侧开关与文档措辞，未影响真实分支的分类/限额逻辑；**后续若再次真实运行，必须重新冻结当时的文件哈希**并记录到新报告。

## 8. 下一步（仅清单，未执行）

当前“缺字段”样本的证据已整理完毕，下列各项均需单独决定/授权，**不按优先级自动推进**：

| 待定项 | 性质 | 说明 |
|---|---|---|
| G3（+可选 G7） | **待决定是否新增** | “字段存在、文档无值”是**另一变体**，与当前缺字段样本不同；是否做、写入与查询范围、是否顺带做因果归因（G7），一并由你单独决定 |
| G2 | 证据增强 | 重跑一次 L2 归档脱敏逐字日志（1 次 UUID GET + ≤4 条组件请求） |
| G1/G5 | 只读核实 | 取当前索引 UUID（仅能说明“当前值”，无法回填 L0）与容器/文档存活 |
| G6 | 离线可做 | 补 panic-safe 断言（无网络，另开一轮即可） |
| G4 | 证据增强 | 服务端侧请求计数（需 ES 日志或额外只读请求） |
| Stage 2D 其他项 | 另属批次 | E-1/E-2/I-1/I-2、D-7 属 S-2 阶段 1/2，未授权、未执行 |

以上任何一项执行前需单独明确范围；本文件不构成 A-4 或 Stage 2D 的通过结论，也未更新任何验收状态。

## 9. 修订记录

- 2026-10-31（初版）：建立三档证据台账与缺口 G1–G7。
- 2026-10-31（据用户补充更正）：① G1 拆开两件事——**L2 内部“前置 GET ↔ 组件预检 GET”一致性已有记录**（`uuid_match=true`），不可证的只有 **L0 ↔ L2 跨运行 incarnation**；② “字段存在、文档无值”重新定性为**与当前样本不同的另一变体**，不进本台账已观察范围，也不作为已推荐的下一步，是否新增及范围另行决定。
- 2026-10-31（独立复核更正）：验收表明确 L0 准备阶段曾按授权创建索引并写入一条合成文档；查询阶段与 L2 为只读。缺口表区分在线操作授权与离线工作。
- 2026-10-31（交付口径更正，据独立复核）：① `TestA4Component_TargetGate` 子用例数按**源码 `refused` 表 13 项与 `-v` 实测 13 条**；§7.1/§7.2 的 14 与更早的 11 **都不成立**，已改为 13（减本轮新增 3 条反推为 10，但**旧字节未留存，该反推不当作已核验事实**）。② §7.3 中 `design/stage2d-a4-l2-component.md` 的“改后”值 `4B82C544…` 是**当时那轮**的记录；本次口径更正后当前值为 `746A89FC065E624C426AE5BC8C6FCC58951B3A056F1F38F30494CE7A38764319`，两者不可混用。③ 本文件自身哈希随本次修改变化（旧值 `7C7E8479B92CCA484C8C17A8686D644D13661BCE6974FC6BEF4D0BF1759B0D46` 见快照 v1）。
