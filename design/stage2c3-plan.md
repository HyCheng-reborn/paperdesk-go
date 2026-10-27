# Stage 2C-3 实施设计（详细实施方案，已实施，待独立复核）

**状态**：2C-3 详细实施设计已确认并实施，**待独立复核**（2026-10-27）。
**实施与设计差异**：实现基本遵循设计文档 D1-D7 决策；`ErrEmbeddingGuardNotReady` 保留为 `ErrEmbeddingProfileNotReady` 的别名以兼容测试引用；`guardedEmbedder` 统一 legacy 与新 profile 的 guard 与响应校验；`SupportsDimensionsRequest` 通过 YAML 配置声明而非真实能力证明。
**本批授权边界**：仅制定实施方案。**未授权**解除门禁、**未授权** 2C-3 实施、**未授权** 2D 在线调用。
**依据**：`design/stage2-plan.md` §5.8 第 6 条（guard 入口与解除）、§7.3（依赖顺序与解除条件）、§7.2 首批范围。
**已验收前置**：**Stage 2C-2（含 P2）已在批准的受控 ES 生命周期及既定离线范围内通过独立复核**，目标 `a60bcee7e2de9fc6b1ff7e5158ccee22d7636bae`（复核来源为云端独立复核，非本地 `verification/` 目录）。2C-1 基线 `2dc0b82` 已在既定离线范围通过独立复核；本文不扩大该结论。

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
- **不得把"Qdrant 生产绑定通过"列为 ES 2C-3 的硬前置。**
- "两后端共享 fingerprint 比较逻辑的离线测试覆盖"与"任一后端的生产可用性"是两件事：前者是共享代码路径的离线证据，后者归 2D 与部署验证；本文只以 ES 侧的绑定/guard 证据作为 2C-3 门槛。

---

## 2. 现状核对（只读，HEAD `a60bcee`）

### 2.1 fail-closed 门禁（四处，缺一不可）

| 文件:行 | 符号 | 现状 |
|---|---|---|
| `server/core/common/embedding.go:25-29` | `common.NewEmbedding` | 显式 `Provider.EmbeddingModel` → 返回 `ErrEmbeddingGuardNotReady` |
| `server/core/rag.go:98-100` | `core.New` | 同上 |
| `server/internal/logic/rag/retriever.go:76-78` | `defaultRagFactory.Create` | 同上 |
| `server/internal/logic/rag/retriever.go:583-585` | `rag.InitWith` | 同上 |

**结论**：2C-3 **不能只删除一个分支**。四处判定必须收敛为同一个"profile 已就绪"判定；否则残留任一入口仍会拒绝，或出现"某入口放行但未绑定/未校验"的不一致状态。

### 2.2 构造入口与现有顺序

- `defaultRagFactory.Create`（`retriever.go:69`）：`normalizeDimensions` → 门禁 → `vector.CheckManagedLifecycle` → `buildRegistry` → `ComputeFingerprintMeta` → `IndexExists` → 受控 `CreateIndex(fp)` → `vector.BindPhysicalTarget` → `core.New` → `VerifyFingerprint` → `SetRegistry`。
- `buildRegistry`（同文件 `:180`）：只做 `config.ResolveEmbeddingDimensions` 校验与 API Key 解析，**构造期不发起网络请求**；`NewProviderProfile`（`core/providers/profile.go:65`）只做字段校验与归一化。
- `core.New`（`rag.go:91`）：`ResolveDimensions` → 门禁 → `ValidatePhysicalName` → Qdrant 拒绝（`:107-109`）→ `ManagedResourceLifecycle`（`:110-112`）→ `VectorIOGuard` 非空（`:113-115`）→ `BuildIndexer` → 条件预热 QA 模型。
- Embedder 生产构造入口只有 `common.NewEmbedding`，调用点三处：`core/indexer/indexer.go:23`、`core/indexer/indexer_async.go:22`、`core/retriever/retriever.go:25`。
- `Registry.GetEmbedder`（`registry.go:246`）目前**无生产调用点**（仅 `providers.FakeRegistry` 测试实现）。
- `BuildEmbedRole`（`adapter.go:142-170`）：当 `p.Dimensions > 0` 时**一律下发** Dimensions 参数；legacy 路径（`embedding.go:56`）只对 `text-embedding-3-` / `Qwen/Qwen3-Embedding-` 前缀下发。两者行为不一致。
- `dimensionEmbedder`（`embedding.go:66-99`）：guard before/after embedding + 响应维度/数量校验。仅 legacy 路径使用。
- `ComputeFingerprintMeta`（`rag.go:285-308`）：P2 后使用与 `NewEmbedding` 同分支条件。
- `ResolveEmbeddingDimensions`（`dimensions.go:63-94`）：新 profile **必须**声明维度，冲突拒绝。

### 2.3 关于"1024 硬编码"的核实结论

- `common.NewEmbedding` **不再有** 1024 字面量：维度取自 `conf.ResolveDimensions()`。属**已贯通维度**，无需重复修改。
- `1024` 的其余出现：集中 legacy 默认值（`dimensions.go:12`）、测试值（`fake.go:269`）、注释、与向量维度无关的值（`rerank.go:113` MaxChunksPerDoc、`knowledge_chunks.go:13` Ext varchar）。
- **真实残留**：新 profile 分支下"是否下发 `Dimensions` 请求参数"的模型能力判定。`BuildEmbedRole` 在 `p.Dimensions > 0` 时**一律下发**；legacy 路径只对特定前缀下发。需给出模型能力声明方案，**不得用新的前缀硬编码补丁掩盖**。

---

## 3. 装配顺序（约束）

1. **身份先于适配器，不调用模型取身份**：fingerprint 的 `provider_name / embedding_model_id / dimensions` 全部由配置元数据计算；不得为了获得身份而构造或试调用模型。
2. **校验先于构造**：配置校验 → 读取既有索引身份/schema 比对 → 绑定物理目标 → 之后才构造适配器。
3. **禁止用构造顺序制造循环依赖**：embedder 的取得必须走显式注入（`conf.Registry`），不得依赖全局 Registry 已发布的时序巧合。
4. **失败不发布、不清理他人资源**。

---

## 4. 解除后仍然保持的门禁与边界

- `ErrResourceBindingNotReady`：Qdrant 生产绑定继续禁用（incarnation/namespace 未决）。
- `vector.managedResourceLifecycle`：`true` 仍仅为运维部署确认，程序不校验部署事实；默认 `false`。
- 具体物理名（`ValidatePhysicalName`）与"单一具体目标"要求不变。
- `BindPhysicalTarget` 的 guard，以及 embedding 前后、BM25、后台读取、列表、删除前 guard 不变。
- 受控同步 bulk、逐项回执校验、无遗留后台 worker 的行为不变。
- 旧索引身份未知/被误标时仍失败关闭：不自动认领、不迁移、不重建。
- 2A 预算 owner/lease 与 2B 取消传播契约不变（不刷新预算、不吞取消）。

---

## 5. 实施设计决策（D1-D7 已确认方案）

### D1：门禁统一判定

**决策**：提取共享 helper `IsActiveProviderEmbedding(conf)` 到 `common/embedding.go`。所有 4 处门禁调用此函数。

**实现**：

```go
// server/core/common/embedding.go
func IsActiveProviderEmbedding(conf *config.Config) bool {
    return conf != nil && conf.Provider != nil && conf.Provider.EmbeddingModel != ""
}
```

**四处替换**：

| 位置 | 当前行为 | 2C-3 行为 |
|---|---|---|
| `common.NewEmbedding` (embedding.go:25) | `IsActiveProviderEmbedding` → 返回 `ErrEmbeddingGuardNotReady` | `IsActiveProviderEmbedding` → 从 `conf.Registry.GetEmbedder()` 获取 embedder，包装 `guardedEmbedder` |
| `core.New` (rag.go:98) | `IsActiveProviderEmbedding` → 返回错误 | `IsActiveProviderEmbedding` → 跳过门禁错误，继续流程 |
| `defaultRagFactory.Create` (retriever.go:76) | `IsActiveProviderEmbedding` → 返回错误 | `IsActiveProviderEmbedding` → 跳过门禁错误，继续流程 |
| `rag.InitWith` (retriever.go:583) | `IsActiveProviderEmbedding` → 返回错误 | `IsActiveProviderEmbedding` → 跳过门禁错误，继续流程 |

### D2：Guard + 响应验证包装

**决策**：重构 `dimensionEmbedder` 为 `guardedEmbedder`，同时服务 legacy 和新 profile 路径。包装任意 `embedding.Embedder` 提供：

1. 调用前 `VectorIOGuard` 检查
2. 调用后 `VectorIOGuard` 检查
3. 响应数量验证（`len(vectors) == len(texts)`）
4. 响应维度验证（每个向量 `len == expected`）
5. **有限值验证**（`math.IsFinite(v)` 对每个元素，防止 NaN/Inf 进入存储）

**实现**：

```go
// server/core/common/embedding.go
// 重命名 dimensionEmbedder → guardedEmbedder
type guardedEmbedder struct {
    inner      schema.Embedder
    conf       *config.Config
    dimensions int
}

func (g *guardedEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float64, error) {
    // 1. pre-call guard check
    if guard := g.conf.VectorIOGuard; guard != nil {
        if err := guard.Check(ctx, "embedding"); err != nil {
            return nil, err
        }
    }
    // 2. delegate to inner
    vectors, err := g.inner.EmbedDocuments(ctx, texts)
    if err != nil {
        return nil, err
    }
    // 3. post-call guard check
    if guard := g.conf.VectorIOGuard; guard != nil {
        if err := guard.Check(ctx, "embedding"); err != nil {
            return nil, err
        }
    }
    // 4. response count validation
    if len(vectors) != len(texts) {
        return nil, fmt.Errorf("embedding: response count %d != request count %d", len(vectors), len(texts))
    }
    // 5. dimension + finite validation
    for i, v := range vectors {
        if len(v) != g.dimensions {
            return nil, fmt.Errorf("embedding: vector[%d] dimension %d != expected %d", i, len(v), g.dimensions)
        }
        for j, val := range v {
            if !math.IsFinite(val) {
                return nil, fmt.Errorf("embedding: vector[%d][%d] is not finite: %v", i, j, val)
            }
        }
    }
    return vectors, nil
}

// EmbedQuery 同理
```

**新 profile 路径**：`common.NewEmbedding` 当 `IsActiveProviderEmbedding(conf)` 时，从 `conf.Registry.GetEmbedder()` 获取 embedder，包装为 `guardedEmbedder`。

**Legacy 路径**：同当前，仅重命名。

### D3：Dimensions 请求参数能力声明

**决策**：在 `ProviderConfig` 中增加 `SupportsDimensionsRequest bool` 字段。当 `true` 且 `Dimensions > 0` 时，`BuildEmbedRole` 下发 Dimensions 参数。当 `false` 时，无论 Dimensions 值如何都不下发。

**实现**：

```go
// server/core/config/config.go
type ProviderConfig struct {
    // ... existing fields ...
    SupportsDimensionsRequest bool `yaml:"supports_dimensions_request"` // model accepts dimensions param
}

// server/core/providers/roles.go
type Capabilities struct {
    Stream                    bool
    StructuredOutput          bool
    ToolCalling               bool
    Usage                     bool
    SupportsDimensionsRequest bool // new: model accepts dimensions parameter
}

// server/core/providers/adapter.go BuildEmbedRole (line ~153-156)
if p.Dimensions > 0 && p.Capabilities.SupportsDimensionsRequest {
    d := p.Dimensions
    ecfg.Dimensions = &d
}
// 否则不下发 Dimensions（即使 p.Dimensions > 0）

// server/core/providers/profile.go NewProviderProfile
// 传递 SupportsDimensionsRequest 到 Capabilities

// server/internal/logic/rag/retriever.go buildRegistry
// 从 ProviderConfig 传递 SupportsDimensionsRequest 到 ProviderProfile
```

**Legacy 路径不变**：仍使用前缀匹配（`text-embedding-3-` / `Qwen/Qwen3-Embedding-`）。

**配置示例**：

```yaml
provider:
  name: bailian
  embedding_model: text-embedding-v4
  embedding_dimensions: 1024
  supports_dimensions_request: true  # text-embedding-v4 支持 dimensions 参数
```

### D4：显式 Registry 装配

**决策**：当前顺序已正确。唯一变更：`common.NewEmbedding` 从 `conf.Registry.GetEmbedder()` 获取 embedder，而非全局 `common.GetRegistry()`。避免全局时序依赖。

**实现**：

- `common.NewEmbedding` 签名已接收 `conf *config.Config`
- 当 `IsActiveProviderEmbedding(conf)` 时：
  1. 检查 `conf.Registry != nil`（必须由 factory 注入）
  2. 调用 `conf.Registry.GetEmbedder(ctx)` 获取 `EmbedRole`
  3. 包装为 `guardedEmbedder`
- `core.New` 已接收 `conf`，且 `conf.Registry` 已由 factory 设置
- `BuildIndexer`/`BuildIndexerAsync`/`BuildRetriever` 已接收 `conf`
- 三处 embedder 构造点（`indexer.go:23`、`indexer_async.go:22`、`retriever.go:25`）已使用 `common.NewEmbedding(ctx, conf)` —— **无需改动**

### D5：身份与维度单源收敛

**决策**：提取共享函数 `ResolveEmbeddingIdentity(conf)` 返回 `(providerName, modelID string, dimensions int, err error)`。`ComputeFingerprintMeta` 和 `common.NewEmbedding` 均调用此函数。

**实现**：

```go
// server/core/common/embedding.go
func ResolveEmbeddingIdentity(conf *config.Config) (providerName, modelID string, dimensions int, err error) {
    if IsActiveProviderEmbedding(conf) {
        return conf.Provider.Name, conf.Provider.EmbeddingModel, conf.EmbeddingDimensions, nil
    }
    // Legacy path
    dims, err := conf.ResolveDimensions()
    if err != nil {
        return "", "", 0, err
    }
    return "legacy", conf.EmbeddingModel, dims, nil
}
```

**调用点**：

- `ComputeFingerprintMeta`（`rag.go:285`）：调用 `ResolveEmbeddingIdentity` 获取身份，替代当前分支逻辑
- `common.NewEmbedding`：调用 `ResolveEmbeddingIdentity` 获取维度用于 `guardedEmbedder` 包装
- P2 修复保持：chat-only Provider（`EmbeddingModel==""`）走 legacy 路径，身份为 `"legacy"`

### D6：错误语义

**决策**：

| 旧错误 | 新错误 | 语义 |
|---|---|---|
| `ErrEmbeddingGuardNotReady` | `ErrEmbeddingProfileNotReady` | profile 已配置但构造失败（Registry 不可用、适配器构建失败） |
| — | `ErrEmbeddingConfigInvalid`（新增） | 配置验证失败（缺少 API key、无效维度） |

**实现**：

```go
// server/core/common/embedding.go
var (
    ErrEmbeddingProfileNotReady = errors.New("embedding: profile configured but not constructible")
    ErrEmbeddingConfigInvalid   = errors.New("embedding: invalid configuration")
)

// 删除 ErrEmbeddingGuardNotReady
```

**替换规则**：

- 所有 `errors.Is(err, ErrEmbeddingGuardNotReady)` → `errors.Is(err, ErrEmbeddingProfileNotReady)`
- `core.New`（rag.go:98-100）：当 active profile 构造失败 → `ErrEmbeddingProfileNotReady`
- `common.NewEmbedding`：当 active profile 构造失败 → `ErrEmbeddingProfileNotReady`
- 已有 `ErrFingerprintMismatch`、`ErrResourceBindingNotReady` 保持不变

### D7：初始化需求调整

**决策**：当 `Provider.EmbeddingModel != ""` 时，跳过 `InitWith` 中 legacy `EmbeddingModel` 和 `ChatModel` 的非空要求。

**实现**：

```go
// server/internal/logic/rag/retriever.go InitWith (line ~593-598)

// Legacy embedding model required only when no Provider embedding
hasProviderEmbedding := cfg.Provider != nil && cfg.Provider.EmbeddingModel != ""
if cfg.EmbeddingModel == "" && !hasProviderEmbedding {
    return fmt.Errorf("rag: embedding.model not configured")
}

// Chat model required only when no Provider chat model
hasProviderChat := cfg.Provider != nil && cfg.Provider.ChatModel != ""
if cfg.ChatModel == "" && !hasProviderChat {
    return fmt.Errorf("rag: chat.model not configured")
}
```

**其他门禁**：

- `core.New`（rag.go:98-100）：删除门禁错误返回，active profile 时继续流程
- `defaultRagFactory.Create`（retriever.go:76-78）：删除门禁错误返回，active profile 时继续流程

---

## 6. 文件变更清单

| 文件 | 变更内容 | 设计项 |
|---|---|---|
| `server/core/common/embedding.go` | 新增 `IsActiveProviderEmbedding`、`ResolveEmbeddingIdentity`；重命名 `dimensionEmbedder` → `guardedEmbedder`（增加 finite 检查）；新增 active profile 分支（经 `conf.Registry.GetEmbedder`）；替换 `ErrEmbeddingGuardNotReady` → `ErrEmbeddingProfileNotReady` + `ErrEmbeddingConfigInvalid` | D1, D2, D5, D6 |
| `server/core/rag.go` | `core.New` 删除门禁错误返回，改用 `IsActiveProviderEmbedding` 判定继续流程；`ComputeFingerprintMeta` 改用 `ResolveEmbeddingIdentity` | D1, D5 |
| `server/internal/logic/rag/retriever.go` | `defaultRagFactory.Create` 删除门禁错误返回；`InitWith` 条件化 legacy 必填项 | D1, D7 |
| `server/core/config/config.go` | `ProviderConfig` 新增 `SupportsDimensionsRequest bool` | D3 |
| `server/core/providers/roles.go` | `Capabilities` 新增 `SupportsDimensionsRequest bool` | D3 |
| `server/core/providers/adapter.go` | `BuildEmbedRole` 检查 `SupportsDimensionsRequest` 后再下发 Dimensions | D3 |
| `server/core/providers/profile.go` | `NewProviderProfile` 传递 `SupportsDimensionsRequest` | D3 |
| `server/internal/logic/rag/retriever.go` `buildRegistry` | 从 `ProviderConfig` 传递 `SupportsDimensionsRequest` | D3 |
| `server/core/common/embedding_guard_test.go` | 更新门禁测试（新错误名、新行为） | D6 |
| `server/core/fingerprint_gate_test.go` | 更新门禁行为测试 | D1 |
| `server/internal/logic/rag/retriever_test.go` | 更新 factory 测试（active profile 场景） | D1, D7 |
| `server/internal/logic/rag/dimensions_test.go` | 更新维度测试 | D3, D5 |
| **新建** `server/core/common/guarded_embedder_test.go` | 测试 `guardedEmbedder`（finite 检查、维度检查、guard before/after） | D2 |
| **新建** `server/core/common/identity_test.go` | 测试 `ResolveEmbeddingIdentity` | D5 |

---

## 7. 生产调用链（2C-3 实施后）

```
YAML config
  → InitWith (retriever.go)
    → normalizeDimensions (ResolveEmbeddingDimensions)
    → [D7: 条件化 legacy 必填项检查]
    → VectorStore factory
    → defaultRagFactory.Create
      → normalizeDimensions
      → [D1: IsActiveProviderEmbedding → 跳过门禁错误]
      → CheckManagedLifecycle
      → buildRegistry (ProviderConfig → ProviderProfile with SupportsDimensionsRequest)
      → ComputeFingerprintMeta → [D5: ResolveEmbeddingIdentity]
      → IndexExists → CreateIndex(fp) → BindPhysicalTarget
      → core.New
        → ResolveDimensions
        → [D1: IsActiveProviderEmbedding → 跳过门禁错误]
        → ValidatePhysicalName → Qdrant reject → Lifecycle check → VectorIOGuard check
        → BuildIndexer → [D4: common.NewEmbedding(ctx, conf)]
          → [D1: IsActiveProviderEmbedding → conf.Registry.GetEmbedder]
          → [D2: wrap guardedEmbedder(guard + dimension + finite check)]
        → BuildIndexerAsync → same embedder path
        → BuildRetriever → same embedder path
      → VerifyFingerprint → SetRegistry
```

---

## 8. 离线验收矩阵

| 用例 | 断言 | 边界 |
|---|---|---|
| Profile 成功路径 | `Registry.GetEmbedder` 返回适配器；身份/维度与 fingerprint 一致；不返回门禁错误 | 离线 fake/httptest |
| 身份不一致阻断 | profile model ≠ fingerprint model → `ErrFingerprintMismatch`，在任何 I/O 前拒绝 | 复用 2C-2 门控路径 |
| 维度不一致阻断 | 声明维度 ≠ 既有索引 → 在适配器构造前失败 | 复用 2C-2 门控路径 |
| fingerprint 缺失/损坏 | 仍 fail-closed；门禁解除不放松 fingerprint 检查 | 2C-2 回归 |
| Chat-only Provider 隔离 | `Provider.EmbeddingModel==""` → legacy 身份 `"legacy"`；P2 回归通过 | P2 回归 |
| 维度单源 | 同一 profile → fingerprint、ES mapping、请求参数均为同一维度 | D5 验证 |
| 取消与预算保持 | guard 失败 → 不发起模型调用；budget owner/lease 不变 | 2A/2B 回归 |
| Qdrant 继续禁用 | `QdrantClient != nil` → `ErrResourceBindingNotReady`，不受 profile 影响 | D1 边界 |
| guardedEmbedder finite 检查 | NaN/Inf 响应 → 在 Store 前报错 | D2 新测试 |
| SupportsDimensionsRequest=false | Dimensions 不在 API 请求中发送（即使 > 0） | D3 新测试 |
| InitWith legacy 跳过 | 新 profile 已配置，legacy `EmbeddingModel` 为空 → 无错误 | D7 新测试 |
| InitWith legacy 仍需 | 无 Provider，legacy `EmbeddingModel` 为空 → 报错 | D7 回归 |

---

## 9. 验证计划

### 9.1 基础验证命令

```powershell
cd c:\Zhy\code\go-rag\server
go build ./...
go vet ./...
go test -p 1 ./core/... ./internal/... -count=1
go test -race -p 1 ./core/common/... ./core/... -count=1
go test -p 1 ./... -count=1
```

仅新增并发/生命周期协调场景需要 `-count=20`。

### 9.2 关键场景压测

```powershell
# guardedEmbedder 并发安全
go test -race -count=20 -run "TestGuardedEmbedder_" ./core/common/...

# ResolveEmbeddingIdentity 一致性
go test -race -count=20 -run "TestResolveEmbeddingIdentity_" ./core/common/...

# InitWith 条件化校验
go test -race -count=20 -run "TestInitWith_" ./internal/logic/rag/...
```

---

## 10. 回滚边界

回滚 = `git revert` 2C-3 提交。不删除、不回填既有 fingerprint 数据，不改写历史验收记录。

---

## 11. 与 2D 的边界

2C-3 只解除"离线可判定的受控 ES 路径门禁"。以下内容归 2D 或部署验证在**单独授权**后进行：

- 真实模型 API 调用（text-embedding-v4 等）
- 真实 ES/Qdrant/MySQL 端到端
- ES 权限设置与"禁止自动创建索引"验证
- `SupportsDimensionsRequest` 真实模型能力验证
- 在线 usage/budget 监控

**2C-3 的离线验收不授权任何 2D 真实模型调用，也不证明真实部署权限已核实。** 2C-3 通过 ≠ 2D 可执行 ≠ 真实设施可用 ≠ Stage 2 完成。

---

## 12. 交付物与退出条件

- **交付物**：代码改动、离线测试与日志、`CHANGES.md`/`PROJECT_PROGRESS.md` 状态更新、独立复核报告（由复核者出具）。
- **退出条件**：§8 离线验收矩阵全部通过，独立复核明确"仅 ES 路径门禁解除、其余门禁与边界未被动"。
- **回滚**：`git revert` 2C-3 提交；不删除、不回填既有 fingerprint 数据，不改写历史验收记录。
