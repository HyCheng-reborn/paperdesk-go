# Stage 2 细化实施方案

> **状态**：2A 剩余阻塞修复完成（2026-10-19），待再次复核；2B 阻塞修复完成（2026-10-20 第三轮），待再次复核；2C/2D 待实施
> **日期**：2026-10-20
> **基线**：实施 `212ccd8`；修复 `2780e39`；2B 实施 `df260dd`；2B 阻塞修复（一轮）`a24cb8d`；2B 阻塞修复（二轮）`be28e91`；2B 阻塞修复（三轮）`745c443`（`review/stage1a` 分支）
> **约束**：本轮只修订方案文档，不改业务代码；不启动服务；不调用模型 API；不操作 Codex 独立审查 worktree

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
| 2A | 角色接口 + 共享预算核心 + 生产共享链 | ✅ | ❌ |
| 2B | 百炼请求/响应适配 + 错误映射 + 预算 + 默认工厂与业务接线 | ✅（httptest） | ❌ |
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
- **已确认**：QA 模型已有 `qaModelCache`（`sync.Mutex` + `ready bool` + 实例级缓存），失败后可重试；其他模型（chat/embedding/rerank/rewrite）仍为无锁包级变量（`chatModel`/`embeddingModel`/`rerankModel`/`rewriteModel`），Stage 2 需统一为 Provider 契约。

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
| `common.GetQAModel(ctx, cfg)` | `indexer/qa.go:defaultQAModelFactory` | `ProviderRegistry.GetQAModel(ctx)` | 保留实例级缓存 |
| `common.NewEmbedding(ctx, conf)` | `rag.go:New()` → embedder | `ProviderRegistry.GetEmbedder(ctx)` | 维度由 Provider 声明 |
| `rerank.NewRerank(conf, doer)` | `retriever.go:retrieveDoOnce()` | `ProviderRegistry.GetReranker(ctx)` | 保留已验收校验链 |

### 3.1.1 默认创建→注入→业务调用→HTTP 尝试完整映射表

> **已确认**：以下为当前源码中模型实例的完整创建链路，Stage 2 必须全部接管。

| 角色 | 默认创建位置 | 创建方式 | 业务调用方 | HTTP 尝试 |
|---|---|---|---|---|
| **Chat** | `internal/logic/chat/chat.go:45-50` | `openai.NewChatModel(ctx, &openai.ChatModelConfig{...})` | `rag.go:New()` → `x.cm` | Eino 内部 HTTP |
| **Embedding** (indexer) | `core/indexer/indexer.go:19` | `common.NewEmbedding(ctx, conf)` → `openai.NewEmbedder` | `indexer.go` ES/Qdrant indexer | Eino 内部 HTTP |
| **Embedding** (async indexer) | `core/indexer/indexer_async.go:18` | `common.NewEmbedding(ctx, conf)` → `openai.NewEmbedder` | `indexer_async.go` ES/Qdrant indexer | Eino 内部 HTTP |
| **Embedding** (retriever) | `core/retriever/retriever.go:25` | `common.NewEmbedding(ctx, conf)` → `openai.NewEmbedder` | `retriever.go` ES/Qdrant retriever | Eino 内部 HTTP |
| **Rewrite** | `core/retriever/retriever.go:76` | `common.GetRewriteModel(ctx, nil)` → `qwen.NewChatModel` | `retriever.go:Retrieve()` | Eino 内部 HTTP |
| **Rerank** | `core/retriever/retriever.go:261` | `rerank.NewRerank(cfg, nil)` | `retriever.go:retrieveDoOnce()` | `rerank.go` 独立 HTTP |
| **QA 预热** | `core/rag.go:106` | `common.GetQAModel(ctx, nil)` → `qwen.NewChatModel` | `indexer/qa.go` via factory | Eino 内部 HTTP |

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

- **当前**：`chatModel`/`embeddingModel`/`rerankModel`/`rewriteModel` 为无锁包级变量，首次调用懒加载；QA 有 `qaModelCache`（`sync.Mutex` + `ready bool` + 实例级缓存，失败后可重试）。
- **Stage 2 方案**：引入 `ProviderRegistry` 接口，内部使用 `sync.RWMutex` 保护模型实例缓存。`GetChatModel` 等公开函数在兼容层内委派 Registry，不改变调用方签名（2A 阶段）；后续批次逐步迁移调用方直接使用 Registry。
- **设计决定**：不一次性删除现有包级变量，通过兼容层渐进迁移，避免单次改动过大。

### 3.3.1 Registry 构造的不可变 ProviderProfile

- **设计决定**：每个 Registry 实例在构造时接收一个不可变 `ProviderProfile`，一旦创建不可修改。
- **Profile 键至少覆盖**：
  - `ProviderName`（如 `bailian`、`openai`）
  - `Protocol`（如 `openai-compatible`、`dashscope-native`）
  - `Endpoint` / `Region`（如 `dashscope.aliyuncs.com/compatible-mode/v1`）
  - `Model` / `Version`（如 `qwen-plus`、`text-embedding-v4`）
  - `Dimensions`（Embedding 角色专用，如 `1024`）
  - `KeyOptions`（关键选项，如 `stream`、`structuredOutput`、`toolCalling`）
  - `CredentialIdentity`（凭据身份标识，如 API Key 的 hash 或 ID，不存明文）
- **进程预算由进程 owner 持有**：`ProcessBudget` 由进程启动时创建，Registry 构造时绑定；不能通过新建 Registry 重置全局额度。
- **模型缓存 per Profile 隔离**：不同 Profile 的模型实例独立缓存，不共享。

### 3.3.2 兼容层收到非 nil 配置必须参与身份

- **设计决定**：当兼容层（`common.GetChatModel` 等）收到非 nil 配置时，必须用该配置构建 Profile 并参与身份匹配，不能仍由旧的无键单例抢先返回。
- 仅当配置为 nil 时，才走默认 Profile（向后兼容）。

### 3.4 Eino 类型与业务接口之间的适配边界

- **已确认**：`model.BaseChatModel`（Eino）为 Chat 角色的底层接口；`embedding.Embedder`（Eino）为 Embedding 角色的底层接口。
- **设计决定**：角色接口在 Eino 类型之上增加 Provider 能力声明（如是否支持流式、structured output、usage 返回），不假设所有 Provider 均支持。
- **适配边界**：业务代码通过角色接口访问模型；角色接口的实现内部持有 Eino 模型实例。Eino 类型不直接暴露给 `rag.go` 以上的业务层。

### 3.4.1 Role↔Eino 适配策略

- **设计决定**：选择嵌入 Eino interface 方式，角色接口的实现内部持有 Eino 模型实例并适配其签名；不引入显式 adapter 层，除非后续发现必要。
- **已确认**：Eino v0.6.0 的 Stream 返回类型为 `*schema.StreamReader[*schema.Message]`（schema 包泛型 struct 指针），不是 `model.StreamReader` 接口。

### 3.4.2 角色接口签名（锁定 Eino v0.6.0）

```go
// ChatRole —— Generate/Stream 均包含 opts 参数
// Stream 返回 *schema.StreamReader[*schema.Message]（schema 包泛型 struct，不是 model.StreamReader 接口）
type ChatRole interface {
    Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error)
    Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

// EmbedRole —— 采用 EmbedStrings 与 Eino embedding.Embedder 对齐
type EmbedRole interface {
    EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
    Dimensions() int
    ModelID() string
}

type RerankRole interface {
    Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)
}
```

### 3.4.3 Capabilities 最小结构

- **设计决定**：每个 ProviderProfile 关联一个 `Capabilities` struct，声明该 Provider 支持的能力：

```go
type Capabilities struct {
    Stream           bool // 是否支持流式
    StructuredOutput bool // 是否支持结构化输出（JSON Schema / tool calling）
    ToolCalling      bool // 是否支持 tool calling
    Usage            bool // 是否返回 usage 信息
}
```

- 不假设所有 Provider 均支持上述能力；Capabilities 在 Profile 构造时确定，不可变。

### 3.4.4 Extract 角色定位

- **设计决定**：Extract 先作为 ChatRole 的角色标识（alias），与 Chat 共享同一模型配置和底层实例，但通过不同角色接口访问。
- 即：`ProviderRegistry.GetExtract(ctx)` 返回 `ChatRole`，与 `GetChat(ctx)` 可返回同一实例（或独立实例，取决于 Profile 配置）。
- 不独立获取入口，除非后续发现需要独立的模型配置或能力。

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
- **设计决定**：Provider 层若包装 reader/body 用于观察终结（如 finalize slot），必须为透明包装，不与消费者抢读，不新增裸 goroutine 包装阻塞调用；不改变 Stage 1 SSE EOF/错误和流关闭规则。

---

## §4 成本与请求预算

### 4.1 四种计数器

> **设计决定**：预算系统维护四种独立计数器，各自生命周期不同。

| 计数器 | 说明 | 消耗时机 | 释放时机 |
|---|---|---|---|
| **总发送尝试计数** | **发送前**原子预留，发送后单调消耗（不退款）；reserved → committed 状态机 | 发送前预留（reserved）→ 进入实际发送边界前原子承诺（committed） | 不释放（单调递减） |
| **在途并发 slot** | 请求开始时占，幂等 finalize 释放 | 请求开始时占 slot | stream: 四种路径（EOF、终止性读错、取消后关闭、提前 Close）共用**幂等 finalize**，任一路径 finalize 一次即释放；非流式: body 消费/解析完成并关闭（或读错/取消后关闭） |
| **token 预留账本** | 输入+输出预留 token 额度 | 发送前预留 | 按 usage 结算后释放差额；usage 缺失时保守保留预留 |
| **实际结算账本** | 按 usage 实际值结算 | 响应完成后 | 单调消耗，不释放 |

**reserved → committed 状态机**：
- `reserved`：发送前原子预留成功，尚未进入实际 HTTP 发送
- `committed`：进入实际发送边界前原子承诺，此后不可撤销
- 尚未 committed 的 reserved 可因预算不足回退（不消耗计数）
- 已 committed 的尝试无论结果如何均单调消耗

### 4.2 RequestBudget 最小可实施语义

```go
type RequestBudget struct {
    MaxRequests     int64         // 最大请求数（总发送尝试）
    MaxInputTokens  int64         // 输入 token 估算上限（预留）
    MaxOutputTokens int64         // 最大输出 token 上限（预留）
    MaxConcurrency  int           // 并发上限（在途 slot）
    Deadline        time.Duration // 请求级 deadline
}
```

- **设计决定**：RequestBudget 是“每次 HTTP 尝试前的原子预留”机制，不是精确费用上限。
- **不将 token 估算声称为精确费用上限**：输入 token 基于文本长度的粗略估算（字符数/系数），实际消耗以 Provider 返回的 usage 为准。**token 粗估明确标注为估算，不冒充精确金额。**

### 4.3 预算作用域（三层 owner 结构）

> **设计决定**：引入三层 owner 结构，明确每层的生命周期和职责。

| 层级 | 结构 | Owner | 生命周期 | 说明 |
|---|---|---|---|---|
| **进程级** | `ProcessBudget` | 进程 owner | 进程启动→关闭 | Registry 构造时绑定，不可重置；不能通过新建 Registry 重置全局额度 |
| **请求/任务级** | `RequestBudget` / `TaskBudget` | 请求或任务 | 单次 Retrieve/Index/ChatStream 调用→完成 | 独立结构，绑定 ProcessBudget；累计上限受 ProcessBudget 约束 |
| **尝试级** | `AttemptLease` | 单次 HTTP 尝试 | Reserve→Commit→Finish/Abort | 每次 HTTP 尝试的短期句柄；持有 slot + token 预留引用 |

**请求级 owner 覆盖规则**：
- `RequestBudget` 覆盖整个 `Retrieve()`/`Index()` 调用，在 controller/indexer 入口构造一次
- ChatStream 各阶段（Retrieve + Rewrite + Rerank + 最终 Generate）共享同一 request owner
- Index 同步与后台共享同一 task owner（`TaskBudget`），通过**显式移交**而非 ctx 继承
- 每个 `RequestBudget`/`TaskBudget` 内部维护独立累计，同时向 `ProcessBudget` 原子预扣

**进程预算不可重置**：`ProcessBudget` 由进程 owner 持有，不能通过新建 Registry 重置全局额度。

### 4.4 原子预留规则（attempt lease/reservation 模式）

- 发送前在两个作用域（`RequestBudget`/`TaskBudget` + `ProcessBudget`）**整体原子预留**：
  1. 占在途并发 slot（`atomic.AddInt64(&inFlight, 1)`）
  2. 预留 token 额度（输入估算 + 最大输出）
  3. 总发送尝试计数原子预留 1 次（reserved 状态）
- **任一不足都返回 `ErrBudgetExhausted`，不进入 sender**：已成功的部分立即回退（释放 slot、释放 token 预留、释放尝试计数预留）
- **reserved → committed 状态机**：进入实际发送边界前原子承诺（`AttemptLease.Commit()`），此后不可撤销
- **重试也计入预算**：每次重试预留新的 slot + token + 尝试计数
- 返回 `*AttemptLease` 作为单次 HTTP 尝试的短期句柄，持有 slot、token、尝试计数的预留引用

### 4.5 预留退款/结算规则

| 场景 | 处理 |
|---|---|
| **尚未发送的失败**（reserved 未 commit） | 可撤销尝试预留：释放 slot + 预留 token + 释放尝试计数预留，不消耗计数 |
| **发送后网络失败**（已 commit） | 总发送尝试计数已消耗（不退款）；保守保留输入+输出预留（因为无法确认 Provider 是否已处理）；幂等 finalize 释放 slot |
| **usage 缺失** | 记录 `unknown`，**不把 unknown 当 0**；保守保留预留（输入+最大输出）；幂等 finalize 释放 slot |
| **流中断** | 总发送尝试计数已消耗；已预留的 token 不退款；**幂等 finalize 释放 slot** |
| **正常完成** | 按 usage 实际值结算；幂等 finalize 释放 slot；预留与实际差额释放回预留账本 |

**slot 与 token 结算解耦**：
- slot 释放由幂等 finalize 触发，与 token 结算独立
- token 结算可在 slot 释放之后异步完成（如 usage 解析）
- 不能保留 token 就连 slot 一并泄漏

### 4.6 一次性结算（Finish/Abort/Close 幂等保护）

- **设计决定**：采用每 attempt lease/reservation 模式，明确 Finish/Abort/Close 一次性结算：
  - 幂等 finalize 释放 slot（`sync.Once` 或等价原子保护）
  - 按 usage 结算 token（或保守保留）
  - 记录实际消耗到结算账本
- **幂等保护**：Finish/Abort/Close 分别对 slot 与 token 结算做幂等保护：
  - 重复调用 Finish/Abort/Close 不重复退款 token
  - 重复调用不重复释放 slot
  - Close 与 EOF 并发时只 finalize 一次
- **slot 与 token 解耦**：不能保留 token 就连 slot 一并泄漏；即使 token 结算未确定（如 usage 缺失），slot 仍必须释放
- 结算必须在 attempt 结束后执行一次，不分散执行。

### 4.7 并发 slot 释放时机

| 场景 | slot 释放时机 |
|---|---|
| **stream** | 四种路径（EOF、终止性读错、取消后关闭、提前 Close）共用幂等 finalize，任一路径 finalize 一次即释放 |
| **非流式** | body 消费完成并关闭（或读错/取消后关闭） |

**幂等 finalize 语义**：
- 无论哪条路径触发，finalize 只执行一次（`sync.Once` 或等价原子保护）
- Close 与 EOF 并发时只 finalize 一次
- 提前 Close（如取消后主动关闭）也触发 finalize
- 终止性读错（如网络中断、解析失败）也触发 finalize
- 删除“EOF + Close”的逻辑与表述：不再要求“先 EOF 再 Close”才释放

### 4.8 自动重试

- **设计决定**：RequestBudget 是自动重试的唯一所有者；底层 HTTP transport 不做独立重试。
- **重试条件**：网络超时、5xx 错误；不重试 4xx 错误。
- **重试上限**：由 RequestBudget 的 `MaxRequests` 控制，不硬编码重试次数。
- **重试约束**：有绝对 deadline + 有限尝试数 + 退避；**已向用户输出的 stream 不自动重放**。

### 4.9 Index 后台接管预算的方式

- **设计决定**：Index 同步 + 后台共享同一 `TaskBudget` owner：
  - 后台 goroutine 在同步阶段结束时接收 task owner 引用（通过显式移交 `TaskBudget.TransferToBackground()`，而非 ctx 继承）
  - 保留 Stage 1 后台生命周期与 HTTP 取消不迁移的现有语义
  - Index 同步 + 后台的累计上限受同一 task owner 约束
  - 后台任务的每个 HTTP 尝试前预留 slot + token + 尝试计数，完成后结算
  - 后台任务取消时，未发送的预留释放；已发送的保守保留
- **显式移交语义**：
  - `TaskBudget.TransferToBackground()` 返回新句柄，后台持有该句柄继续操作
  - 同步阶段返回后，同步侧不再持有该 task owner 的活跃引用
  - 进程级 `ProcessBudget` 仍约束后台任务的累计消耗

### 4.10 禁止静默切换

- **设计决定**：预算耗尽、Provider 不可用、模型错误等场景下，不得静默切换到其他模型/Provider/计费方式。
- 错误必须向上传播，由调用方决定是否降级。

### 4.11 无法精确控制金额时的说明

- 当 Provider 的 usage 返回不包含 token 信息（如某些代理/中转服务），无法精确计算费用。此时如实说明“费用估算不可用”，不编造数字。

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

> **已确认**：以下列出所有维度硬编码位置，Stage 2 必须全部改为从 Embedding 配置/Provider 声明读取。

| 向量存储 | 写死维度位置 | 当前值 |
|---|---|---|
| **ES mapping** | `config/helpers.go:40`（`FieldContentVector`） | `Dims: Of(1024)` |
| **ES mapping** | `config/helpers.go:45`（`FieldQAContentVector`） | `Dims: Of(1024)` |
| **Qdrant mapping** | `config/helpers.go:57`（`FieldContentVector`） | `Size: 1024` |
| **Qdrant mapping** | `config/helpers.go:61`（`FieldQAContentVector`） | `Size: 1024` |
| **Qdrant indexer** | `indexer/indexer.go:73` | `VectorDim: 1024` |
| **Qdrant indexer (async)** | `indexer/indexer_async.go:52` | `VectorDim: 1024` |

- 六处均需改为从 Embedding 配置/Provider 声明读取维度。

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

### 7.1 批次 2A：角色接口 + 共享预算核心 + 生产共享链

**目标**：定义不可变 ProviderProfile/角色和最小 capabilities；实现可编译的 Stream/Embedding 适配（锁定 Eino v0.6.0）；实现实际会被 2B 使用的共享预算执行核心；提供一个明确构造入口及现有模型→Role/Eino adapter 的生产共享链。

**修改文件**：
- `server/core/providers/roles.go`（新增）—— ChatRole/EmbedRole/RerankRole 接口 + Capabilities struct
- `server/core/providers/profile.go`（新增）—— ProviderProfile 不可变结构
- `server/core/providers/registry.go`（新增）—— ProviderRegistry 接口 + 默认实现
- `server/core/providers/budget.go`（新增）—— ProcessBudget + RequestBudget + attempt lease/reservation
- `server/core/providers/transport.go`（新增）—— 统一 HTTP transport（共享 `*http.Client`，可注入）
- `server/core/providers/adapter.go`（新增）—— 现有模型→Role/Eino adapter 生产共享链
- `server/core/providers/fake.go`（新增）—— 全角色 fake 实现（离线测试用）
- `server/core/providers/roles_test.go`（新增）
- `server/core/providers/budget_test.go`（新增）
- `server/core/providers/adapter_test.go`（新增）

**接口签名草案**（锁定 Eino v0.6.0）：
```go
// roles.go
type ChatRole interface {
    Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error)
    Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

type EmbedRole interface {
    EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
    Dimensions() int
    ModelID() string
}

type RerankRole interface {
    Rerank(ctx context.Context, query string, docs []*schema.Document, topK int) ([]*schema.Document, error)
}

type Capabilities struct {
    Stream           bool
    StructuredOutput bool
    ToolCalling      bool
    Usage            bool
}

// profile.go
type ProviderProfile struct {
    ProviderName       string
    Protocol           string
    Endpoint           string
    Region             string
    Model              string
    Version            string
    Dimensions         int
    KeyOptions         map[string]string
    CredentialIdentity string
    Capabilities       Capabilities
}

// registry.go
type ProviderRegistry interface {
    GetChat(ctx context.Context) (ChatRole, error)
    GetRewrite(ctx context.Context) (ChatRole, error)
    GetQA(ctx context.Context) (ChatRole, error)
    GetExtract(ctx context.Context) (ChatRole, error)
    GetEmbedder(ctx context.Context) (EmbedRole, error)
    GetReranker(ctx context.Context) (RerankRole, error)
}

// budget.go
type ProcessBudget struct { ... }  // 进程级 owner，Registry 构造时绑定，不可重置
func NewProcessBudget(maxRequests, maxInputTokens, maxOutputTokens int64, maxConcurrency int) *ProcessBudget

type RequestBudget struct { ... }  // 请求/任务级累计 owner，绑定 ProcessBudget
func (pb *ProcessBudget) NewRequestBudget(ctx context.Context, opts ...BudgetOption) (*RequestBudget, error)
func (rb *RequestBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error)  // 返回 AttemptLease

type TaskBudget struct { ... }  // Index 同步+后台共享
func (pb *ProcessBudget) NewTaskBudget(ctx context.Context, opts ...BudgetOption) (*TaskBudget, error)
func (tb *TaskBudget) Reserve(estimate *TokenEstimate) (*AttemptLease, error)
func (tb *TaskBudget) TransferToBackground() (*TaskBudget, error)  // 显式移交

type AttemptLease struct { ... }  // 每次 HTTP 尝试的短期句柄
func (al *AttemptLease) Commit() error  // reserved → committed
func (al *AttemptLease) Finish(usage *model.TokenUsage)  // 一次性结算（幂等）
func (al *AttemptLease) Abort(err error)  // 取消（幂等）
func (al *AttemptLease) Close()  // 等价 Abort（幂等）

type TokenEstimate struct {
    InputTokens  int64  // 输入 token 估算
    MaxOutput    int64  // 最大输出 token
}
```

**2A 首批必须包含**：
1. 不可变 ProviderProfile/角色和最小 capabilities，使用锁定 Eino 的可编译 Stream/Embedding 适配
2. 实际会被 2B 使用的共享预算执行核心（request/task owner、process owner、attempt lease、deadline/并发管理、一次性结算）
3. 一个明确构造入口及现有模型→Role/Eino adapter 的生产共享链
4. default 构造只发布完整初始化对象；模型缓存 per Profile 隔离

**依赖**：无新外部依赖。

**验收测试**：
- fake 覆盖所有角色（Chat/Embed/Rerank/Rewrite/QA/Extract）
- 预算并发争用（atomic 预留 + race）
- 预算不足返回 `ErrBudgetExhausted`
- 取消传播
- attempt lease/reservation 生命周期（占 slot→commit→发送→结算→释放 slot）
- usage 缺失时保守保留预留
- adapter 生产共享链：现有 `openai.NewChatModel` → ChatRole 适配
- `MaxRequests=1` 并发实际发送计数为 1
- 请求足够/进程不足或反之均零发送且完全回退
- 重试/子步骤/后台接管不刷新累计额度
- 发送后网络失败不返还尝试计数
- 重复 Finish/Abort/Close 不重复退款
- httptest 阻塞 body 时下个请求不越过并发上限
- 非流式读失败/stream 提前 Close/取消/错误均最终释放 slot
- Close 与 EOF 并发只 finalize 一次
- unknown usage 不退款 token 但下一请求能用释放的 slot

**回滚边界**：2A 仅新增文件 + 兼容层，不改变现有调用方行为。回滚 = 删除 `providers/` 目录。

### 7.2 批次 2B：百炼适配 + 错误映射 + 默认工厂与业务接线

**目标**：实现百炼 Provider 的 Chat/Embedding/Rerank 适配；错误映射；预算集成；**承接默认工厂和业务接线**（不能只列 `bailian/` 子目录）。

**修改文件**：
- `server/core/providers/bailian/chat.go`（新增）
- `server/core/providers/bailian/embedding.go`（新增）
- `server/core/providers/bailian/rerank.go`（新增）
- `server/core/providers/bailian/chat_test.go`（新增，httptest）
- `server/core/providers/bailian/embedding_test.go`（新增，httptest）
- `server/core/providers/bailian/rerank_test.go`（新增，httptest）
- `server/core/config/config.go`（修改：Provider 配置块）
- `server/core/common/chat_model.go`（修改：兼容层委派 Registry）
- `server/core/common/embedding.go`（修改：兼容层委派 Registry）
- `server/core/rag.go`（修改：New() 从 ProviderRegistry 获取模型）
- `server/internal/logic/rag/retriever.go`（修改：readRagConfig 读取 Provider 配置块）

**2B 必须承接**：
- 默认工厂（`chat.go:45-50`、`indexer.go:19`、`indexer_async.go:18`、`retriever.go:25/76/261`、`rag.go:106`）的业务接线
- 明确百炼各协议的 encoder/decoder，以及复用既有 Rerank 校验链的方式
- 新 Embedding profile 的生产启用

**依赖**：复用现有 eino-ext openai/qwen 组件；不新增外部依赖。

**验收测试**：
- httptest 模拟百炼 endpoint：正常响应、错误码、畸形 JSON、usage 缺失、流中断
- 重试计费（重试计入预算）
- 取消/超时传播
- Rerank 适配保留已验收校验链
- Embedding index、数量、维度、有限值及 usage presence 必须在原始响应边界验证
- 业务接线验证：兼容层委派 Registry 后，现有调用方行为不变

**回滚边界**：2B 新增 `bailian/` 子目录 + 兼容层修改，不改现有 `rerank/rerank.go`。回滚 = 删除 `bailian/` + 回退兼容层。

### 7.3 批次 2C：维度/索引 fingerprint

**目标**：补齐真实索引创建入口、所有维度位置、fingerprint 存取及旧索引策略；维度不匹配时拒绝操作。

**2C 必须承接**：
- 补齐真实索引创建入口（`config/helpers.go:40/45/57/61`、`indexer/indexer.go:73`、`indexer/indexer_async.go:52`）
- fingerprint 存取及旧索引策略
- **同维度不等于同模型**：维度匹配不代表模型相同，fingerprint 必须包含模型身份
- guard 的依赖（维度不匹配阻断写入/检索）

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
- 2A 先落实可复用的生产共享预算/适配核心：不必实现百炼，但不能只给空接口和 fake 自测。

### 7.6 推荐首批

**推荐首批为 2A**：角色接口 + 共享预算核心 + 生产共享链。理由：
- 为后续批次提供接口基础 + 预算基础 + 适配基础
- 完全可离线验证
- 回滚成本最低

### 7.7 后续批次落实项

#### 2B 批次落实项

- Embedding index、数量、维度、有限值及 usage presence 必须在原始响应边界验证
- 明确百炼各协议的 encoder/decoder，以及复用既有 Rerank 校验链的方式
- 新 Embedding profile 的生产启用

#### 2C 批次落实项

- 补齐真实索引创建入口、所有维度位置、fingerprint 存取及旧索引策略
- 同维度不等于同模型
- guard 的依赖

#### 在线未知项

- 继续留给授权探针；A-4、向量库启动验证与付费模型探针分开安排

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

| 批次 | 验收条件 | 回滚边界 | 状态 |
|---|---|---|---|
| **2A** | 不可变 ProviderProfile/角色+capabilities；锁定 Eino 的可编译 Stream/Embedding 适配；共享预算执行核心（attempt lease、deadline/并发、一次性结算）；生产共享链；fake 覆盖所有角色；预算并发争用 race 通过；预算不足返回错误；取消传播；usage 缺失保守预留；`MaxRequests=1` 并发实际发送计数为 1；请求足够/进程不足或反之均零发送且完全回退；重试/子步骤/后台接管不刷新累计额度；发送后网络失败不返还尝试计数；重复 Finish/Abort/Close 不重复退款；httptest 阻塞 body 时下个请求不越过并发上限；非流式读失败/stream 提前 Close/取消/错误均最终释放 slot；Close 与 EOF 并发只 finalize 一次；unknown usage 不退款 token 但下一请求能用释放的 slot；预留阶段溢出拦截；resolver 隔离内部 Profile；`go test -race ./core/providers/...` exit 0 | 删除 `providers/` 目录 | **2A 剩余阻塞修复完成（2026-10-19）** |
| **2B** | httptest 模拟百炼正常/错误/畸形/流中断/usage 缺失均通过；重试计入预算；Rerank 保留已验收校验链；Embedding 响应边界验证；承接默认工厂和业务接线；兼容层委派 Registry；预算入口 owner 复用与重定向计 attempts；流式 leaseStreamReader 幂等终结；Reset 失败保留 Registry；cfg=nil 默认路径 vs 显式配置；OpenAI 生产 builder；Send 语义修正 + 输出预留同源 + Embedding usage 隔离；`go test -race ./core/providers/bailian/...` exit 0 | 删除 `bailian/` + 回退兼容层 | **2B 阻塞修复完成（2026-10-20 第三轮）** |
| **2C** | 维度 fingerprint 匹配/不匹配逻辑正确；ES/Qdrant CreateIndex 使用配置维度；维度不匹配阻断写入/检索；同维度不等于同模型 | `git revert` 2C 提交 | |
| **2D** | 探针在授权预算内执行；所有候选模型最小调用通过；脱敏证据完整；A-4 QA 向量行为记录 | 不影响离线代码（探针为只读验证） | |

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
| QA 模型有 `qaModelCache`（`sync.Mutex` + `ready bool` + 实例级缓存） | 源码 `chat_model.go:139-178` | 2026-10-19 |
| ES/Qdrant 硬编码 1024 维 | 源码 `config/helpers.go:40/45/57/61`、`indexer/indexer.go:73`、`indexer/indexer_async.go:52` | 2026-10-19 |
| Rerank 已有完整校验链（Stage 1B-1） | 源码 `rerank/rerank.go` | 2026-10-19 |
| 当前使用 SiliconFlow 作为 baseURL 代理 | `config_demo.yaml` | 2026-10-19 |
| Eino v0.6.0，GoFrame v2.9.0，ES client 8.16.0 | `go.mod` | 2026-10-19 |
| Eino Stream 返回 `*schema.StreamReader[*schema.Message]`（泛型 struct 指针） | 源码 `stream.go:110`、`retriever_test.go:55` | 2026-10-19 |
| Eino Embedding 使用 `EmbedStrings` 方法 | 源码 `retriever/qdrant_retriever.go:57`、`indexer/qdrant_indexer_custom.go:44` | 2026-10-19 |
| Chat factory 直接 `openai.NewChatModel` | 源码 `internal/logic/chat/chat.go:45-50` | 2026-10-19 |
| Embedding 三处分别 `common.NewEmbedding` | 源码 `indexer/indexer.go:19`、`indexer_async.go:18`、`retriever/retriever.go:25` | 2026-10-19 |
| Rewrite 用 `common.GetRewriteModel` | 源码 `retriever/retriever.go:76` | 2026-10-19 |
| Rerank 用 `rerank.NewRerank(...,nil)` | 源码 `retriever/retriever.go:261` | 2026-10-19 |
| QA 预热用 `common.GetQAModel` | 源码 `rag.go:106` | 2026-10-19 |

### 10.2 设计决定

| 决定 | 理由 |
|---|---|
| Chat 和 Extract 共享配置，独立角色接口 | 当前无 Extract 入口，预留扩展 |
| 不一次性删除包级变量，通过兼容层渐进迁移 | 避免单次改动过大 |
| RequestBudget 是自动重试的唯一所有者 | 避免底层 transport 独立重试导致预算失控 |
| 维度 fingerprint 记录在索引创建时 | 确保写入/检索与索引一致 |
| 不假设 JSON Schema/tool calling 兼容 | 百炼文档提及支持但未经探针验证 |
| 统一 transport 不等于同一协议 | Chat/Embedding 走 OpenAI 兼容，Rerank 可能走 DashScope 原生 |
| Stream 返回 `*schema.StreamReader[*schema.Message]`，不是 `model.StreamReader` | Eino v0.6.0 实际类型 |
| EmbedRole 采用 `EmbedStrings` 与 Eino 对齐 | 不拆分 EmbedDocuments/EmbedQuery |
| Extract 先作为 ChatRole 的角色标识（alias） | 与 Chat 共享配置，独立角色接口 |
| 四种计数器独立生命周期 | 总发送尝试/在途 slot/token 预留/实际结算 |
| 总发送尝试计数发送前原子预留（reserved → committed） | 避免发送后才发现超额；未 commit 可回退 |
| 三层 owner 结构（Process/Request-Task/AttemptLease） | 明确各层生命周期与职责；进程级不可重置；请求/任务级独立累计 |
| Index 同步+后台共享 TaskBudget，显式移交 | 不依赖 ctx 继承；保留 Stage 1 后台生命周期语义 |
| 幂等 finalize 释放 slot（四路径共用） | 避免 EOF+Close 耦合；重复调用安全；slot 与 token 结算解耦 |
| slot 与 token 结算解耦 | 不能保留 token 就泄漏 slot；usage 缺失不影响 slot 释放 |
| 仅“尚未发送的失败”可撤销尝试预留 | 发送后网络失败保守保留预留 |
| 进程预算由进程 owner 持有，不能通过新建 Registry 重置 | 全局额度不可重置 |
| 已向用户输出的 stream 不自动重放 | 重试不重放已输出内容 |
| 同维度不等于同模型 | fingerprint 必须包含模型身份 |

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
