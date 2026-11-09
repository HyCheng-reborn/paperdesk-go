# Stage 2D 在线探针方案与预算草案（v0.9，待审阅，未执行）

> **性质**：待审阅草案。**离线方案与预算准备，不构成验收，不授权任何在线调用。**
> **日期**：2026-10-28，Asia/Shanghai。v0.2：按第一轮审阅意见修订——ES 显式创建权限与自动创建行为拆分、前置条件按范围拆分、预算与费用区分、workspace ID 条件化、`SupportsDimensionsRequest` 三类事项拆分及相关冲突消除。v0.3：按第二轮审阅意见修订——S-1 实际验证链两种方案对比、S-2 循环前提拆分（验证阶段/受控端到端阶段）、预算核心能力与探针接线区分、旧日期模型资料标注、相关冲突消除。v0.4：仅消除两处内部冲突——§4.1 允许 S-1 使用本机 fake ES 并明确仍禁止真实 ES；§5.2 区分 D-7 的"执行前可核实的版本信息"与"只有阶段 2 才能验证的客户端行为"。v0.4 不改变任何授权、预算、验收状态。v0.5：按 `verification/independent-2d-7a30899-20261028/REPORT.md` 的五项问题修订方案 A 离线骨架——默认构建不再存在远端开关（远端能力改由 `//go:build online` 决定）、在线远端强制 HTTPS 且禁止端点 userinfo、被禁止的重定向不再被当作传输错误重试、入口在第一步发送前完成与 Readiness 同源的完整预检、证据只写脱敏的错误类别与状态码；同时把 "骨架未实现/待准备" 改为准确的实现状态边界（§2.1、§3.2.2、§7、§9）。v0.5 不改变任何授权、预算与验收状态，也不表示独立复核已通过。v0.6：按 `verification/independent-2d-3aea024-20261028/REPORT.md` 的五项增量问题加固——online 构建的 `NewChain`（及其后的 `Run`/`RunStep`）与 `RunOnline` 共用 S-1 授权与环境变量凭据边界、全局停止与新发送/重试在同一锁内协调并取消在途尝试、预检复用生产 builder 的角色模型身份校验（含 `m@v1`）、`NewChain`/入口对可变计划与步骤做防御性深拷贝、凭据就绪与执行共用同一判定；v0.6 同样不改变任何授权、预算与验收状态。v0.7：按 `verification/independent-2d-bdd3a6d-20261028/REPORT.md` 的三项问题加固——把“停止标记后零新发送/新重试”的准入下沉到统一传输层（`sendGate` 的 `commitSend` 与 `stop` 构成 RWMutex 全序，区分“已准入/已经在途”，在途只取消 ctx、不声称可撤销）、预检复用生产 `rerank.ValidateInput` 以在第一次模型发送前拒绝 nil/空文档（保留 `ErrEmptyStep` 与 `rerank.ErrInvalidInput` 错误链）、`Plan.Validate` 复用生产的具体物理名校验与完整期望 fingerprint 比较（`bad,*` 与不匹配 similarity 在 Readiness 与执行入口一致拒绝）。v0.7 同样不改变任何授权、预算与验收状态，也不表示独立复核已通过。v0.8：按 `verification/independent-2d-1e81aa4-20261028/REPORT.md` 的四项问题修正——把发送拆为四个可区分状态（入口已准入 / 已通过闸门 / 已交给 transport / 对端已收到），新增 `StepReport.GateAdmissions`/`TransportCalls` 并把 `Attempts` 明确为“预算已承诺次数”（不是实际发送，也不是对端命中）；闸门拒绝时预算仍保守结算、lease 收敛、`errors.Is` 链保留；模型名与 similarity/provider 的非规范输入（首尾空白/大写）在 Plan.Validate、Readiness、NewChain 与 fake ES 绑定入口一致拒绝，不改已验收的 fingerprint 格式；更正证据表述（fake ES 是 httptest 本机 HTTP，不经 sendGate，不证明真实 ES 行为）。v0.8 仍**不能保证**字面“停止后零新发送”：已过闸门、未 handoff 的请求仍可能稍后发送，该窗口保留为待用户决定的选项（§9.1），本批不声称该契约已通过。v0.9：按 `verification/independent-2d-91c872-20261029/REPORT.md` 的三项问题处理——修复真实注册的百炼 embedding 角色不满足 `providers.ProfiledEmbedRole` 导致 `NewChain` 恒失败的前置遗留（`bailian.validatedEmbedRole` 新增防御性 `Profile()`，身份门禁不放宽）；如实更正“停止标记后零新预算提交”的过强表述，把“已承诺、随后被闸门拒绝”的保守承诺与发送/对端事实分开，新增确定性特征化测试并保留为待决边界（预算侧参与记为 B+，未实现）；交付停止语义方案 B 的离线设计 `design/stage2d-stop-b-plan.md`（线性化点、有界等待、超时未确认、A/C 对比），**未实现、未批准、不表示契约已闭合**。
> **固定基线**：Stage 2C-3 独立复核通过目标 `a12af924f0441363b46e0fec41fca29bdc30993a`（2026-10-28 16:30 +0800，35 文件，父提交 `2c952ce`）。S-1 离线骨架的实现起点为 `7a30899b0e205ad714b5a8ac4db152eb200bcb76`（父提交 `a12af92`）。后续执行时以当时批准的固定提交重新核对，不自动采用最新 HEAD。
> **本文不修改**：生产代码、门禁状态、`PROJECT_PROGRESS.md`/`CHANGES.md` 验收表述、既有 `verification/` 证据与审查 worktree。

---

## 1. 依据（已只读核实）

| # | 依据 | 出处 |
|---|---|---|
| 1 | 离线复核结论："可进行 2D 的**离线方案和预算准备**，实际在线探针应另行获得明确授权"；本次复核不授权在线调用，不代表 Stage 2 完成 | `verification/independent-2c3-a12af92-20261028/REPORT.md` §结论、§剩余边界与下一步 |
| 2 | 带版本模型身份**当前禁用**：`providers.RequestModelID` 对合法带版本身份（`m@v1`）返回 `ErrModelVersionNotExpressible`，在生成期望指纹前提前拒绝、不产生请求。该设计保守禁用，**不证明真实服务是否支持版本参数**，不能写成真实能力已验证 | 同上 §修复复核 1；`server/core/providers/profile.go:95-118` |
| 3 | 归 2D 或部署验证且在**单独授权**后才可进行的事项清单（真实模型 API 调用、真实 ES/Qdrant/MySQL 端到端、ES 权限与"禁止自动创建索引"验证、`SupportsDimensionsRequest` 真实模型能力验证、在线 usage/budget 监控） | `design/stage2c3-plan.md` §11 |
| 4 | 2D 原始方案基线（最小调用矩阵、停止条件、脱敏证据、无凭据时保持未执行） | `design/stage2-plan.md` §8 |
| 5 | 待探针确认项清单（含 ES kNN 缺字段行为、ES 客户端/服务端兼容归属 0C/启动验证） | `design/stage2-plan.md` §8.8、§8.9、§10.3 |
| 6 | 百炼官方文档口径（核查日期 2026-10-19，**旧日期资料**）：`text-embedding-v4` 默认 1024 维、单次 ≤10 条、单条 ≤8192 token；Chat/Embedding OpenAI 兼容 endpoint；qwen3-rerank 与 gte-rerank-v2 两种路径。**均为资料口径，不是本项目在线实测事实**；任何在线执行前须重新核实（见 §3.1） | `design/stage2-plan.md` §6、§10.1 |
| 7 | 预算配置结构已存在：`ProviderConfig.MaxRequests/MaxInputTokens/MaxOutputTokens/MaxConcurrency`（YAML `provider.*`）；`providers.NewProcessBudget(maxRequests, maxInputTokens, maxOutputTokens int64, maxConcurrency int)`，`<=0` 表示该维度不限、负值拒绝 `ErrBudgetInvalidOption` | `server/core/config/config.go:57-78`；`server/core/providers/budget.go:146-161` |
| 8 | `SupportsDimensionsRequest` 是 YAML 本地声明，非真实能力证明；缺失默认 `false`（不下发 dimensions 参数），严格布尔解析 | `server/core/config/config.go:75-77`；`design/stage2c3-plan.md` D3；`server/core/providers/adapter.go` `BuildEmbedRole` |
| 9 | 受控生命周期既有契约：`vector.CheckManagedLifecycle`、`vector.ValidatePhysicalName`、`vector.BindPhysicalTarget`（`server/core/vector/target.go:20/39/143`）、`config.VectorIOGuard`（`server/core/config/config.go:23`）、`ErrResourceBindingNotReady`（Qdrant 生产绑定拒绝） | 左列文件行号，均在固定提交树中核实 |
| 10 | 在线测试隔离先例：`//go:build online`（`server/core/websearch/youcom_integration_test.go`、`server/core/rerank/rerank_test.go`）、`//go:build integration && online`（`server/core/rag_test.go`） | 左列文件第 1 行 |
| 11 | 独立复核第三项已证明的离线预算语义（真实 `core.New → VerifyFingerprint → IndexAsync` 链，进程上限 1：QA 耗尽后 embedding 被拒、bulk 未发生、committed 各 1、in-flight 0）——2D 在线预算链判据沿用该语义 | 复核报告 §修复复核 3 |
| 12 | 受控**显式创建**索引的代码路径：`defaultRagFactory.Create` 在 `!IndexExists` 时调用 `vs.CreateIndex(ctx, name, fp)`（`server/internal/logic/rag/retriever.go:139-150`；`server/core/vector/es.go:65`），该请求需要账号具备显式 create 权限 | 固定提交 `a12af92` 代码树只读核实 |
| 13 | **自动创建**是 ES 服务端行为：写入（index/bulk）落到不存在索引时由集群设置 `action.auto_create_index` 决定是否隐式建索引；本仓客户端代码不读取也不设置该集群级设置。因此"一次显式 create 请求被拒绝"不能证明自动创建已禁用（被拒也可能仅因账号无 create 权限，两者是不同控制面） | 代码事实（§1/12 只读核查的补集：无集群设置读取路径）；`action.auto_create_index` 属 ES 部署侧事实，本项目**未核实**，不写成已验证 |
| 14 | 生产 Provider embedding 直接入口 `common.NewEmbedding → newProviderEmbedding` 的硬前提：`conf.Registry` 已注入、`ManagedResourceLifecycle==true`、`IndexName` 非空、`verifiedTargetBinding` 通过——绑定只能由 `vector.BindPhysicalTarget` 对**可读取的 ES 目标**产出（私有字段不可伪造，零值/手工值 `Check` 必败），且构造期一次重新读取 + 每次 embedding 前后 guard 再次读取目标。即：**没有任一 ES 目标可读时，生产链 embedder 必然 `ErrEmbeddingProfileNotReady` fail closed** | `server/core/common/embedding.go:101-282`；`server/core/vector/target.go:46-180`（固定提交 `a12af92` 只读核实） |
| 15 | 本机 fake ES 建立真实绑定的离线先例已存在：`httptest` 假服务器（含 `X-Elastic-Product` 头）承载索引元数据，`BindPhysicalTarget` 对其产出通过校验的 `TargetBinding`，`guardedEmbedder` 的 guard 读取的是该本地 HTTP 目标 | `server/core/common/binding_testenv_test.go:17-97`、`bindTestTarget:99-117`（先例为实现者测试辅助，本次仅核实其存在与机制，不扩大其结论） |
| 16 | 绕过绑定链的直连构造入口存在：`providers.BuildEmbedRole/BuildChatRole/BuildRerankRole` 只需 Profile+Transport（凭据经 `BuildConfig.APIKey` 或调用方提供），**不需要任何 ES 绑定**；版本身份拒绝（`p.RequestModel()`）在该层即生效 | `server/core/providers/adapter.go:75-103/147-180/217-242`（只读核实） |
| 17 | 预算接线的生产现状是**组件级支持**：`BuildConfig.Budget==nil` 时跳过预算包装；`!=nil` 时角色被 `budgetEmbedRole/budgetChatRole/budgetRerankRole` 包装（调用时从 ctx 复用 `RequestBudget` 或新建）；进程级持久化仅存在于生产工厂链 `buildRegistry`（包级单例）；**在线探针自身的入口/顺序/停止逻辑（runbook、骨架）当前无任何实现** | `server/core/providers/adapter.go:30-45/99-101/176-178/238-240`；`server/core/providers/budget_adapter.go:145-148/232-235/288-291`；`server/internal/logic/rag/retriever.go:206-217`（只读核实） |

**资料与实测严格分栏**：本文所有引用官方文档的数值一律标注"资料口径（2026-10-19 核查）"；只有 2D 在线探针实际执行并留证后，才能改标"在线实测"。所有 2026-10-19 口径项均为**旧日期资料**：在线执行/授权前需重新核实并更新核查日期记录，旧口径仅留作历史对照，不代表当前可用性。实现者自测（`verification/stage2c3-fix2/` 等）与独立实测分层记录，不混写。

---

## 2. 2D 要验证的问题、前置条件与通过/失败判据

**通用前置条件**（任一不满足 → 所有在线项保持"未执行"）：

- A. 用户对本文的**在线执行**另行给出明确授权，且授权须声明所属范围（S-1/S-2/S-3，见下表）与预算数值（§6 U-3）。
- B. 该范围内列明的部署前提（§5）逐项核实通过并留证；不在该范围内的前提不构成本次执行阻塞。
- C. 所需凭据仅经环境变量注入；不存在可用凭据或未获授权 → 按 §7"离线可做"处理，不编造结果。

**按范围拆分的前置条件**（v0.2，审阅意见 2：仅模型探针不无条件依赖真实 ES 验证）：

| 范围 | 覆盖验证项 | 必需前提 | 明确不需要 |
|---|---|---|---|
| **S-1 模型直连探针** | P-1..P-7 | 模型 API 凭据（U-1/U-5）；§3.2/§3.2.1 预算数值确定（U-3）；§2.1 链方案选定（U-6）；§5 中 D-4/D-6 确认 | **不要求**真实 ES 部署验证（D-1a/D-1b/D-2、§5.1 矩阵、D-7）先行或通过；P-1..P-7 只调用模型 HTTP 入口（或仅本机假目标，见 §2.1），不写任何真实向量库 |
| **S-2 真实 ES 部署验证** | 阶段 1（验证）：§5.1 矩阵 E-1/E-2/I-1/I-2；阶段 2（受控端到端）：（可选）P-8、D-7（拆分见 §5.2） | 阶段 1 只需 §5.2 的"验证阶段最小前提"；阶段 2 额外要求阶段 1 结果满足要求 | **不需要**付费模型调用，不消耗模型预算 |
| **S-3 组合端到端** | 含任何"真实模型输出写入真实 ES 受控索引"的链路 | S-1（含 §2.1 选定方案准备完成）与 S-2 **阶段 1 验证完成且结果可接受**（§5.2）同时满足，且另行给出 S-3 授权 | — |

各范围**独立授权、独立证据目录、独立结论**：S-1 通过/失败不推导 S-2/S-3 的任何结论，反之亦然（复核报告口径：真实设施与在线维度未验证，不因离线或单范围结果扩大）。P-5 在 S-1 内仅覆盖模型 HTTP 链的预算语义；写入侧预算链属 S-3。ES 地址与账号事实由用户在授权时提供（见 §6 U-2 附注），不改变"S-1 不依赖 ES 验证"的边界。v0.3 补充：S-2 内部也分两个阶段，避免"执行验证前需先有验证结论"的循环前提（§5.2）。

### 2.1 S-1 实际验证链：两种可行方案（方案 A 已离线实现，方案 B 未实现）

代码事实（§1/14）：生产 Provider embedding 入口 `common.NewEmbedding` 必须有可信物理绑定，而绑定必须对可读的 ES 目标执行 I/O 才能建立与维持（guard 在每次 embedding 前后再次读取目标）。因此 **"S-1 不连接任何 ES"与"走生产完整链"不能同时成立**，必须二选一。v0.5 状态：方案 A 的**离线**骨架已实现并离线验证（`server/core/probe`，证据见 §9），仍待独立复核、真实在线未执行；方案 B 仍未实现。本文不声称任一方案已在线可用：

| 维度 | **方案 A：本机 fake ES 建立真实绑定 + 真实模型** | **方案 B：仅 Provider adapter 直连真实模型** |
|---|---|---|
| 构造方式 | 复用既有机制先例（§1/15，实现者为测试辅助实现；探针需一个**非 test 包**的入口，属待准备项）：本机 `httptest`/本地受控假目标承载一次性索引元数据 → `BindPhysicalTarget` 产出真实 `TargetBinding` → `defaultRagFactory` 同款顺序或直接入口注入 conf → `common.NewEmbedding` → 真实模型 HTTP | `NewProviderProfile → BuildEmbedRole/BuildChatRole/BuildRerankRole`（§1/16）直接持有角色并发起真实请求，不经 `common.NewEmbedding` |
| 可证明 | P-1/P-2/P-3/P-4 的**响应形状与数值事实**（维度/数量/index 映射/usage）在完整生产 guard 链下成立；`guardedEmbedder` 包装、guard before/after、身份比较链被实际执行（执行的是真实生产代码，读取的是本地假目标） | 响应形状与数值事实；`BuildEmbedRole` 的 dimensions 下发行为（D3）；`RequestModel` 版本拒绝在发送前生效（P-6）；可选 `BuildConfig.Budget` 路径（仍受 §3.2.2 接线验收约束） |
| 不可证明 | 真实 ES 的任何事实：D-1a/D-1b、真实 mapping/kNN 行为、资源替换检测在真实后端上的有效性（假目标不替代 S-2）；"绑定所指向的生产资源安全"这一前提本身 | 生产绑定/就绪链完全未覆盖：`verifiedTargetBinding`、`BindPhysicalTarget`、guard before/after、`ManagedResourceLifecycle`/`IndexName`/Registry 就绪判定、direct-entry 身份比较（`checkBindingIdentity`/`ErrEmbeddingIdentityMismatch`）、`filterEmbeddingOptions` 模型覆盖拒绝的生产包装位置——以上均不随 B 的通过获得任何结论 |
| 所需离线准备（v0.5：方案 A 已实现离线骨架，方案 B 仍待准备） | v0.5 已实现：非 test 包的本机假 ES 绑定入口（`LocalFakeESTarget`）、一次性命名与期望 fingerprint 注入、guard 读取次数计数、三角色 adapter 与预算 owner 接线；离线计数证明构建期 2 次读取 + 每次 embedding 前后各 1 次 guard 读取。仍待准备：真实模型 baseURL 指向 U-1 endpoint 的在线取值（U-1 未定） | 探针入口构造 Profile/Transport/Budget 的显式装配（不碰 `buildRegistry` 包级单例，避免进程预算语义纠缠，见 §1/17）；预算 ctx owner 接线；响应校验链复用说明（B 方案下 `guardedEmbedder` 不在链上，P-1 判据需由探针自身的数量/维度/有限值检查实现并在 CLAIMS 中注明与生产链差异） |
| 证据 | 绑定建立/guard 读取计数日志（本地 HTTP，无真实 ES）+ 模型响应摘要 + 预算计数，分目标类型标注"本地假目标" | 请求/响应摘要 + 预算计数；CLAIMS 中显式列出"未覆盖链"清单（即本行"不可证明"列，不得省略） |

**方案决定 = U-6 待用户选择**（也可 A/B 分别执行、分别记录）。未选定并准备完成前，S-1 内 Embedding 侧在线项（P-1/P-2 及 P-5/P-6/P-7 的 embedding 部分）不得执行；Chat/Rerank 不受此纠缠（adapter 直连即它们的生产路径）。无论选哪种，"在线结论不覆盖 ES 部署事实"不变；方案 A 不得被表述为"真实 ES 已验证"。v0.5：方案 A 仅完成**离线**骨架与离线验证（§9），U-6 的**在线执行**选择与授权仍待用户决定。

| 编号 | 验证问题 | 通过判据 | 失败判据 | 需保留证据 |
|---|---|---|---|---|
| P-1 | Embedding 在线维度一致性：配置声明维度（`provider.embedding_dimensions`）与实际返回向量长度一致 | `guardedEmbedder`（数量/维度/有限值校验，`server/core/common/embedding.go`）对真实响应全链无拒绝；每条响应 `len(vector) == 声明维度` | 任一向量长度 ≠ 声明，或出现 NaN/Inf/数量不符 → **立即全停**，记录脱敏摘要 | 请求文本数、响应向量长度分布、声明值、拒绝/通过计数（无 Key、无完整向量内容） |
| P-2 | Embedding 批次行为：请求数量与 `data[].index` 顺序对应 | 响应 `data` 长度 == 请求 `input` 长度；`data[i].index` 与输入下标一致，不依赖隐式顺序 | 数量/映射不符 → 停止 | 请求条数、index 映射核对结果。官方"≤10 条/单条 8192 token"仅为**资料口径**，实际边界以在线核实为准（V-2 待核实） |
| P-3 | Rerank 实际响应映射 | 响应通过已验收校验链（index/score/NaN/Inf/重复/越界、响应体限制、有界超时，`server/core/rerank/rerank.go` 契约不变） | 校验链任一拒绝且非预期 → 停止 | 文档数、返回条数、score 有限性摘要（不记录语料原文） |
| P-4 | Chat usage 格式：非流式 `response.usage`；流式 `stream_options.include_usage` 末 chunk usage | 两条路径 usage 字段可解析并计入结算账本 | usage 缺失 → 按既定语义记 `unknown` + 保守保留预留（**不把 unknown 当 0**），如实记录该在线行为，不判为代码失败 | usage 存在性、token 计数、路径（流式/非流式）摘要 |
| P-5 | 在线预算真实链：预留→承诺→结算在真实发送上的语义 | 每次真实 HTTP 尝试前有预留；`ErrBudgetExhausted`（`budget.go:17`）触发后**零新发送**；request/process committed 计数与停止时刻观测一致；结束后 in-flight=0（语义基线见 §1/11） | 出现"预算耗尽仍发送"或计数与观测不一致 → 阻塞级失败，立即全停 | 预算上限、预留/承诺/结算计数、耗尽错误发生次数 |
| P-6 | 带版本模型身份**当前禁用行为**回归：带 `@v` 身份在发送前被 `ErrModelVersionNotExpressible` 拒绝且无请求产生 | 在线入口同样提前拒绝、零请求 | 若请求被发出 → 阻塞级失败 | 拒绝错误、零发送观测 |
| P-7 | `SupportsDimensionsRequest` 声明 vs 服务实际行为（仅当授权范围包含，V-3） | 二维结论冻结：声明 `true` 时服务是否接受 dimensions 参数、声明 `false` 时省略参数返回何维度 | 声明与实际漂移（如声明 true 但服务忽略参数、返回维度≠声明）→ 停止并转入 U-4 决策 | 两组请求的差异与实际返回维度。**未获授权前此项保持"未验证"，不得写成已验证** |
| P-8 | （可选，范围由 U-2 决定）A-4：QA 关闭时 `qa_content_vector` 字段缺失场景下真实 ES kNN 行为 | 行为被观测并记录（报错/空结果/其他），与检索器错误路径一致 | 行为导致静默错误结果 → 记录为缺陷候选，不自行修复 | ES 响应/错误摘要。**属真实 ES 验证项，不与付费模型调用混排为同一授权** |

**未决问题（明确不宣称已验证）**：真实服务是否支持带版本模型身份或版本参数（P-6 只验证本地拒绝行为）；Qdrant incarnation/namespace（2D 不验证、不解除）；text-embedding-v4 等候选模型能力细节；ES 自动创建禁用与显式 create 权限的实际部署状态（属 §5.1 待授权验证项，不预先写成已验证）；S-1 采用方案 A 还是 B（U-6，两者验证链覆盖面不同，见 §2.1）；探针实际预算接线（§3.2.2，离线验收完成前不得声称"每次尝试都经预算"）。

---

## 3. 模型、请求次数与预算计划

### 3.1 候选与参数（全部待决定，不猜测）

| 角色 | 候选（资料口径，2026-10-19 核查，非本项目实测；旧日期资料，执行前须重新核实） | 探针实际 model ID / endpoint 地域 / 工作空间 |
|---|---|---|
| Chat | `qwen-plus` 或 `qwen-turbo`，`/compatible-mode/v1/chat/completions` | **U-1 待决定** |
| Embedding | `text-embedding-v4`（资料口径：默认 1024 维，64-2048 可选），`/compatible-mode/v1/embeddings` | **U-1 待决定**（含探针维度声明值） |
| Rerank | `qwen3-rerank`（`/compatible-api/v1/reranks`）或 `gte-rerank-v2`（DashScope 原生路径；资料口径 gte-rerank 系列 2026-05-30 下线） | **U-1 待决定** |

工作空间 ID **仅在所选服务形态要求时提供**：按 `design/stage2-plan.md` §8.2 资料口径，它只用于专属域名 base_url 构造；选用公共 `dashscope.aliyuncs.com` endpoint 时不需要。

**旧日期资料的执行前重新核实要求**：上表及 §1/6 全部口径核实于 2026-10-19（含候选模型可用性、下线日期、批次/单条限制、维度范围）。这些是会漂移的事实：任何在线执行或授权决定前，需由用户提供新的官方口径记录并更新核查日期；本文不自行补写当前可用性、价格或模型能力，也不把旧口径当作"当前仍成立"的证据。

不猜测模型能力、维度、价格或服务端行为：上表"资料口径"列仅注明出处与核查日期，实际以 P-1..P-7 在线观测为准。

### 3.2 预算结构（组件机制已存在；探针接线验收见 §3.2.2，数值待决定）

- 进程级：`ProcessBudget`（`NewProcessBudget`，§1/7），探针进程构造一次，不可重置；候选维度：`MaxRequests`、`MaxInputTokens`、`MaxOutputTokens`、`MaxConcurrency`。
- **`0` 的语义（§1/7）**：`<=0` 表示该维度**不限**（`0` 即不限），负值是配置错误被拒绝。因此本文所有"有限上限"待决定值必须以**正整数**给出；**不得以 `0` 冒充有限上限**——把待定维度填 `0` 等于该维度不设防。若某维度确拟不限，须在授权文本中显式写明"该维度不限"并接受对应风险，不默认沉默不限。
- 请求/任务级：每探针步骤经 `WithMaxRequests/WithMaxInputTokens/WithMaxOutputTokens/WithMaxConcurrency/WithDeadline`（`server/core/providers/budget.go:533-552`）构造，累计受进程级约束。
- 每次实际 HTTP 尝试（含重试）在同一 owner 上预留→承诺→结算；重试不刷新累计额度（2A/2B 已验收语义，不重述）。

**建议结构（数值一律 U-3 待决定，不由本文填写）**：

| 参数 | 建议表达式 | 取值 |
|---|---|---|
| 进程 `MaxRequests` | Σ(每探针计划发送次数 + 计划内重试上限)，逐探针列行 | **U-3 待决定** |
| `MaxConcurrency` | 建议 1（串行、可观测、排除并发歧义） | 待 U-3 确认 |
| 单步 `Deadline` | 每角色独立 deadline | **U-3 待决定** |
| token 预留（输入/输出） | 输入按探针固定短文本估算并标注"估算"；输出仅 Chat 需要 | **U-3 待决定** |
| 全局停止阈值（错误率/总时长） | 首个非预期错误即全停（§4.2），错误率>0 即停 | 待 U-3 确认 |

**预算上限与费用金额是两回事（审阅意见 3）**：本项目**未核实**任何定价、计费规则与账号侧配额（均列 V-1 待核实），`ProcessBudget` 也不存在金额维度——因此本文**不承诺任何金额上限**；金额预算与账号侧限额均为待决定/待核实项，由用户在 U-1/U-3 决策时自行设定并确认，不据资料口径推断。下文"预算"一律指代码可执行的请求数/token/并发/时间上限。

### 3.2.1 强制分层：代码执行 vs 运行流程/部署保证（执行前必须全部确定）

| 限制项 | 由代码执行 | 由运行流程/部署保证 |
|---|---|---|
| 请求数上限（进程/请求级，含重试） | `ProcessBudget`/`RequestBudget` 预留→承诺（§1/7） | — |
| token 预留（输入估算/最大输出） | 同上；usage 缺失保守保留 | — |
| 并发 slot | `MaxConcurrency` + 幂等 finalize | — |
| 单步 deadline | `WithDeadline`（budget.go:551-552） | — |
| 探针总时长/总 wall-clock | 否（预算系统无此维度） | runbook：到时停止并取证 |
| 首个非预期错误后不再启动后续步骤（§4.2） | 否（代码只拦截已接线的预留点） | runbook：单进程顺序执行、异常即终止进程 |
| 停止后不重复启动补救性调用 | 否 | 运行流程：一次执行一个证据目录；再启动需新授权 |
| 账号侧配额/限流/金额上限 | 否 | 部署/账号管理（V-1 待核实） |

U-3 数值须逐行覆盖上表"由代码执行"全部四项与"runbook"两项的取值；有限上限以正整数给出（`0`＝不限，见 §3.2，不得以 0 冒充有限上限）。"由代码执行"列的前提是 §3.2.2 探针接线验收通过；未通过前该列只是组件能力，不是对探针实际行为的保证。

### 3.2.2 预算核心能力 ≠ 探针实际接线（v0.3；v0.5：已离线实现并离线验证）

代码事实（§1/17）：预算组件支持预留→承诺→结算、四计数器与幂等 finalize，但这是**组件级能力**：`BuildConfig.Budget` 为 nil 时探针入口可完全绕过预算；`buildRegistry` 的进程预算持久化只覆盖生产工厂链。v0.5 状态：在线探针的入口、装配顺序与停止逻辑（`server/core/probe` 的 `Chain`）已在离线骨架中实现，但**未在线执行**。因此仍不得把组件能力表述为"探针已保证每次 HTTP 尝试、重试、deadline 与停止都经预算组件"，也不得把离线 W-1..W-5 通过表述为在线预算语义已验证。

这些是**离线验收要求**（fake/httptest 证明探针**实际使用链路**，而非组件单测）；v0.5 已在离线验证中逐项覆盖（证据见 §9），但仍待独立复核，且 S-1/S-3 的**在线**执行仍需单独授权：

- W-1：探针链上每一次真实发送均经 `AttemptLease` 预留→承诺（httptest 计数可验证，含重试的每一次额外尝试计入同一 owner）。
- W-2：`ErrBudgetExhausted` 后探针入口零新发送（httptest 服务端计数为 0 的新增请求）。
- W-3：deadline 与并发上限在探针入口生效（非仅 budget 单测）。
- W-4：usage 缺失路径与结算计数可观测、可入证据（unknown 不记 0）。
- W-5：停止逻辑接线：预留失败/非预期错误后探针不再发起后续步骤（§4.2 与 §3.2.1 runbook 行的代码侧支撑）。v0.7 补充：停止的“零新发送”承诺由传输层闸门承担——`beginAttempt` 只表示入口准入，真正的发送准入（`commitSend`）与停止标记发布（`stop`）通过同一把 `RWMutex` 构成全序；停止标记之后尚未过闸门的尝试零字节发出，已过闸门的请求属于“已经在途”，只取消 ctx、不声称可撤销，也不会被重试。

验收证据存离线验证目录（`verification/stage2d-offline-a12af92-20261028/` 与 `verification/stage2d-fix1-7a30899-20261028/`）；**独立复核通过前**，探针链只能标"离线验证通过 / 待独立复核"，不得写成已通过或已在线验证。金额上限与账号侧限制的待核实状态不受本节影响（V-5，本文仍不承诺任何金额）。

### 3.3 待核实项（不自行创造，留待授权执行或资料补证）

- V-1：候选模型的当前可用性/定价/配额（现有口径为 2026-10-19 旧日期资料，执行前需重新核实；需用户提供的账号事实，不由本文推断）。
- V-2：Embedding 批次与单条 token 实际服务边界（现仅为 §1/6 资料口径）。
- V-3：`SupportsDimensionsRequest` 在线核实是否纳入本批范围 → U-2/U-4。
- V-4：ES `action.auto_create_index` 实际值与探针账号权限（§5.1，未核实；不拿配置声明或单次拒绝现象当证据）。
- V-5：定价、计费规则与账号侧配额/限流（不承诺金额上限的前提条件；需用户提供账号侧事实）。

---

## 4. 在线探针请求范围、停止条件、失败处理与证据

### 4.1 请求范围

最小调用矩阵沿 `design/stage2-plan.md` §8.4 结构（S-1 范围）。请求范围仅在与对应授权范围匹配时执行：S-1 执行 P-1..P-7。**S-1 的 ES 侧仅允许 §2.1 方案 A 的进程内本机 fake ES**（`httptest`/本地受控假目标承载一次性索引元数据，仅用于产出真实 `TargetBinding` 并驱动生产 guard/身份检查链）；**任何真实 ES/Qdrant/MySQL 的连接、读写与真实资源操作一律禁止**，fake ES 不产生任何真实 ES 侧事实，不得据此填写 D-1a/D-1b/D-2/D-7，也不得表述为"ES 已验证"。只有 S-3 才允许涉及受控**真实** ES 写入链路。Chat 单轮、Chat 流式、Embedding 单条、Embedding 批量、Rerank 各一次极小请求；输入为固定占位短文本（不含业务语料、不含敏感信息），不跑全语料。每请求前预留，预留失败不发该请求。P-6 为"预期零请求"的验证项（观测拒绝，不发送）。P-7 视授权范围增加一组差异请求。

### 4.2 停止条件（任一触发 → 立即全停后续所有探针）

1. 任一预留失败（`ErrBudgetExhausted` 或 deadline）。
2. 任何非预期错误（含 HTTP 非预期状态、解析失败、校验链拒绝）。
3. 维度/数量/index 映射不一致（P-1/P-2 失败判据）。
4. 用户取消或观测到预算计数与实际发送不一致（P-5 失败判据）。
5. 全部门禁与禁用边界保持：不静默切换 Provider/模型/计费服务（`design/stage2-plan.md` §4.10 禁止静默切换契约）。

### 4.3 失败处理

- 已 committed 尝试：不退款、保守保留预留；未 commit 的预留：回退（`design/stage2-plan.md` §4.5 既有语义，不改动）。
- 停止后：保留全部已产生证据，不做第二次"补救性"调用；任何重试决策需新的明确授权。
- 失败不触发任何代码修改；问题记录进报告，修复另行立项。

### 4.4 证据（新增目录 `verification/stage2d-<sha8>-<日期>/`，不覆盖既有证据）

- `environment.log`：OS/Go/gcc（如 race 需要，沿用 MSYS2 UCRT64 工具链事实）、`GOPROXY`/`GOSUMDB`、凭据存在性（不记值）。
- 每个探针：脱敏请求/响应摘要——维度/计数/index 映射/token 数/耗时/退出码；**无 API Key 明文、无完整模型输出、无业务语料**。
- `results.txt`：命令与逐项退出码；预算结算计数（request/process committed、in-flight 终值）。
- `CLAIMS.md`：**"官方文档口径 vs 在线实测"两栏对照表**，逐条标注哪些判定来自资料、哪些来自本次实测。
- 实现者自测与独立复核分层记录（沿用 2C-3 复核口径），不通过测试总数推断覆盖率。

---

## 5. 部署前提清单（按范围/阶段适用；任一未核实 → 对应在线项禁止执行）

注意区分两类条目：**待核实项**（D-1a/D-1b/D-2 等本身由 §5.1 验证阶段产生证据，不预先要求"已满足"，否则构成循环前提，见 §5.2）与**先决条件**（执行验证前就必须成立的事实/资源，如 ES 地址、账号、一次性命名授权）。下表"要求"列描述目标状态；"适用"列说明何时必须成立。

| # | 前提 | 适用范围 | 要求 | 核实方式（不得以配置声明替代） |
|---|---|---|---|---|
| D-1a | ES **显式 create 权限** | S-2 阶段 2/S-3（阶段 1 的 E-1 本身是核实它的手段） | 受控新建索引（§1/12 的 `vs.CreateIndex`）需要账号具备对该物理名的 create 权限；这是部署侧事实，当前**未核实**（V-4） | 仅由 §5.1 矩阵 E-1（成功路径）证据；若 E-1 被拒 → 受控新建不可用，S-3/阶段 2 降级为"仅对已预建索引验证"或停止，不推断为"自动创建已禁用" |
| D-1b | ES **自动创建行为** | S-2 阶段 2/S-3（同左：阶段 1 是核实手段） | 写入（index/bulk）不得隐式创建索引；实际状态由集群 `action.auto_create_index` 与账号权限共同决定（§1/13），当前**未核实**（V-4） | 仅由 §5.1 矩阵 E-2（行为证据，含 §5.1.1 拒绝分类）+ I-1（配置读取，若有权限）证明；**"一次显式 create 被拒绝"不构成充分证据** |
| D-2 | 禁止计划外创建（无 sidecar 副作用） | S-2/S-3（与 D-1a/D-1b 同：阶段 1 前后清单对比即其核实证据，阶段 2/S-3 执行前须已有该证据） | 探针期间不发生任何计划外索引/集合创建 | 探针前后 mapping/索引清单对比留证 |
| D-3 | 资源名称不复用 | S-2/S-3 | 一次性物理名（前缀拟 `rag-2d-probe-<sha8>-<日期>-`，E-1/E-2 子名独立，记录于证据目录），探针后保留取证、禁止复用、不自动删除 | 命名记录 + `ValidatePhysicalName`/`BindPhysicalTarget` 既有契约（§1/9）保持；受控目标不构成后端原子 CAS 的既有边界不变 |
| D-4 | Qdrant 禁用 | 全部范围 | 生产绑定继续禁用、不解除、不安排 Qdrant 在线探针（incarnation/namespace 未决） | 代码门保持 `ErrResourceBindingNotReady`；测试不注入 Qdrant 凭据 |
| D-5 | MySQL | 仅 S-3（若链路涉及） | S-1/S-2 不连接数据库；若 S-3 链路涉及则另行列出前提 | U-2 范围决定 |
| D-6 | 受控生命周期契约 | S-2/S-3（S-1 仅预算/取消契约相关） | `CheckManagedLifecycle` / `BindPhysicalTarget` / `VectorIOGuard` / 旧索引 fail-closed / 预算与取消契约全部保持 | 执行前确认目标固定提交未动这些契约 |
| D-7 | ES 客户端/服务端兼容 | S-2/S-3 | 客户端 8.16.0 vs 实际服务端版本（`design/stage2-plan.md` §8.9：Compose 8.11.3 / README 8.18.0 为历史记录值，以实际部署为准） | 单列验证项，归属部署验证，不与付费模型调用混合计账（§7.4） |

### 5.1 ES 创建权限与自动创建验证矩阵（待授权设计，本文不执行、不连接 ES）

两个控制面必须分别验证，任一单项结果不推出另一项结论：

| 编号 | 拟验证操作 | 预期结果 | 一次性测试名称（示例，实际按 D-3 生成） | 可能副作用 | 取证 | 清理责任 |
|---|---|---|---|---|---|---|
| E-1 | 受控路径**显式 create**：对不存在的一次性物理名走 `vs.CreateIndex(name, fp)` 完整路径（携带 fingerprint `_meta`） | 成功创建；mapping `_meta` 可读回 | `rag-2d-probe-<sha8>-<日期>-explicit` | 产生一个空的受控命名索引（无文档写入） | 创建请求/响应摘要、`_meta` 读回记录、索引清单前后差 | 默认**保留取证、禁止复用、不自动删除**（沿用 D-3）；如需删除，须**单独删除授权**后手工执行并留证，责任人与时点在授权文本中指定 |
| E-2 | 验证**自动创建被禁用**：用探针账号对**不存在的另一一次性名称**执行一次最小 index/bulk 写入（单条占位文档，不写业务语料） | 写入被拒绝（索引不存在且未隐式创建）；写入后 GET 该索引确认仍不存在 | `rag-2d-probe-<sha8>-<日期>-autoc` | 预期零副作用（拒绝不建资源）；若意外创建 = 该实例未禁用自动创建 → D-1b 核实失败并全停 | 拒绝响应摘要 + 写入后存在性检查结果 | 若意外创建：保留该意外索引作为证据，按 D-3 不复用，删除仍需单独授权 |
| I-1 | （可选，若账号有权限）读取集群设置 `action.auto_create_index` | 得到实际值并记录 | 无写入，不产生资源 | 无 | GET cluster settings 响应摘要 | 无需清理（只读） |
| I-2 | （可选）账号权限说明与上述行为证据的一致性核对 | 部署侧说明与 E-1/E-2/I-1 观测不矛盾 | 无 | 无 | 对照记录 | 无需清理 |

**与受控新建的相容性说明**：受控路径需要显式 create 权限，而目标是禁止*意外自动创建*，两者不冲突且可共存——典型部署形态是 `action.auto_create_index` 禁止通配隐式创建、同时为受控命名模式（如本探针一次性前缀）保留显式 create 能力；具体权限模型与集群设置组合由部署方确定并在授权文本中声明（本项目不推断其默认值，也不把任一变体写成已验证）。E-1 与 E-2 共同构成"显式可建、隐式不可建"的行为证据对；缺任一项，D-1a/D-1b 均不得标为已核实。

### 5.1.1 E-2 拒绝分类（v0.3）：被拒绝 ≠ 自动创建已禁用

E-2 写入被拒绝时，必须先对拒绝原因分类再下结论（依据响应体错误类型/状态码/错误 reason 原文脱敏记录；分类能力本身未验证，若响应不可区分则落入末行）：

| 拒绝类型 | 可得的结论 | 不可得的结论 |
|---|---|---|
| 自动创建策略拒绝（如 `action_auto_create_index_no_match_exception` 类错误，表明索引不存在且被 auto-create 策略拦截） | 支持"该名称模式下隐式创建被禁用"的行为证据，写入 D-1b 证据链 | 仍不能单独证明集群全局设置值（可能是索引模式级策略）；需与 I-1 合并判读 |
| 权限不足（authorization/forbidden 类，与索引存在性无关） | 仅说明该账号对该操作无权限 | **不得**作为 D-1b（自动创建禁用）任何一方的证据；D-1a/D-1b 结论均不受影响 |
| 其它错误（连接/超时/解析/未知状态码） | 无有效结论，记录后按 §4.2 停止 | 不得推断任何 ES 侧事实 |
| 无法区分以上任一情形（响应信息不足） | 结论只能收窄为：**"该账号、该名称、该次操作未产生索引"**（附写入后存在性检查证据） | 不得推断集群/账号级配置、策略或权限模型；D-1b 保持未核实 |

意外创建（E-2 预期外成功）的处理维持不变：记录为 D-1b 核实失败并全停，保留意外索引取证，删除仍需单独授权（§5.1 清理列）。

### 5.2 S-2 阶段拆分（v0.3 拆分，v0.4 补充 D-7 两分，消除循环前提）

v0.2 的写法把"D-1a/D-1b/D-2 核实留证"列为整个 S-2 的前置条件，而这三项只能由 S-2 内的 E-1/E-2 执行产生——构成循环。拆为两阶段：

| | **阶段 1：验证阶段**（需明确授权后才执行） | **阶段 2：受控端到端阶段**（仅阶段 1 完成并满足要求后可申请） |
|---|---|---|
| 内容 | §5.1 矩阵 E-1/E-2/I-1/I-2（逐项授权，含意外创建处置） | P-8（可选）、D-7 连接/创建/mapping/kNN 往返、（若进入 S-3）受控写入链 |
| 必需前提（先决，执行前即须成立） | ① 用户给出阶段 1 明确授权；② ES 地址与账号可用（用户提供）；③ 一次性命名规则与 D-3 不复用承诺经授权确认；④ 只读清单能力（前后对比取证用）；⑤ D-4 确认（不注入 Qdrant） | ① 阶段 1 全部已授权操作完成且证据归档；② **D-1b 结论为"隐式创建被禁用或可区分且可接受"**；③ D-1a 结论满足本阶段所需（预建索引则不要求显式 create）；④ D-2/D-6 按本阶段适用项核实；⑤ D-7 仅要求"执行前可核实"部分（见下方 D-7 拆分） |
| 产出的结论 | D-1a/D-1b/D-2 的首次核实证据（之前只能是"未核实"，不得预先假定满足） | 真实 ES 受控链行为记录；若使用真实模型输出写入 → 需另按 S-3 授权 |

必须等验证结果才能下的结论（阶段 1 前一律写"未核实"，不得预先标为已满足）：D-1a（显式 create 是否可用）、D-1b（隐式创建是否禁用及拒绝类型）、D-2（计划外创建是否为零）、按 E-2 结果的权限/策略判读。而阶段 1 自身的前提（上表①..⑤）不依赖这些结论，因此 S-2 可执行、不再循环。

**D-7 拆分（v0.4）**：D-7 的"兼容性"包含两类性质不同的信息，必须分栏记录，不得互相顶替：

| 类别 | 具体内容 | 何时可得 | 能否作为阶段 2 前提 |
|---|---|---|---|
| 执行前可核实的版本信息 | 客户端库版本（`server/go.mod` 声明值）、服务端版本与发布资料、仓库内既有兼容口径声明（`design/stage2-plan.md` §8.9 历史记录值需重新核实） | 阶段 2 执行前，只读核实 | 可以，作为阶段 2 前提 ⑤ |
| 只有阶段 2 才能验证的客户端行为 | 客户端与该服务端实际握手与产品校验（`X-Elastic-Product`）、mapping 读回、kNN/bulk 往返、错误类型映射 | 只能由阶段 2 的真实执行观测产生 | 不可以；列为阶段 2 的验证内容与产出，写成前提即构成新循环 |


---

## 6. 需要用户作出的最小决策

- **U-1 模型与端点**：S-1 三个角色的实际探针 model ID、endpoint 地域/域名；工作空间 ID **仅在所选服务形态要求时提供**（公共 compatible-mode endpoint 不需要，见 §3.1）。资料候选不等于能力已验证，取值由用户提供。
- **U-2 范围**：选择 S-1（仅模型直连探针，不要求真实 ES 验证先行）/ S-2（仅 ES 部署验证，不耗模型预算；内部再分阶段 1 验证/阶段 2 受控端到端，§5.2，分别授权）/ S-3（组合）或分别执行；各范围**分别授权、分别记录结果**（§2）。选 S-2/S-3 时需由用户提供 ES 地址与账号事实，并给出 §5.1 执行决定。
- **U-6 S-1 验证链方案**：按 §2.1 选择方案 A（本机 fake ES 真实绑定 + 真实模型）/ 方案 B（仅 adapter 直连）/ A+B 分别执行；选择决定验证结论的覆盖范围（B 不覆盖生产绑定/guard 链，A 不覆盖真实 ES）。v0.5：方案 A 已完成**离线**骨架与离线验证（§9，仍待独立复核），方案 B 仍未实现；两者的**在线执行**仍需单独授权。
- **U-3 预算数值**：§3.2 与 §3.2.1 表中全部由代码执行/runbook 保证的限制（请求数、token、并发、单步 deadline、总时长、总 wall-clock）。有限上限以正整数给出；`0`＝不限，不得以 0 冒充有限上限；不拟限制的维度须显式写明。金额上限与账号侧限额不在代码预算内，由用户另行设定（V-5）。本文不填数字。
- **U-4 `SupportsDimensionsRequest`**：区分三类事项——(a) **受控能力验证探针**：是否将 P-7 纳入 S-1 授权范围；(b) **生产配置决定**：验证后是否把 YAML `supports_dimensions_request` 置 `true`（独立配置变更，另批评审）；(c) **解除生产门禁**：生产部署采用该 profile 的真实端到端验证（属 S-3/部署验证，需再单独授权）。三者均不得预先写成已验证；验证通过只冻结"声明 vs 实际"事实，不自动授权 (b)(c)。在此之前保持 `false`（不下发参数）。
- **U-5 凭据注入方式**：环境变量最终命名（草案占位 `DASHSCOPE_API_KEY`；`design/stage2-plan.md` §8.2 曾用 `BAILIAN_API_KEY`，名称未定稿；`ProviderConfig.APIKey` 亦支持 YAML，但探针凭据拟不落盘不入 Git）。工作空间 ID 同按 U-1 仅条件性提供。

**拟交付的复核证据**：§4.4 全套 + 复核报告（含逐命令退出码与两栏对照表）。交付后状态为"待独立复核"，本草案不预先更新任何验收状态。

---

## 7. 离线可做 vs 需明确授权后才能执行

**可离线准备（不触碰外部设施、不改生产代码，各批仍需按批授权）**：v0.5 状态——第 1–4 项已离线实现并离线验证（待独立复核），第 5 项仍待准备：

1. 本方案与预算草案（本文，v0.5，仍待审阅）。
2. `//go:build online` 探针骨架（复用既有标签隔离先例，§1/10；默认构建不编译执行）——v0.5 已实现：默认构建（`!online`）不含任何远端开关，远端能力仅由 `endpoint_policy_online.go` 提供。
3. 预算接线离线验收 W-1..W-5（§3.2.2，fake/httptest 证明探针实际链，非组件单测）——v0.5 已实现并离线验证（含 UCRT64 race）。
4. §2.1 方案 A 所需的非 test 包本机假 ES 绑定入口——v0.5 已实现（`LocalFakeESTarget` 走生产 `BindPhysicalTarget`/`common.NewEmbedding` 链）；真实 ES 仍属 S-2。
5. 一次性物理命名、§5.1 矩阵（含 §5.1.1 拒绝分类记录格式）与 D-2/D-3 的证据模板与 runbook。

**必须用户明确授权后才执行**：

- 任何真实模型调用（含付费与免费额度，S-1/S-3）；任何真实 ES/Qdrant/MySQL 连接、写入与 §5.1 矩阵执行（S-2/S-3）；任何测试资源的删除清理（§5.1 清理列）。
- 解除任何门禁：含 `SupportsDimensionsRequest` 置 true（U-4(b) 生产配置决定）、Qdrant 绑定、`managedResourceLifecycle` 部署确认语义变更；P-7 验证通过不自动授权任一项。
- 2D 验收与 Stage 2 整体验收状态的更新、提交、推送、合并或 PR 操作。

**边界声明（原文保持）**：2C-3 离线通过 ≠ 2D 可执行 ≠ 真实设施可用 ≠ Stage 2 完成。带版本模型身份当前禁用；在真实协议映射被明确并验证前，不将其写成真实能力已验证。无可用凭据或未获授权时，2D 保持"未执行"，不编造探针结果，离线结论不受影响。

---

## 8. 执行约束（本草案批次）

不调用真实模型、ES、Qdrant 或数据库；不执行 2D 在线探针（含 §5.1 矩阵与 §5.2 阶段 1/2）；不解除门禁；不更新验收状态；不提交、推送、合并或修改 PR；不清理或覆盖既有工作区修改、审查 worktree 和 verification 证据。v0.5 批次（修复 `7a30899` 的五项独立复核问题）的改动范围：`server/core/probe/` 与本文；证据目录 `verification/stage2d-fix1-7a30899-20261028/`；不包含生产代码、门禁、`PROJECT_PROGRESS.md`/`CHANGES.md` 与既有 verification 证据。v0.6 批次（修复针对 `3aea024` 增量复核的五项问题：online 直接入口授权绕过、停止后仍发起重试、预检遗漏后续角色模型身份、Plan 可变引用数据竞争、空凭据误报就绪）的改动范围与之相同：仅 `server/core/probe/` 与本文；证据目录 `verification/stage2d-fix2-3aea024-20261028/`；不包含独立复核目录、既有 `.git-commit-msg.txt` 与既有 `CLAIMS.md` 改动。v0.7 批次（修复针对 `bdd3a6d` 复核的三项问题：停止标记后的发送窗口、nil Rerank 文档未在预检拒绝、非法目标/不匹配 similarity 被误报就绪）的改动范围为 `server/core/probe/`、`server/core/rerank/rerank.go`（提取共享纯输入校验）与本文；证据目录 `verification/stage2d-fix3-bdd3a6d-20261028/`；同样不包含独立复核目录、既有 `.git-commit-msg.txt` 与既有 `CLAIMS.md` 改动。v0.8 批次（修复针对 `1e81aa4` 复核的四项问题：停止标记与发送边界语义、发送证据计数、目标身份规范化一致性、证据表述更正）的改动范围为 `server/core/probe/` 与本文；证据目录 `verification/stage2d-fix4-1e81aa4-20261028/`；同样不包含独立复核目录、既有 `.git-commit-msg.txt` 与既有 `CLAIMS.md` 改动。v0.9 批次（处理针对 `91c872f` 复核的三项问题：百炼 embedding 身份接口阻塞、停止标记后仍可新增预算承诺、方案 B 离线设计）的改动范围：`server/core/providers/bailian/embedding.go`（新增 `Profile()` 与编译期断言）、`server/core/probe/`（新增 `bailian_identity_test.go`、`budget_stop_test.go`）、本文与新建的 `design/stage2d-stop-b-plan.md`；证据目录 `verification/stage2d-fix5-91c872-20261029/`；同样不包含独立复核目录、既有 `.git-commit-msg.txt` 与既有 `CLAIMS.md` 改动；不包含任何 `providers` 预算 API 变更（B+ 未实现）。

§2.1/§3.2.2/§5.1.1/§5.2 描述的探针链均为离线实现；除 §9 明确标注的“已离线验证”外，本文不声称任何在线结果。

---

## 9. 实现状态与未验证边界（v0.9）

**实现位置**：`server/core/probe`（非 test 包；默认构建编译，online 入口另有 `//go:build online`）。

| 能力 | 状态 | 证据/边界 |
|---|---|---|
| 方案 A 本机 fake ES 真实绑定 + 完整 fingerprint + guard 前后检查 | **已实现，离线验证通过** | `fake_es.go`/`chain.go`；构建期 2 次读取、每次 embedding 前后各 1 次 guard 读取；未涉及真实 ES |
| W-1..W-5（预算 owner/lease、预留失败零发送、deadline/并发、usage 可解析性、停止后不启动） | **已实现，离线验证通过** | `chain_test.go`；对端计数 = 真实发送次数 |
| 取消、清理、Qdrant 禁用、默认未授权边界 | **保持原状，未被本批修改** | 无新增映射表或 DDL |
| 默认构建的远端隔离（审查问题 1） | **已修复，离线验证通过** | 远端能力改为构建标签常量；`ChainOptions` 仅剩 `APIKey`（反射锁定）；`RunOnline` 不参与默认构建 |
| 在线远端必须 HTTPS、禁 userinfo、错误不回显 URL（问题 2） | **已实现，离线验证通过** | `CheckEndpoints`；本机 fake 代理计数为 0；占位凭据不回显 |
| 重定向/策略/HTTP 状态错误不重试（问题 3） | **已修复，离线验证通过** | `errcat.go` 的允许集合；307 只命中原端点 1 次、目标 0 次 |
| 入口第一步发送前完整预检，与 Readiness 同源（问题 4） | **已实现，离线验证通过** | `Preflight`/`ValidateSteps` 被 `Run`、`RunOnline`、`Readiness` 共用 |
| `Report.StopReason` 脱敏且保留 `errors.Is` 链（问题 5） | **已实现，离线验证通过** | 固定类别 + 可选状态码；原始错误逐层保留 |
| online 构建直接入口（`NewChain`→`Run`/`RunStep`）的 S-1 授权与凭据边界（增量问题 1） | **已修复，离线验证通过** | `resolveConstructionCredential`/`remoteSendAuthorizationError`；非回环未授权为 `ErrNotAuthorized`、字面量密钥为 `ErrNotConfigured`；本机 fake 代理与模型命中均为 0；未授权的回环 fake 不受影响 |
| 全局停止与新发送/重试的准入协调（增量问题 2） | **已修复，离线验证通过** | `beginAttempt`/`stop` 在同一锁内协调并取消在途尝试；停止后不再有任何重试发送（`ErrAlreadyStopped`），在途工作与预算 lease 收敛 |
| 预检复用生产 builder 的角色模型身份校验（增量问题 3） | **已修复，离线验证通过** | `ValidateRoles` 复用 `NewProviderProfile` + `RequestModel`；Readiness/Preflight/NewChain 同源拒绝 `m@v1`，所有模型端点零命中，保留 `ErrModelVersionNotExpressible` 链 |
| Plan/Step 防御性深拷贝（增量问题 4） | **已修复，离线验证通过** | `Plan.Clone`/`step.clone`；构造后修改 `Budget.Unlimited` 不再与运行期竞争，也不改变已固定的预算与授权 |
| 凭据边界单一实现（增量问题 5） | **已修复，离线验证通过** | `Credentials.Validate` 被 Readiness/RunOnline/NewChain 共用；未声明/空值/空白变量名/多声明一致拒绝，错误与证据只回显变量名 |
| 停止标记后的发送准入（v0.7 P1） | **已修复，离线验证通过** | `sendgate.go` 的 `sendGate`/`commitSend` 与 `chain.go` 的 `stop` 构成 RWMutex 全序；已准入未发送的尝试在停止后返回 `ErrAlreadyStopped` 且零模型命中，在途请求不被撤销也不重试；原复核 20k/100k 人工句柄形状 5/5 与 1/1 命中为 0（均 SKIP），剥离闸门的变异 3/3 复现 1 次命中 |
| 发送四状态的可见性与计数（v0.8 P1/P2） | **部分完成**：零新闸门准入/零新重试可保证；字面“零新发送”**未闭合**（§9.1） | 四状态：入口已准入 / 已通过发送闸门（`commitSend`，线性化点）/ 已交给 transport（`g.inner.RoundTrip`）/ 对端已收到（仅 fake/代理可观测）。新增 `StepReport.GateAdmissions`、`TransportCalls`；`Attempts` 改为明确的“预算已承诺次数”。`TestStepReport_CountsSeparateCommitFromSend`：闸门拒绝时 attempts/committed=1/1 而 gate/transport=0/0、对端 0；发送 1/1/1；重试 2/2/2。`TestSendGate_PostGatePreTransportWindowIsNotClosed`：取消先到时 0 命中，用 `beforeCancel` 钩子把“标记已发布、取消未施加”分开后 handoff 命中 1（记录事实，不声称闭合） |
| 目标身份规范化与就绪一致（v0.8 P2） | **已修复，离线验证通过** | `Plan.Validate`/`NewLocalFakeESTarget` 一致拒绝非规范输入：模型名首尾空白（`chat/embed/rerank.model`）、similarity `"  COSINE  "`/`COSINE`、provider `OpenAI` 均返回 `ErrNotConfigured` 与具体原因；`Ready()=false`、执行入口零发送；标准 cosine 仍就绪可执行，且 `NewExpectedEmbeddingFingerprint`、实际绑定与 Marshal→Parse 的 digest 一致（不改已验收的 fingerprint 格式） |
| fake ES 证据表述（v0.8 P4） | **已更正** | 本机 fake ES 是 `httptest.NewServer` 的本机 HTTP 服务（ES 客户端经本机 HTTP 读取索引元数据），不是“纯本地对象调用”；它不经模型 `sendGate`，不证明真实 ES 行为；旧复现的 SKIP、变异测试与实现者自测均不写作独立通过 |
| 百炼 embedding 身份接口（v0.9 P1，**前置遗留**） | **已修复，离线验证通过** | 真实注册后 Registry 返回 `*bailian.validatedEmbedRole`，它此前未实现 `providers.ProfiledEmbedRole`，导致 `NewChain` 恒失败（接口强制来自更早的 `2c952ce`，非本批固定起点新回归）。新增防御性 `Profile()`（`v.profile.Clone()` 值拷贝）+ 编译期断言；身份门禁未放宽：provider/模型/维度三项不一致仍在构造期 `ErrEmbeddingIdentityMismatch` 且零发送；百炼与 OpenAI 对照均为 1/1/1、对端 1 命中 |
| 发送证据计数与预算承诺区分（v0.9 P2） | **部分处理，边界待决**（§9.2） | `Attempts`=预算已承诺次数；`GateAdmissions`/`TransportCalls` 为不同观测点（后者不证明网络字节或对端收到）。新增确定性特征化 `TestStopMarkerDoesNotRetractBudgetCommit`：marker 后 committed 0→1、gate/transport 0/0、对端 0、inputUsed>0、inFlight=0。**“停止标记后零新预算提交”未满足**，已更正旧表述并保留为待决边界 |
| 停止语义方案 B 离线设计（v0.9 P3） | **设计已交付，未实现、未批准** | `design/stage2d-stop-b-plan.md`：E1–E4 与 closing 标记/停止成功返回的线性化点、有界等待与 `StopResult{Pending, Confirmed}`、超时“未确认”与调用方责任、`RoundTrip` 可能阻塞在响应头、A/B/C 对比、B 不闭合预算承诺（B+ 需批准） |
| 预检拒绝 nil Rerank 文档（v0.7 P2） | **已修复，离线验证通过** | `rerank.ValidateInput` 由生产 `Rerank` 与 `Step.validateAgainst` 共用；合法 Chat 在前、Rerank 含 nil 时 Preflight/Readiness/Run/RunStep 一致拒绝，全部模型端点零命中，错误链保留 `ErrEmptyStep` 与 `rerank.ErrInvalidInput` |
| 目标规则与生产期望一致（v0.7 P2） | **已修复，离线验证通过** | `Plan.Validate` 复用 `vector.ValidatePhysicalName` 与 `vector.NewExpectedEmbeddingFingerprint` + `NewFingerprint` + `Equal`；`bad,*`/多目标/`dot` 在 Readiness 与执行入口一致拒绝，cosine 仍就绪可执行；静态预检只判定配置自洽性，不保证真实资源在预检后不变（运行期由绑定/guard 重新读取） |
| **真实模型端点的在线探针** | **未执行** | 需 U-1/U-3/U-5 取值与在线授权；本轮零真实调用 |
| **真实 ES / Qdrant / MySQL** | **未验证** | 属 S-2/S-3；本批未连接任何真实设施 |
| **独立复核（v0.5 五项修复）** | **已完成，发现五项增量问题** | 报告 `verification/independent-2d-3aea024-20261028/REPORT.md`；本批逐项修复 |
| **独立复核（v0.6 五项增量修复）** | **已完成，发现三项问题** | 报告 `verification/independent-2d-bdd3a6d-20261028/REPORT.md`；本批逐项修复（v0.7） |
| **独立复核（v0.7 三项修复）** | **已完成，发现四项问题** | 报告 `verification/independent-2d-1e81aa4-20261028/REPORT.md`；本批逐项修复（v0.8），其中停止语义保留为待批准选项（§9.1） |
| **独立复核（v0.8 四项修复）** | **已完成，发现三项问题** | 报告 `verification/independent-2d-91c872-20261029/REPORT.md`；本批逐项处理（v0.9），停止语义与预算承诺仍保留为待决边界（§9.1、§9.2） |
| **独立复核（v0.9 处理）** | **待复核** | 本批完成后停止并等待复核，不更新任何验收状态 |

### 9.1 停止语义：可保证项、残余窗口与待批准选项（v0.8）

**四状态（实现见 `sendgate.go` 文件头）**：

| 状态 | 可观测点 | 停止标记之后 |
|---|---|---|
| 1. 入口已准入 | `beginAttempt` 在 `c.mu` 写侧登记在途句柄 | 不会发送：闸门拒绝 `ErrAlreadyStopped`，零字节发出 |
| 2. 已通过发送闸门 | `commitSend` 在 `c.mu` 读侧返回（与 `stop` 写侧同属一把 RWMutex，**线性化点**） | 不再受影响：请求继续流向 handoff |
| 3. 已交给底层 transport | 调用 `g.inner.RoundTrip` 开始 | **不可撤销**，只能取消 ctx 让传输层尽快放弃 |
| 4. 对端已收到 | 仅测试 fake/代理自身的计数 | 不属于本包可观测事实，也不作承诺 |

**已能保证**：停止标记之后零新闸门准入、零新重试；第 1 类尝试返回 `ErrAlreadyStopped` 且零字节发出。
（注意：本项**不**包含“零新预算提交”，见 §9.2。）

**未闭合**：第 2 与第 3 步之间没有任何锁（持锁跨越整个 handoff 会让 `stop` 无界等待，被禁止），
因此“已过闸门、尚未交给 transport”的请求仍可能在标记之后开始传输。独立复核用人工登记
100,000 个句柄放大“标记发布 → 逐个取消”的滞后，5/5 观测到 fake 模型命中 1；本批用
`Chain.beforeCancel`（仅测试钩子）把“标记已发布”与“取消已施加”确定性地分开，得到同样的
hits=1（`TestSendGate_PostGatePreTransportWindowIsNotClosed/stop_marker_does_not_block_handoff`）。
人工句柄与钩子只用于构造时序，不代表常规触发概率；本批不以这些证据声称原契约通过。

**待批准选项**（在用户决定前，本项保持“未闭合”，不得写入验收通过）：

- **A. 接受较弱契约**：把契约明确改为“停止后零新闸门准入、零新重试；已通过闸门的请求可能稍后发送且不可撤销”。代价：需同步修订 §4.2 停止条件措辞与 §9 结论；不声称零新发送。
- **B. 两阶段停止 + 有界等待**：`stop` 先发布 closing 标记拒绝新准入，再在锁外**有界**等待“已过闸门、未 handoff”的计数归零（闸门在 `commitSend` 后登记、handoff 返回后注销）；超时则以明确错误报告“无法确认零新发送”。代价：`stop` 最多等待一个可配置上限；传输层阻塞时仍只能返回未确认，不制造无界等待或死锁。
- **C. 串行化登记-发送**：把“过闸门 + 开始 handoff”做成不可分割的登记段，`stop` 等待登记段为空后再返回。代价：模型请求启动与 `stop` 共享一把锁（降低吞吐），且仍不能撤销已 handoff 的请求，语义同 B（“停止返回后不再开始新 handoff”），实现更简单。

任何选项都不承诺撤销已发出的请求；不得用无限持锁、死锁或无界等待制造保证。

**v0.9 补充**：本项只处理“发送准入”；预算承诺是另一条独立边界（见 §9.2）。
方案 B 的完整离线设计（线性化点、有界等待、超时未确认、A/B/C 对比、以及 B 不闭合预算承诺的说明）
见 `design/stage2d-stop-b-plan.md`；该设计**未实现、未批准**，不表示本项已闭合。

### 9.2 预算承诺与停止准入（v0.9，待决）

- **事实**：预算 `Reserve → Commit` 发生在角色适配器内部（`budgetEmbedRole` / `bailian.validatedEmbedRole`），
  位置在发送闸门 `commitSend` **之前**；`stop` 只发布停止标记并取消在途 ctx，不参与预算账本。
  因此已准入的尝试可能在标记之后完成一次 Commit
  （`TestStopMarkerDoesNotRetractBudgetCommit`：marker 后 `committed` 0→1，`gate/transport` 0/0，对端 0）。
  该用例用仅测试钩子 `beforeCancel` 固定时序，**不代表常规负载触发概率**；模型零命中也不等于预算零消耗。
- **能保证**：停止后零新**入口准入**，所以这类承诺只可能属于标记前已准入的尝试；
  已承诺账本保守结算（`Abort` 不退款）、lease 恰好终结一次、slot 归零、无残留预留、
  `errors.Is` 链保留 `ErrAlreadyStopped` 与原始原因。
- **不能保证**：停止标记后零新预算提交。旧表述已在本版更正，**不得**再把它写成契约通过。
- **闭合路径（B+，未实现、需批准）**：让 `Commit` 在 `pb.mu` 内咨询与 `stop` 同全序的准入判据；
  代价是修改 `providers` 预算 API 与锁序（影响全部消费者），或把预算提交移入 probe 自有包装层
  （牺牲“生产角色内部记账”的路径真实性）。取舍分析见 `design/stage2d-stop-b-plan.md` §3.4。
  三种路径均不得静默退款、吞错误链或泄漏 lease。
