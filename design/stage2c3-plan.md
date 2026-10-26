# Stage 2C-3 实施范围（已整理，详细实施方案待确认，未实施）

**状态**：2C-3 范围已整理，详细实施方案待确认，**未实施、未验收**。本文不含代码改动，也不代表任何门禁已解除或 Stage 2 完成。
**本批授权边界**：仅修正文档。**未授权**解除门禁、**未授权** 2C-3 实施、**未授权** 2D 在线调用。
**依据**：`design/stage2-plan.md` §5.8 第 6 条（guard 入口与解除）、§7.3（依赖顺序与解除条件）、§7.2 首批范围。
**已验收前置**：**Stage 2C-2（含 P2）已在批准的受控 ES 生命周期及既定离线范围内通过独立复核**，目标 `a60bcee7e2de9fc6b1ff7e5158ccee22d7636bae`（复核来源为云端独立复核，非本地 `verification/` 目录）。2C-1 基线 `2dc0b825` 已在既定离线范围通过独立复核；本文不扩大该结论。

---

## 1. 本批目标与边界

**唯一目标**：**受控 ES 路径的新 Embedding profile 接线与条件启用**。

1. 让显式声明 `Provider.EmbeddingModel` 的 profile，在**身份、维度、目标绑定**均已校验通过后，经 `Registry.GetEmbedder` 进入受控 ES 写入/检索调用链，而不是在入口处一律 `ErrEmbeddingGuardNotReady`。
2. 新 profile 的身份与维度全部取自配置元数据（`Provider.EmbeddingModel` / `Provider.EmbeddingDimensions`，经 `config.ResolveEmbeddingDimensions` 单一边界），与索引 fingerprint **同源**。

**明确非目标（不在本批）**

- 不解除 Qdrant 生产绑定：incarnation/namespace 未决，Qdrant 继续生产禁用。
- 不解除 `vector.managedResourceLifecycle` 部署确认；不把部署确认当作程序已验证的事实。
- 不执行 2D 在线探针，不调用任何真实/付费模型，不做真实 ES/Qdrant/MySQL 验证。
- 不新增 manifest 表/DDL、不做 alias 自动切换、不做旧索引迁移或重建。
- 不改动 2A 预算 owner/lease、2B StreamSession 取消契约与 Registry 发布契约。
- 不改写既有 fingerprint 版本/字段/digest，不自动认领或覆盖历史记录。

**Qdrant 边界（必须分开表述）**

- Qdrant incarnation/namespace 未决 → **Qdrant 继续生产禁用**。
- **不得把“Qdrant 生产绑定通过”列为 ES 2C-3 的硬前置。**
- “两后端共享 fingerprint 比较逻辑的离线测试覆盖”与“任一后端的生产可用性”是两件事：前者是共享代码路径的离线证据，后者归 2D 与部署验证；本文只以 ES 侧的绑定/guard 证据作为 2C-3 门槛。

## 2. 现状核对（只读，2026-10-26，HEAD `a60bcee`）

### 2.1 fail-closed 门禁（四处，缺一不可）

| 文件:行 | 符号 | 现状 |
|---|---|---|
| `server/core/common/embedding.go:25-29` | `common.NewEmbedding` | 显式 `Provider.EmbeddingModel` → 返回 `ErrEmbeddingGuardNotReady` |
| `server/core/rag.go:98-100` | `core.New` | 同上 |
| `server/internal/logic/rag/retriever.go:76-78` | `defaultRagFactory.Create` | 同上 |
| `server/internal/logic/rag/retriever.go:583-585` | `rag.InitWith` | 同上 |

**结论**：2C-3 **不能只删除一个分支**。四处判定必须收敛为同一个“profile 已就绪”判定；否则残留任一入口仍会拒绝，或出现“某入口放行但未绑定/未校验”的不一致状态。

### 2.2 构造入口与现有顺序

- `defaultRagFactory.Create`（`server/internal/logic/rag/retriever.go:69`）：`normalizeDimensions` → 门禁 → `vector.CheckManagedLifecycle` → `buildRegistry` → `core.ComputeFingerprintMeta` → `IndexExists` → 受控 `CreateIndex(fp)` → `vector.BindPhysicalTarget` → `core.New` → `VerifyFingerprint` → `SetRegistry`。
- `buildRegistry`（同文件 `:180`）：只做 `config.ResolveEmbeddingDimensions` 校验与 API Key 解析，**构造期不发起网络请求**；`NewProviderProfile`（`core/providers/profile.go:65`）只做字段校验与归一化。
- `core.New`（`server/core/rag.go:91`）：`ResolveDimensions` → 门禁 → `ValidatePhysicalName` → Qdrant 拒绝（`:107-109`）→ `ManagedResourceLifecycle`（`:110-112`）→ `VectorIOGuard` 非空（`:113-115`）→ `BuildIndexer` → 条件预热 QA 模型。
- Embedder 的生产构造入口当前只有 `common.NewEmbedding`，调用点三处：`core/indexer/indexer.go:23`、`core/indexer/indexer_async.go:22`、`core/retriever/retriever.go:25`。
- `Registry.GetEmbedder`（`core/providers/registry.go:246`；接口声明 `:48`）目前**无生产调用点**（仅 `providers.FakeRegistry` 测试实现）。其缓存键来自 `ProviderProfile.CacheKey()`，失败不发布缓存。
- 适配器：`providers.BuildEmbedRole`（`core/providers/adapter.go:142`）→ `embopenai.NewEmbedder` + `embedAdapter`（`Dimensions()`/`ModelID()` 取 Profile 声明）；bailian `BuildEmbed`（`core/providers/bailian/embedding.go`）→ `validatedEmbedRole`（响应维度校验 + 预算结算 + usage collector）；openai `BuildEmbed`（`core/providers/openai/register.go`）→ 直接 `providers.BuildEmbedRole`。
- 现有 guard/维度包装只在 legacy 路径：`common.NewEmbedding` 返回 `dimensionEmbedder`（embedding 前后各一次 `conf.VectorIOGuard` + 响应维度与配置维度比对）。
- 维度/身份来源：`config.ResolveEmbeddingDimensions`（`core/config/dimensions.go:63`，新模型**必须**声明维度，冲突拒绝）、`Config.ResolveDimensions`（`:96`）、`ComputeFingerprintMeta`（`core/rag.go:285`，P2 后与 `NewEmbedding` 同分支条件）、ES `CreateIndex` 使用 `fp.Dimensions`（`core/vector/es.go:73-78`）。

### 2.3 关于“1024 硬编码”的核实结论

- `common.NewEmbedding` **不再有** 1024 字面量：维度取自 `conf.ResolveDimensions()`；`dimensions` 只在 `text-embedding-3-` / `Qwen/Qwen3-Embedding-` 前缀时才下发 `Dimensions` 参数（`core/common/embedding.go:54-58`）。属**已贯通维度**，无需重复修改。
- `1024` 的其余出现，按性质区分：
  - **集中 legacy 默认值**：`core/config/dimensions.go:12` `DefaultEmbeddingDimensions = 1024`（注释明确“legacy configuration default, not a schema default”）。
  - **测试值**：`core/providers/fake.go:269`（`NewFakeEmbed(1024, ...)`）。
  - **注释**：`core/common/embedding.go:55`、`core/providers/bailian/embedding.go:95`。
  - **与向量维度无关**：`core/rerank/rerank.go:113`（`MaxChunksPerDoc`）、`internal/model/gorm/knowledge_chunks.go:13`（`varchar(1024)`）。
- **真实残留（待设计，不是“重复修改旧硬编码”）**：新 profile 分支下“是否下发 `Dimensions` 请求参数”的模型能力判定。`BuildEmbedRole` 在 `p.Dimensions > 0` 时**一律下发**；legacy 路径只对特定前缀下发。两者行为不一致，需在实施设计中给出模型能力声明方案，**不得用新的前缀硬编码补丁掩盖**。

## 3. 装配顺序（约束，不是实现方案）

1. **身份先于适配器，不调用模型取身份**：fingerprint 的 `provider_name / embedding_model_id / dimensions` 全部由配置元数据计算；不得为了获得身份而构造或试调用模型（无在线探测、无“先发一次请求看维度”）。
2. **校验先于构造**：配置校验（`ResolveEmbeddingDimensions`，冲突拒绝）→ 读取既有索引身份/schema 并与期望比对（`ReadFingerprint` / `VerifyFingerprint`）→ 绑定具体物理目标（`BindPhysicalTarget`）→ 之后才构造并调用对应适配器。
3. **禁止用构造顺序制造循环依赖**：Registry 在 factory 内构建，`core.New` 成功后才 `SetRegistry` 发布；embedder 的取得必须走显式注入（如 `conf.Registry`）或等价的显式路径，不得依赖“全局 Registry 已发布”的时序巧合，也不得为此改动 Registry 发布契约。
4. **失败不发布、不清理他人资源**：任一校验失败不得发布全局状态；不得因后续失败删除已存在资源（无所有权证明）。
5. 具体的注入点、包装层归属、错误语义映射属于**实施设计项**（见 §5），由实施者先提交方案再实施；实施者不得自行选定。

## 4. 解除后仍然保持的门禁与边界

- `ErrResourceBindingNotReady`：Qdrant 生产绑定继续禁用（incarnation/namespace 未决）。
- `vector.managedResourceLifecycle`：`true` 仍仅为运维部署确认，程序不校验部署事实；默认 `false`。
- 具体物理名（`ValidatePhysicalName`）与“单一具体目标”要求不变。
- `BindPhysicalTarget` 的 guard，以及 embedding 前后、BM25、后台读取、列表、删除前 guard 不变。
- 受控同步 bulk、逐项回执校验、无遗留后台 worker 的行为不变。
- 旧索引身份未知/被误标时仍失败关闭：不自动认领、不迁移、不重建。
- 2A 预算 owner/lease 与 2B 取消传播契约不变（不刷新预算、不吞取消）。

## 5. 实施设计项（未定，需先确认方案）

| # | 设计项 | 为什么要先定 | 需要的结论 |
|---|---|---|---|
| D1 | 四处门禁统一判定 | 只删一处会留下不一致放行/拒绝 | 判定函数位置、四处调用的原子一致性 |
| D2 | Embedder 取得路径与 guard 包装归属 | legacy 有 `dimensionEmbedder`（guard+响应维度）；`embedAdapter` 无 guard，bailian `validatedEmbedRole` 只校验维度与预算 | 新 profile 下 guard 与响应维度校验由谁负责、放在哪一层 |
| D3 | `Dimensions` 请求参数下发能力 | 见 §2.3，直接下发与 legacy 行为不同 | 能力来源（Profile 字段/Provider 声明），不得前缀硬编码 |
| D4 | Registry 注入路径 | 避免全局发布时序依赖与循环依赖 | 显式注入点（`conf.Registry` 或等价），以及 `core.New` 前的可用性 |
| D5 | 身份/维度单源收敛 | 防止“取指纹用 A、实际调用用 B” | `ComputeFingerprintMeta` 与 embedder 取得共用同一判定/同一维度值的位置 |
| D6 | 错误语义 | 解除后不得留下“已定义但不触发”的误导分支 | `ErrEmbeddingGuardNotReady` 保留/替换范围，失败原因是否可区分 |
| D7 | 配置校验规则调整 | `InitWith`/factory 现要求 `embedding.model` 非空等前提可能需随新 profile 变化 | 新 profile 下的必填项与校验顺序（不放松已有校验） |

## 6. 离线验收矩阵（2C-3 实施后适用）

| 用例 | 断言 | 边界 |
|---|---|---|
| 显式 profile 启用成功路径 | 经 `Registry.GetEmbedder` 取得的适配器身份/维度与 fingerprint 一致；不再返回门禁错误 | 离线 fake/httptest，无真实后端 |
| 身份不一致仍阻断 | profile 声明的 model 与既有 fingerprint 不一致 → 写入/检索前拒绝（`ErrFingerprintMismatch` 语义不变） | 复用 2C-2 门控路径 |
| 维度不一致仍阻断 | 声明维度与既有索引/fingerprint 不一致 → 调用适配器前即失败 | 同上 |
| fingerprint 缺失/损坏 | 仍 fail-closed，不因解除门禁而放行或回填 | 同上 |
| legacy 分支不受影响 | `Provider.EmbeddingModel` 为空时身份仍为 `legacy`，P2 回归用例继续通过 | P2 用例保持 |
| 维度单源 | 同一 profile 下 fingerprint、ES mapping 维度、请求参数完全一致 | 不含真实后端 |
| 失败前置与取消 | guard 失败时不发起模型调用；预算 owner/lease 与取消传播不变 | 复用既有预算与取消用例 |

## 7. 与 2D 的边界

2C-3 只解除“离线可判定的受控 ES 路径门禁”。真实模型可用性、真实返回维度与 usage、真实 ES 权限与“禁止自动创建索引”设置、部署确认事实，均由 2D 或部署验证在**单独授权**后进行。

**2C-3 的离线验收不授权任何 2D 真实模型调用，也不证明真实部署权限已核实。** 2C-3 通过 ≠ 2D 可执行 ≠ 真实设施可用 ≠ Stage 2 完成。

## 8. 交付物与退出条件

- 交付物：代码改动、离线测试与日志、`CHANGES.md`/`PROJECT_PROGRESS.md` 状态更新、§5 设计项结论记录、独立复核报告（由复核者出具）。
- 退出条件：§6 全部通过、§5 各项结论已确认，且独立复核明确“仅 ES 路径门禁解除、其余门禁与边界未被动”。
- 回滚：回滚 = `git revert` 2C-3 提交；不删除、不回填既有 fingerprint 数据，不改写历史验收记录。
