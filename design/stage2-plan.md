# Stage 2 细化实施方案

> **状态**：方案待复核
> **日期**：2026-10-19
> **基线**：`69984c4`（`review/stage1a` 分支）
> **约束**：本轮只读源码、核实依赖、编写方案；不改业务代码；不启动服务；不调用模型 API；不操作 Codex 独立审查 worktree

---

## §1 概述

### 1.1 Stage 2 目标

从"各处读配置、模型写死、Provider 隐式"转变为：

1. **模型角色契约**：Chat / Rewrite / QA / Extract 各自职责明确，哪些角色共享模型、哪些必须独立配置。
2. **Provider 能力声明**：百炼（DashScope）作为目标 Provider，明确 Chat/Embedding/Rerank 各自的 endpoint、请求/响应格式和能力边界。
3. **请求预算（RequestBudget）**：最小可实施语义——每次实际 HTTP 尝试前原子预留请求数、输入估算、最大输出 token、并发和 deadline；预留失败不发送请求。
4. **HTTP transport 统一**：统一 transport 不代表所有 Provider 使用同一种请求协议；保留 Rerank 已验收的输入隔离、结果校验、响应体限制和错误链。
5. **向量维度契约**：Embedding 模型身份及维度进入索引配置；现有索引维度不匹配时拒绝操作。

### 1.2 范围边界

- **不重新审查或扩大 Stage 1 收尾**。Stage 1 代码改进阶段已关闭（基线 `71c4c81`）。
- **不升级 Eino 版本或任何依赖**。当前 eino v0.6.0 保持不变。
- **候选模型及维度不视为已验证事实**。`text-embedding-v4` 1024 维、`qwen3-rerank` 等均为候选，以在线探针实际响应为准。
- **不为 Stage 2 升级依赖**。所有修改在现有 eino v0.6.0 + GoFrame v2.9.0 框架内完成。

### 1.3 分批概览

| 批次 | 内容 | 可离线 | 需真实设施 |
|---|---|---|---|
| 2A | 角色接口 + fake 实现 | ✅ | ❌ |
| 2B | 百炼请求/响应适配 + 错误映射 + 预算 | ✅（httptest） | ❌ |
| 2C | 维度/索引 fingerprint + 不匹配阻断 | ✅（httptest + fake 向量库） | ❌ |
| 2D | 在线探针（仅制定方案，不执行） | ❌ | ✅（需授权凭据） |

---

## §2 模型角色与职责

### 2.1 Chat 角色

| 角色 | 当前源码入口 | 职责 | 必须独立？ |
|---|---|---|---|
| **Chat**（对话） | `common.GetChatModel()` → `openai.NewChatModel` | 流式/单轮对话回答 | 否，可与 Extract 共享配置 |
| **Rewrite**（查询改写） | `common.GetRewriteModel()` → `qwen.NewChatModel` | 检索前查询改写 | 否，但当前使用不同构造器（qwen vs openai） |
| **QA**（问题生成） | `common.GetQAModel()` → `qwen.NewChatModel`（`qaModelCache` 实例级缓存） | 索引时为文档生成 QA 内容 | 否，但已有独立缓存机制 |
| **Extract**（信息抽取） | 当前无独立入口，Stage 2 新增 | 结构化信息抽取 | 设计决定：与 Chat 共享配置，独立角色接口 |

### 2.2 共享与独立配置规则

- **设计决定**：Chat 和 Extract 可共享同一模型配置（同一 Provider + 同一模型名），但通过不同角色接口访问，便于后续独立调整。
- **设计决定**：Rewrite 和 QA 当前使用 `qwen.NewChatModel`，与 Chat 的 `openai.NewChatModel` 构造器不同。Stage 2 不改变这一事实，但在角色接口层统一抽象。
- **已确认**：QA 模型已有 `qaModelCache`（`sync.Once` + 实例级缓存），其他模型（chat/embedding/rerank/rewrite）仍为无锁包级变量（`chatModel`/`embeddingModel`/`rerankModel`/`rewriteModel`），Stage 2 需统一为 Provider 契约。

### 2.3 Embedding 角色

- **当前入口**：`common.NewEmbedding(ctx, conf)` → `openai.NewEmbedder`
- **当前维度处理**：仅 `text-embedding-3-*` 和 `Qwen/Qwen3-Embedding-*` 前缀设 `Dimensions=1024`；默认模型 `text-embedding-3-large`，实际配置用 `BAAI/bge-m3`（不设 Dimensions）
- **Stage 2 目标**：Embedding 角色接口统一 query/document 使用一致模型与配置；维度由模型身份决定并写入索引 fingerprint

### 2.4 Rerank 角色

- **当前入口**：`rerank.NewRerank(conf, httpDoer)` → 独立 HTTP 实现（Stage 1B-1 重构）
- **已验收特性**：输入隔离、结果校验（index/score/NaN/Inf）、响应体 10MB 限制、30s 有界超时、HTTPDoer 接口注入、独立副本输出
- **Stage 2 目标**：保留全部已验收特性；适配百炼 Rerank endpoint；不改变输入/输出契约

---

## §3 源码映射与修改文件

### 3.1 当前入口 → 新入口映射

| 当前入口 | 调用方 | 新入口（Stage 2） | 说明 |
|---|---|---|---|
| `common.GetChatModel(ctx, conf)` | `rag.go:New()` → `x.cm` | `ProviderRegistry.GetChatModel(ctx, role)` | 通过角色+Provider获取 |
| `common.GetRewriteModel(ctx, conf)` | `retriever.go:Retrieve()` | `ProviderRegistry.GetRewriteModel(ctx)` | 角色接口 |
| `common.GetQAModel(ctx, conf)` | `indexer/qa.go:defaultQAModelFactory` | `ProviderRegistry.GetQAModel(ctx)` | 保留实例级缓存 |
| `common.NewEmbedding(ctx, conf)` | `rag.go:New()` → embedder | `ProviderRegistry.GetEmbedder(ctx)` | 维度由 Provider 声明 |
| `rerank.NewRerank(conf, doer)` | `retriever.go:retrieveDoOnce()` | `ProviderRegistry.GetReranker(ctx)` | 保留已验收校验链 |

### 3.2 实际修改文件清单

| 文件 | 修改类型 | 说明 |
|---|---|---|
| `server/core/providers/registry.go` | **新增** | ProviderRegistry 接口 + 默认实现 |
| `server/core/providers/roles.go` | **新增** | ChatRole/RewriteRole/QARole/ExtractRole/EmbedRole/RerankRole 接口定义 |
| `server/core/providers/budget.go` | **新增** | RequestBudget 结构体与原子预留逻辑 |
| `server/core/providers/transport.go` | **新增** | 统一 HTTP transport（共享 `*http.Client`，可注入） |
| `server/core/providers/fake.go` | **新增** | 全角色 fake 实现（离线测试用） |
| `server/core/providers/bailian/` | **新增** | 百炼 Provider 适配（2B 批次） |
| `server/core/common/chat_model.go` | **修改** | 委派 ProviderRegistry；保留现有签名兼容层 |
| `server/core/common/embedding.go` | **修改** | 委派 ProviderRegistry；移除硬编码维度前缀判断 |
| `server/core/config/config.go` | **修改** | 新增 Provider 配置块（ProviderName/BaseURL/APIKey/Model 映射） |
| `server/core/vector/es.go` | **修改** | CreateIndex 维度从硬编码 1024 改为从 Embedding 配置读取 |
| `server/core/vector/qdrant.go` | **修改** | 同上 |
| `server/core/rag.go` | **修改** | New() 从 ProviderRegistry 获取模型；传递维度到向量存储 |
| `server/internal/logic/rag/retriever.go` | **修改** | readRagConfig 读取 Provider 配置块 |
| `server/core/rerank/rerank.go` | **不修改** | 保留全部已验收校验链 |

### 3.3 如何替换现有全局模型缓存

- **当前**：`chatModel`/`embeddingModel`/`rerankModel`/`rewriteModel` 为无锁包级变量，首次调用懒加载；QA 有 `qaModelCache`（实例级 `sync.Once`）。
- **Stage 2 方案**：引入 `ProviderRegistry` 接口，内部使用 `sync.RWMutex` 保护模型实例缓存。`GetChatModel` 等公开函数在兼容层内委派 Registry，不改变调用方签名（2A 阶段）；后续批次逐步迁移调用方直接使用 Registry。
- **设计决定**：不一次性删除现有包级变量，通过兼容层渐进迁移，避免单次改动过大。

### 3.4 Eino 类型与业务接口之间的适配边界

- **已确认**：`model.BaseChatModel`（Eino）为 Chat 角色的底层接口；`embedding.Embedder`（Eino）为 Embedding 角色的底层接口。
- **设计决定**：角色接口在 Eino 类型之上增加 Provider 能力声明（如是否支持流式、structured output、usage 返回），不假设所有 Provider 均支持。
- **适配边界**：业务代码通过角色接口访问模型；角色接口的实现内部持有 Eino 模型实例。Eino 类型不直接暴露给 `rag.go` 以上的业务层。

### 3.5 配置缺失、非法配置和初始化失败的错误传播

- **设计决定**：Provider 配置缺失（无 APIKey / 无 BaseURL / 无模型名）在 `New()` 或 `InitWith()` 时返回明确错误，不延迟到首次模型调用。
- **设计决定**：非法配置（如 ProviderName 不在已知列表）在启动阶段报错，不静默回退默认 Provider。
- **已确认**：当前 `embedding.go` 已有 APIKey/BaseURL 从 conf 或环境变量读取的逻辑；Stage 2 将此逻辑统一到 Provider 配置块。

### 3.6 默认测试不连接真实设施

- **设计决定**：所有离线测试使用 fake 实现（`providers/fake.go`），不连接真实模型/向量库/数据库。
- **已确认**：Stage 1 已通过 `//go:build integration` 和 `//go:build online` 标签隔离真实设施测试。

### 3.7 流式 reader 的关闭、取消和资源所有权

- **已确认**：Stage 1C 已处理流式 EOF/错误语义（`stream.go`）、成功路径关闭前台分支（`chat.go:streamCoordinator`）。
- **设计决定**：Stage 2 不改变流式 reader 的所有权规则；Provider 层的流式实现必须将 `model.BaseChatModel.Stream()` 返回的 reader 资源所有权传递给调用方，Provider 层不消费 reader。

---

## §4 成本与请求预算

### 4.1 RequestBudget 最小可实施语义

```go
type RequestBudget struct {
    MaxRequests     int64         // 最大请求数
    MaxInputTokens  int64         // 输入 token 估算上限
    MaxOutputTokens int64         // 最大输出 token 上限
    MaxConcurrency  int           // 并发上限
    Deadline        time.Duration // 请求级 deadline
}
```

- **设计决定**：RequestBudget 是"每次 HTTP 尝试前的原子预留"机制，不是精确费用上限。
- **不将 token 估算声称为精确费用上限**：输入 token 基于文本长度的粗略估算（字符数/系数），实际消耗以 Provider 返回的 usage 为准。

### 4.2 预算作用域

| 作用域 | 说明 | 实现 |
|---|---|---|
| **请求级** | 单次 `Retrieve()` 或 `Index()` 调用内的预算 | context 携带 `*RequestBudget` |
| **进程级** | 整个进程的全局预算上限 | `ProviderRegistry` 持有全局 `atomic.Int64` 计数器 |

### 4.3 原子预留规则

- 每次实际 HTTP 尝试前：`remaining = atomic.AddInt64(&counter, -1)`；若 `remaining < 0` 则回退预留并返回预算耗尽错误。
- **预留失败不发送请求**：预算不足时返回 `ErrBudgetExhausted`，不发起 HTTP 调用。
- **重试也计入预算**：自动重试的唯一所有者是 RequestBudget；每次重试预留新的请求数/token 额度。

### 4.4 预留退款/结算规则

| 场景 | 处理 |
|---|---|
| **usage 缺失** | 记录 `unknown`，不显示 0；保守估算（按请求最大输入估算） |
| **流中断** | 已预留的 token 不退款；记录中断时已消耗的输入/输出 |
| **网络失败** | 预留的请求数退回（未实际消耗 Provider 额度）；token 估算不退回 |
| **正常完成** | 按 usage 实际值结算；若 usage 不可用则保留估算值 |

### 4.5 自动重试

- **设计决定**：RequestBudget 是自动重试的唯一所有者；底层 HTTP transport 不做独立重试。
- **重试条件**：网络超时、5xx 错误；不重试 4xx 错误。
- **重试上限**：由 RequestBudget 的 `MaxRequests` 控制，不硬编码重试次数。

### 4.6 禁止静默切换

- **设计决定**：预算耗尽、Provider 不可用、模型错误等场景下，不得静默切换到其他模型/Provider/计费方式。
- 错误必须向上传播，由调用方决定是否降级。

### 4.7 无法精确控制金额时的说明

- 当 Provider 的 usage 返回不包含 token 信息（如某些代理/中转服务），无法精确计算费用。此时如实说明"费用估算不可用"，不编造数字。

---

## §5 Embedding 与向量维度

### 5.1 响应校验

- **响应数量**：Embedding API 返回的 `data` 数组长度必须与请求的 `input` 数组长度一致；不一致时报错。
- **index/顺序**：每个 `data[i].index` 必须与输入下标对应；不依赖隐式顺序。
- **维度**：返回向量的维度必须与模型声明的维度一致；不一致时拒绝写入索引。
- **有限数值校验**：向量元素必须为有限浮点数（非 NaN、非 Inf）。

### 5.2 query/document 一致性

- **设计决定**：query 和 document 使用一致模型与配置。不在检索时使用与索引时不同的 Embedding 模型。
- **已确认**：当前 `rag.go:New()` 创建一个 embedder 同时用于索引和检索。

### 5.3 模型身份及维度进入索引配置

- **设计决定**：`ProviderRegistry` 在初始化 Embedding 角色时声明模型身份（model ID）和输出维度。
- **维度来源**：优先使用 Provider 声明的维度；若 Provider 不支持声明，则使用配置中指定的维度。
- **索引 fingerprint**：索引创建时记录 `{model_id, dimensions}` 指纹；后续写入/检索时校验指纹匹配。

### 5.4 维度不匹配时拒绝操作

- **设计决定**：如果现有索引的维度与当前 Embedding 配置的维度不一致，拒绝写入/检索操作并返回明确错误。
- **已确认**：当前 ES 和 Qdrant 均硬编码 1024 维。

### 5.5 ES/Qdrant 两条实际路径与当前写死维度位置

| 向量存储 | 写死维度位置 | 当前值 |
|---|---|---|
| **ES** | `es.go:CreateIndex()` → `FieldContentVector` 和 `FieldQAContentVector` | `Dims: of(1024)` |
| **Qdrant** | `qdrant.go:CreateIndex()` → 两个命名向量 | `Size: 1024` |

- 两处均需改为从 Embedding 配置/Provider 声明读取维度。

### 5.6 无真实向量库时能验证什么

- **可离线验证**：维度 fingerprint 匹配/不匹配逻辑（使用 fake VectorStore）；维度不匹配时的错误路径；Embedding 响应校验（数量/index/维度/NaN/Inf）。
- **不可离线验证**：ES/Qdrant 实际 mapping 与维度往返兼容；真实 Embedding API 返回维度与声明一致。

---

## §6 Provider 与 HTTP 契约

### 6.1 Chat 契约

| 能力 | 百炼（已确认/待探针确认） | 说明 |
|---|---|---|
| 单轮 Generate | ✅ 已确认（OpenAI 兼容） | `POST /compatible-mode/v1/chat/completions` |
| 流式 Stream | ✅ 已确认（OpenAI 兼容） | `stream: true`，`stream_options.include_usage: true` |
| 结构化输出 | ⚠️ 待探针确认 | 百炼文档提及支持，但不假设 JSON Schema/tool calling 兼容 |
| usage 返回 | ✅ 已确认 | 非流式在 `response.usage`；流式在最后一 chunk |
| 取消/超时 | ✅ 已确认 | context 取消 → HTTP 请求中止 |

### 6.2 Embedding 契约

| 项目 | 百炼（已确认/待探针确认） | 说明 |
|---|---|---|
| endpoint | ✅ 已确认（OpenAI 兼容） | `POST /compatible-mode/v1/embeddings` |
| 批次限制 | ✅ 已确认 | 单次最多 10 条文本 |
| 返回映射 | ✅ 已确认 | `data[].embedding` + `data[].index` + `usage` |
| 维度 | ✅ 已确认 | `text-embedding-v4` 默认 1024，支持 64-2048 |
| 单条 token 限制 | ✅ 已确认 | 每条最大 8192 token |

### 6.3 Rerank 契约

| 项目 | 百炼（已确认/待探针确认） | 说明 |
|---|---|---|
| qwen3-rerank endpoint | ✅ 已确认 | `POST /compatible-api/v1/reranks`（OpenAI 兼容路径） |
| gte-rerank-v2 endpoint | ✅ 已确认 | `POST /api/v1/services/rerank/text-rerank/text-rerank`（DashScope 原生路径） |
| 请求体结构 | ✅ 已确认 | qwen3-rerank: 扁平结构 `{model, query, documents, top_n}`；gte-rerank-v2: 嵌套结构 `{model, input: {query, documents}, parameters: {top_n}}` |
| 结果映射 | ✅ 已确认 | 返回 `results[]`，每项含 `index` + `relevance_score` |
| 文档数限制 | ✅ 已确认 | qwen3-rerank: 500；gte-rerank-v2: 未明确但推荐 ≤100 |
| gte-rerank 下线 | ✅ 已确认 | 2026-05-30 下线，推荐迁移到 qwen3-rerank |

**重要**：保留 Rerank 已验收的输入隔离、结果校验（index/score/NaN/Inf/重复/越界）、响应体 10MB 限制、30s 有界超时和错误链。Stage 2 的百炼 Rerank 适配在此基础上增加 endpoint/请求体/响应解析。

### 6.4 统一 transport 不等于同一种请求协议

- **设计决定**：统一 transport 提供共享的 `*http.Client`、超时、重试和预算集成，但 Chat/Embedding 走 OpenAI 兼容协议，Rerank 可能走 DashScope 原生协议。
- 每种协议有独立的请求构建和响应解析逻辑。

---

## §7 分批计划与真实测试

### 7.1 批次 2A：角色接口 + fake 实现

**目标**：定义所有角色接口；提供 fake 实现；离线测试覆盖所有角色。

**修改文件**：
- `server/core/providers/roles.go`（新增）
- `server/core/providers/registry.go`（新增）
- `server/core/providers/budget.go`（新增）
- `server/core/providers/transport.go`（新增）
- `server/core/providers/fake.go`（新增）
- `server/core/providers/roles_test.go`（新增）
- `server/core/providers/budget_test.go`（新增）

**接口签名草案**：
```go
// roles.go
type ChatRole interface {
    Generate(ctx context.Context, messages []*schema.Message) (*schema.Message, error)
    Stream(ctx context.Context, messages []*schema.Message) (model.StreamReader, error)
}

type EmbedRole interface {
    EmbedDocuments(ctx context.Context, texts []string) ([][]float64, error)
    EmbedQuery(ctx context.Context, text string) ([]float64, error)
    Dimensions() int
    ModelID() string
}

type RerankRole interface {
    Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)
}

// registry.go
type ProviderRegistry interface {
    GetChat(ctx context.Context) (ChatRole, error)
    GetRewrite(ctx context.Context) (ChatRole, error)
    GetQA(ctx context.Context) (ChatRole, error)
    GetEmbedder(ctx context.Context) (EmbedRole, error)
    GetReranker(ctx context.Context) (RerankRole, error)
}

// budget.go
type RequestBudget struct { ... }
func (b *RequestBudget) Reserve(n int64) error
func (b *RequestBudget) Release(n int64)
```

**依赖**：无新外部依赖。

**验收测试**：
- fake 覆盖所有角色（Chat/Embed/Rerank/Rewrite/QA/Extract）
- 预算并发争用（atomic 预留 + race）
- 预算不足返回 `ErrBudgetExhausted`
- 取消传播

**回滚边界**：2A 仅新增文件 + 兼容层，不改变现有调用方行为。回滚 = 删除 `providers/` 目录。

### 7.2 批次 2B：百炼适配 + 错误映射

**目标**：实现百炼 Provider 的 Chat/Embedding/Rerank 适配；错误映射；预算集成。

**修改文件**：
- `server/core/providers/bailian/chat.go`（新增）
- `server/core/providers/bailian/embedding.go`（新增）
- `server/core/providers/bailian/rerank.go`（新增）
- `server/core/providers/bailian/chat_test.go`（新增，httptest）
- `server/core/providers/bailian/embedding_test.go`（新增，httptest）
- `server/core/providers/bailian/rerank_test.go`（新增，httptest）
- `server/core/config/config.go`（修改：Provider 配置块）

**依赖**：复用现有 eino-ext openai/qwen 组件；不新增外部依赖。

**验收测试**：
- httptest 模拟百炼 endpoint：正常响应、错误码、畸形 JSON、usage 缺失、流中断
- 重试计费（重试计入预算）
- 取消/超时传播
- Rerank 适配保留已验收校验链

**回滚边界**：2B 新增 `bailian/` 子目录，不改现有 `rerank/rerank.go`。回滚 = 删除 `bailian/` + 回退 `config.go`。

### 7.3 批次 2C：维度/索引 fingerprint

**目标**：Embedding 维度进入索引配置；维度不匹配时拒绝操作。

**修改文件**：
- `server/core/vector/es.go`（修改：维度从配置读取）
- `server/core/vector/qdrant.go`（修改：维度从配置读取）
- `server/core/vector/interface.go`（修改：Config 增加维度字段）
- `server/core/rag.go`（修改：传递维度到向量存储）
- `server/core/vector/fingerprint_test.go`（新增）

**依赖**：无新外部依赖。

**验收测试**：
- 维度匹配/不匹配（fake VectorStore）
- fingerprint 记录与校验
- ES/Qdrant CreateIndex 使用配置维度

**回滚边界**：2C 修改 `vector/` 和 `rag.go`，需回退维度硬编码。回滚 = `git revert` 2C 提交。

### 7.4 批次 2D：在线探针方案（仅制定，不执行）

见 §8。

### 7.5 测试必须调用生产共享实现

- 所有测试使用 `providers/fake.go` 的 fake 实现，但通过 `ProviderRegistry` 接口注入，与生产装配路径一致。
- 不使用独立于生产代码的测试专用模型构造逻辑。

### 7.6 推荐首批

**推荐首批为 2A**：角色接口 + fake 实现。理由：
- 纯新增文件，不修改现有代码
- 为后续批次提供接口基础
- 完全可离线验证
- 回滚成本最低

---

## §8 在线探针方案（2D，仅制定不执行）

### 8.1 前置条件

- 2A/2B/2C 离线测试全部通过
- 获得已授权的百炼 API Key
- RequestBudget 已配置极小预算上限

### 8.2 所需环境变量

```bash
BAILIAN_API_KEY=sk-xxx                    # 百炼 API Key
BAILIAN_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1  # 北京地域
BAILIAN_WORKSPACE_ID=xxx                  # 业务空间 ID（可选，用于专属域名）
PROBE_ENABLED=true                        # 探针总开关
PROBE_MAX_REQUESTS=5                      # 探针最大请求数
PROBE_MAX_TOKENS=1000                     # 探针最大 token
```

### 8.3 模型/endpoint 候选

| 角色 | 候选模型 | endpoint | 说明 |
|---|---|---|---|
| Chat | `qwen-plus` 或 `qwen-turbo` | `/compatible-mode/v1/chat/completions` | 低成本模型 |
| Embedding | `text-embedding-v4` | `/compatible-mode/v1/embeddings` | 默认 1024 维 |
| Rerank | `qwen3-rerank` | `/compatible-api/v1/reranks` | gte-rerank-v2 即将下线 |

### 8.4 最小调用矩阵

| 探针 | 输入 | 预期输出 | 验证项 |
|---|---|---|---|
| Chat 单轮 | "Hello" | 非空回复 + usage | 连通性、usage 格式 |
| Chat 流式 | "Hello" | SSE 事件流 + 最终 usage | 流式格式、include_usage |
| Embedding 单条 | "test" | 1024 维向量 | 维度、数值有限 |
| Embedding 批量 | ["a", "b", "c"] | 3 个向量，index 正确 | 数量、顺序、维度一致 |
| Rerank | query + 3 docs | 排序结果 + score | index 映射、score 有限 |

### 8.5 预算预留与停止条件

- 每个探针调用前预留 RequestBudget（1 请求 + 输入估算 token + 最大输出 token）
- 预留失败 → 停止后续探针
- 任何探针返回非预期错误 → 停止后续探针，记录错误
- 总预算耗尽 → 停止

### 8.6 脱敏证据

- 探针日志不记录 API Key 明文
- 模型原始输出仅记录 token 数量和耗时，不记录完整内容
- 证据文件存放 `verification/stage2d/`

### 8.7 无可用凭据或未取得授权时

- 保持 2D 为"未执行"状态
- 不编造探针结果
- 离线测试（2A/2B/2C）结果不受影响

### 8.8 A-4 缺 QA 向量检索行为

- **已确认**：QA 关闭时 `qa_content_vector` 字段在 ES mapping 中可能缺失；检索器 `retrieveDoOnce` 中 `x.retrieve(ctx, req, true)` 使用该字段做 kNN。
- **待探针确认**：ES 在 `qa_content_vector` 字段缺失时的 kNN 行为（报错？空结果？）。
- **设计决定**：2D 探针包含一项验证：QA 关闭状态下检索器的行为。

### 8.9 ES 启动兼容验证

- **已确认**：ES 客户端 8.16.0 vs 服务端 8.11.3（Compose）/ 8.18.0（README）。
- **待验证**：实际连接、索引创建、mapping 兼容、kNN 查询往返。
- **归属**：ES 启动验证单列为独立验证项（总方案 0C/启动验证），不与 2D 模型探针混合。

---

## §9 验收表

### 9.1 批次验收条件

| 批次 | 验收条件 | 回滚边界 |
|---|---|---|
| **2A** | fake 覆盖所有角色；预算并发争用 race 通过；预算不足返回错误；取消传播；`go test -race ./core/providers/...` exit 0 | 删除 `providers/` 目录 |
| **2B** | httptest 模拟百炼正常/错误/畸形/流中断/usage 缺失均通过；重试计入预算；Rerank 保留已验收校验链；`go test -race ./core/providers/bailian/...` exit 0 | 删除 `bailian/` + 回退 `config.go` |
| **2C** | 维度 fingerprint 匹配/不匹配逻辑正确；ES/Qdrant CreateIndex 使用配置维度；维度不匹配阻断写入/检索 | `git revert` 2C 提交 |
| **2D** | 探针在授权预算内执行；所有候选模型最小调用通过；脱敏证据完整；A-4 QA 向量行为记录 | 不影响离线代码（探针为只读验证） |

### 9.2 整体验收

- 2A + 2B + 2C 离线全部通过后，方可执行 2D 探针。
- 2D 探针通过后，Stage 2 方案阶段结束，进入实施阶段。

---

## §10 事实确认状态

### 10.1 已确认（源码或官方资料）

| 结论 | 来源 | 核查日期 |
|---|---|---|
| 百炼 Chat API 兼容 OpenAI | 官方文档 help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope | 2026-10-19 |
| 百炼 Chat base_url 格式 | `https://dashscope.aliyuncs.com/compatible-mode/v1`（北京）或 `https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`（专属域名） | 同上 |
| 百炼 Embedding 使用 OpenAI 兼容接口 | `POST /compatible-mode/v1/embeddings` | 同上 |
| `text-embedding-v4` 默认 1024 维，支持 64-2048 | 官方文档 help.aliyun.com/zh/model-studio/text-embedding-v4 | 2026-10-19 |
| `text-embedding-v4` 单次最多 10 条文本 | 官方文档 + 社区实践 | 2026-10-19 |
| 百炼 Rerank qwen3-rerank endpoint | `POST /compatible-api/v1/reranks`（OpenAI 兼容路径） | help.aliyun.com/zh/model-studio/text-rerank-api，2026-10-19 |
| 百炼 Rerank gte-rerank-v2 endpoint | `POST /api/v1/services/rerank/text-rerank/text-rerank`（DashScope 原生路径） | 同上 |
| gte-rerank 系列将于 2026-05-30 下线 | 官方文档明确标注 | 同上 |
| qwen3-rerank 最大 500 文档，单条 4000 token，请求最大 120000 token | 官方文档 | 同上 |
| 当前源码 chatModel/embeddingModel/rewriteModel 为无锁包级变量 | 源码 `chat_model.go` | 2026-10-19 |
| QA 模型有 `qaModelCache` 实例级缓存 | 源码 `chat_model.go` | 2026-10-19 |
| ES/Qdrant 硬编码 1024 维 | 源码 `es.go:CreateIndex()` / `qdrant.go:CreateIndex()` | 2026-10-19 |
| Rerank 已有完整校验链（Stage 1B-1） | 源码 `rerank/rerank.go` | 2026-10-19 |
| 当前使用 SiliconFlow 作为 baseURL 代理 | `config_demo.yaml` | 2026-10-19 |
| Eino v0.6.0，GoFrame v2.9.0，ES client 8.16.0 | `go.mod` | 2026-10-19 |

### 10.2 设计决定

| 决定 | 理由 |
|---|---|
| Chat 和 Extract 共享配置，独立角色接口 | 当前无 Extract 入口，预留扩展 |
| 不一次性删除包级变量，通过兼容层渐进迁移 | 避免单次改动过大 |
| RequestBudget 是自动重试的唯一所有者 | 避免底层 transport 独立重试导致预算失控 |
| 维度 fingerprint 记录在索引创建时 | 确保写入/检索与索引一致 |
| 不假设 JSON Schema/tool calling 兼容 | 百炼文档提及支持但未经探针验证 |
| 统一 transport 不等于同一协议 | Chat/Embedding 走 OpenAI 兼容，Rerank 可能走 DashScope 原生 |

### 10.3 待探针确认

| 事项 | 候选值 | 探针验证方式 |
|---|---|---|
| `text-embedding-v4` 实际返回维度 | 默认 1024 | 探针 Embedding 单条，检查返回向量长度 |
| `qwen3-rerank` 实际响应格式 | OpenAI 兼容 `{id, results: [{index, relevance_score}]}` | 探针 Rerank，检查响应结构 |
| 百炼 Chat 是否支持 structured output | 文档提及 | 探针发送 JSON Schema 请求 |
| 百炼 Chat 流式 usage 格式 | `stream_options.include_usage: true` → 最后一 chunk | 探针流式调用 |
| ES 在 `qa_content_vector` 缺失时的 kNN 行为 | 报错/空结果 | 需真实 ES 实例 |
| ES 客户端 8.16.0 vs 服务端 8.11.3 兼容 | 未知 | 需真实 ES 实例（归属 0C/启动验证） |

### 10.4 官方文档链接

| API | 文档链接 | 核查日期 |
|---|---|---|
| Chat（OpenAI 兼容） | https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope | 2026-10-19 |
| Embedding（text-embedding-v4） | https://help.aliyun.com/zh/model-studio/text-embedding-v4 | 2026-10-19 |
| Rerank（文本排序） | https://help.aliyun.com/zh/model-studio/text-rerank-api | 2026-10-19 |
| Base URL 总览 | https://help.aliyun.com/zh/model-studio/base-url | 2026-10-19 |
| 模型列表 | https://help.aliyun.com/zh/model-studio/what-is-model-studio | 2026-10-19 |
