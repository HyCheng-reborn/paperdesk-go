# CLAIMS —— Stage 2D S-1 方案 A 离线准备（本批证据口径）

固定基线：`a12af924f0441363b46e0fec41fca29bdc30993a`（分支 `codex/stage2c2-fingerprint`）
本批性质：**离线**探针骨架与回归测试。未执行任何在线探针，未连接真实模型 / 真实 ES / Qdrant / MySQL。

> 重要边界：本文件**不是**"在线实测"栏。§4.4 要求的"官方文档口径 vs 在线实测"两栏中，
> 本批只可能填充**离线可核实项**；"在线实测"一栏除已经由代码事实确定的语义外，一律保持
> **未执行**，不得以本批离线结果替代。

---

## 1. 三栏对照（资料口径 / 本批离线证据 / 在线实测）

| # | 事项 | 资料口径（2026-10-19 旧日期，需重新核实） | 本批离线证据（可复核） | 在线实测状态 |
|---|---|---|---|---|
| C-1 | `text-embedding-v4` 维度与单次条数上限 | 默认 1024 维、单次 ≤10 条、单条 ≤8192 token | **未验证**。`TestProbeChain_ProductionBindingAndScope` 只用计划声明的 8 维与 2 条占位文本走通链路；这是**本机对端契约**，与真实模型无关 | **未执行** |
| C-2 | Embedding 响应形状（条数/顺序/维度一致） | 未在资料中作为保证 | 已验证**本项目校验逻辑**：`guardedEmbedder` 在响应条数不符、维度不符、含非有限值时 fail closed（`TestProbeChain_ResponseDimensionMismatchRejected` 用 dims/2 触发 `config.ErrEmbeddingDimensions`，且该次尝试仍被预算结算为 committed=1、in-flight=0、未产生向量） | **未执行**（真实服务是否遵守该形状未验证） |
| C-3 | Chat usage 是否返回、字段含义 | 资料口径提到 `stream_options.include_usage`，未作为本仓保证 | 已验证**本项目对 usage 缺失的处置**：对端不返回 usage 时 eino 给出全零非 nil 结构，探针按 `unknown` 记录并按预留保守结算（`TestW4_UsageSettlement/usage_missing_is_unknown`：Usage="unknown"、InputUsed=3、OutputUsed=16、预留归零） | **未执行**（真实服务 usage 数值未验证） |
| C-4 | Rerank 结果映射与分值域 | `gte-rerank-v2` / `qwen3-rerank` 两条路径，分值域未在本仓冻结 | 仅验证**链路与计数**：`role.Rerank` 返回条数被记录（`RerankResults=1`），top_k 越界在发送前拒绝 | **未执行** |
| C-5 | 模型带版本身份（`m@v1`）可用性 | — | 未在本批新增断言；既有代码事实：`providers.RequestModelID` 对合法带版本身份仍返回 `ErrModelVersionNotExpressible`，**在设计上提前拒绝、不产生请求**。该设计不证明真实服务是否支持版本参数 | **未执行** |
| C-6 | `SupportsDimensionsRequest` 真实能力 | 资料称部分模型支持 dimensions 参数 | 未在本批断言；YAML 声明仍默认 `false`（不下发参数），**未解除门禁** | **未执行**（U-4(a) 探针未授权） |
| C-7 | 生产 embedding 入口是否要求可信物理绑定 | — | **已由代码与测试同时确认**：`common.NewEmbedding → newProviderEmbedding` 在 `Registry`/`ManagedResourceLifecycle`/`IndexName`/`verifiedTargetBinding`/身份比较任一不满足时 fail closed。测试以相似度 `dot` 触发 `common.ErrEmbeddingIdentityMismatch`，**模型对端命中 0 次** | 不适用于在线（代码契约） |
| C-8 | guard 是否在发送前生效 | — | **已验证**：本机 fake ES 的资源 uuid 被改写后，embedding 调用返回 `vector.ErrResourceReplaced`，**模型对端命中 0 次**、预算 committed=0、in-flight=0，fake ES 读取计数 3。即 guard 在真实发送之前拦截 | 不适用于在线（代码契约） |
| C-9 | ES 显式 create 权限（D-1a） | — | **未核实**（本批不连接任何真实 ES） | **未执行**（S-2 阶段 1 / E-1） |
| C-10 | ES 自动创建行为（D-1b） | 由集群 `action.auto_create_index` 决定；本仓客户端不读该设置 | **未核实**。本批 fake ES 的 mapping/索引元数据全部由本进程自造，**不构成任何 ES 侧行为证据** | **未执行**（S-2 阶段 1 / E-2、I-1） |
| C-11 | 计划外资源创建（D-2） | — | **未核实**。本机 fake ES 只在进程内存在，`OtherCalls=0` 只说明探针未向假目标发额外请求，**不能推出真实 ES 无副作用** | **未执行** |
| C-12 | ES 客户端/服务端兼容（D-7） | 仓库历史记录：Compose 8.11.3 / README 8.18.0（历史值，需重新核实） | **仅执行前可核实部分可用**：客户端版本以 `server/go.mod` 声明值为准；握手/产品校验/kNN/bulk 往返/错误类型映射**均未验证** | **未执行**（S-2 阶段 2） |

---

## 2. 预算语义（离线可判定，非资料口径）

| # | 断言 | 证据 |
|---|---|---|
| B-1 | 每次模型 HTTP 尝试（含受控重试的每一次）都经同一 `RequestBudget` 的预留→承诺，服务端命中数 = committed 数 | `TestW1_RetryReservesAndCommitsEveryAttempt`：embed 命中 2 = Attempts 2 = committed 2；in-flight 终值 0；input used 8（两次 unknown 各按预留 4 结算） |
| B-2 | 预算耗尽后零新增发送 | `TestW2_ExhaustedBudgetSendsNothingFurther`：进程上限 1 时 chat 命中 1、embed/rerank 命中 0，返回 `providers.ErrBudgetExhausted` |
| B-3 | deadline 与并发上限在探针入口生效 | `TestW3_ExpiredCallerContextStopsBeforeSend`（过期 ctx：三端点命中 0、AttemptsUsed=0）、`TestW3_DeadlineDuringSendIsNotRetried`（单步 150ms：chat 命中 1 且不重试、in-flight 归 0）、`TestW3_ConcurrencyLimitAppliesAtProbeEntry`（并发 1：第二步被拒、embed 命中 0） |
| B-4 | usage 缺失按 unknown，不记 0；结算可观测；释放 in-flight | `TestW4_UsageSettlement`（两子用例） |
| B-5 | 预留失败或非预期错误后不启动后续探针 | `TestW5_UnexpectedErrorStopsSubsequentSteps`（HTTP 500：chat 命中 1、embed/rerank 命中 0、report.Stopped=true，再次 RunStep 返回 `probe.ErrAlreadyStopped`） |
| B-6 | `0` 不作为有限预算；待定参数不静默变成不限 | `TestBudgetPlan_ZeroIsNotFinite`（8 子用例：0 请求 / 0 input token / 0 并发均被拒；只有显式点名 `Unlimited` 才接受；未知维度名、负值、缺 deadline 均拒绝） |
| B-7 | 默认构建与默认测试不会发出真实在线请求 | 三点共同成立：(a) `online_run.go` 带 `//go:build online`，默认构建不编译；(b) `checkEndpoints` 默认仅接受回环端点，非回环返回 `probe.ErrRemoteNotAllowed`（`TestProbeChain_RemoteEndpointRejectedByDefault`）；(c) 本批全部测试的 HTTP 对端都来自 `httptest`（fake ES 与模型对端） |
| B-8 | 探针不持有真实凭据 | `ChainOptions.APIKey` 在测试与 online 骨架中均为占位串或从环境变量临时读取；`environment.log` 记录七个相关环境变量**全部 absent**，且只记录存在性、不记录值 |

---

## 3. 明确不计入"已验证"的事项

- 本批**没有**任何真实模型 / 真实 ES / Qdrant / 数据库调用；`environment.log` 中相关凭据全部 absent。
- fake ES 只证明"生产绑定 / guard / 身份检查链被真实代码执行"，**不产生任何真实 ES 事实**；D-1a / D-1b / D-2 / D-7 保持"未核实"。
- `-race` 未完成（本机 gcc 8.1.0，运行期 `0xC0000139`；同工具链下本批未改动的 `core/providers`、`core/common` 同样无法启动）。probe 包数据竞争**未被检测器验证**。
- 本文件不构成 2D 验收、不改变任何门禁、不授权任何在线执行；S-1 在线执行仍需用户对 `design/stage2d-plan.md` 给出范围与预算数值的明确授权。

## 4. 草案修订（本批仅两处）

- §4.1：明确 S-1 的 ES 侧**仅允许**进程内本机 fake ES（方案 A），真实 ES/Qdrant/MySQL 仍一律禁止；fake ES 不得用于填写 D-1a/D-1b/D-2/D-7 或表述为"ES 已验证"。
- §5.2：D-7 拆为"执行前可核实的版本信息"（可作为阶段 2 前提）与"只有阶段 2 才能验证的客户端行为"（列为阶段 2 的验证内容与产出，写成前提即构成新循环）。

未改动：授权、预算数值、门禁状态、验收状态；§2.1/§7 的"未实现/待准备"表述按最小修订要求保留（与本批已准备离线骨架的差异已记入 `results.txt` U-7）。
