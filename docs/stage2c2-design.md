# Stage 2C-2: Index Fingerprint Persistence & Identity Validation

## 概述

Stage 2C-2 实现索引指纹持久化与身份验证，确保向量索引的 embedding 模型身份一致性。
当索引的 embedding 模型、维度、provider 或相似度度量发生变化时，系统会在启动和运行时拒绝操作，防止向量空间不匹配导致的数据损坏。

## 设计目标

1. **指纹类型**：定义 `Fingerprint` 结构，包含版本、模型 ID、维度、provider 名称和相似度度量
2. **持久化存储**：
   - ES：存储在 index mapping 的 `_meta` 字段
   - Qdrant：存储在 SQL 表 `index_fingerprints`
3. **启动验证**：Rag 初始化时验证指纹一致性
4. **运行时验证**：每次 embedding 操作前验证指纹
5. **失败关闭**：指纹缺失/不匹配/损坏时拒绝操作

## 指纹格式

```go
type Fingerprint struct {
    Version          string `json:"version"`           // 格式版本，当前为 "v1"
    EmbeddingModelID string `json:"embedding_model_id"` // Embedding 模型 ID
    Dimensions       int    `json:"dimensions"`         // 向量维度
    ProviderName     string `json:"provider_name"`      // Provider 名称（小写）
    Similarity       string `json:"similarity"`         // 相似度度量（小写，如 "cosine"）
}
```

### 1.5 ResourceID（物理资源身份）

ResourceID 是不可复用的物理资源标识符，与 Fingerprint（向量空间身份）分离存储：

- ES: 索引 UUID（从 GetIndex API 获取），存储在 `_meta.go-rag-resource-id`
- Qdrant: 创建时生成的 UUID v4，存储在 SQL `resource_id` 列

用途：
- 检测同名资源替换（外部 delete+recreate 导致 UUID 变化）
- 入口校验与操作后检测都比对 resourceID（无进程内缓存，每次从存储重读）
- resourceID 不匹配 = 资源被替换 = fail closed（入口拒绝本次操作；操作后检测返回错误，但已发生的读写副作用不可撤销）

### 规范化 JSON

指纹的规范化 JSON 格式用于计算 SHA-256 摘要：
- 键按字母顺序排序
- 无空白字符
- 示例：`{"dimensions":1024,"embedding_model_id":"text-embedding-v4","provider_name":"bailian","similarity":"cosine","version":"v1"}`

### 摘要计算

```
digest = "sha256:" + hex(SHA-256(canonical_json))
```

## 存储实现

### Elasticsearch

指纹存储在 index mapping 的 `_meta` 字段：

```json
{
  "mappings": {
    "_meta": {
      "go-rag-fingerprint": {
        "version": "v1",
        "embedding_model_id": "text-embedding-v4",
        "dimensions": 1024,
        "provider_name": "bailian",
        "similarity": "cosine",
        "digest": "sha256:abcdef..."
      },
      "go-rag-resource-id": "es-index-uuid-xxxxx"
    },
    "properties": {
      ...
    }
  }
}
```

**API**：
- `PUT /<index>`（CreateIndex）— 创建时 `mappings._meta` 原子携带 fingerprint；创建成功后不再补写 `_meta`，也不做自动删除
- `PUT /<index>/_mapping` — 仅 `WriteFingerprint` 条件写入路径使用（先读 `_meta`，已存在返回 `ErrFingerprintExists`）；当前生产创建/验证路径不调用该方法（调用点仅见测试）
- `GET /<index>` — 读取 mapping `_meta`，并从 `settings.index.uuid` 只读取 resourceID

### Qdrant

指纹存储在 SQL 表 `index_fingerprints`：

```sql
CREATE TABLE index_fingerprints (
  id               BIGINT AUTO_INCREMENT PRIMARY KEY,
  vector_store_type VARCHAR(16) NOT NULL,
  index_name       VARCHAR(255) NOT NULL,
  resource_id      VARCHAR(36) NOT NULL,         -- 物理资源 UUID
  fingerprint_json TEXT NOT NULL,
  fingerprint_digest VARCHAR(128) NOT NULL,       -- sha256: 前缀 + 64 hex
  embedding_model_id VARCHAR(255) NOT NULL,
  dimensions       INT NOT NULL,
  provider_name    VARCHAR(64) NOT NULL,
  similarity       VARCHAR(16) NOT NULL,
  ready            TINYINT NOT NULL DEFAULT 0,    -- 身份验证完成标志
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_store_index (vector_store_type, index_name)
);
```

**API**：
- `WriteFingerprint` - 条件写入：已有记录返回 `ErrFingerprintExists`，不执行更新
- `ReadFingerprint` - 读取指纹 + resourceID
- `CheckFingerprintTableReady` - 只检查表是否存在且可用，不修改 schema

## 验证流程

### 启动验证（VerifyFingerprint）

1. 从配置计算期望指纹
2. 读取存储的指纹、resourceID 与实际 schema（ES/Qdrant 都回读）
3. 存储指纹缺失：**不回填**，返回 `ErrFingerprintMissing`（fail closed）
4. 存储指纹存在：校验 digest、与期望指纹比较、执行严格 schema 校验
5. 验证通过：固定 `boundResourceID`（启动时设置，运行期只读）
6. 验证失败：返回错误，拒绝启动（不删除资源，无所有权证明）

创建时机（生产工厂 `retriever.go`）：单次存在性检查 → 受控 `CreateIndex`（携带 fingerprint）→ `core.New` → `VerifyFingerprint` → 发布 Registry。

### 运行时验证（checkFingerprint）

1. 检查指纹基础设施是否初始化（`vectorStore` 非 nil）
2. 每次从存储重读 fingerprint + resourceID（**无进程内缓存**）
3. 校验 digest、与期望指纹比较、执行严格 schema 校验
4. 比对 resourceID 与 `boundResourceID`，不一致返回 `ErrResourceReplaced`
5. 验证失败：返回错误，拒绝操作

## 错误码

| 错误码 | 含义 | 触发条件 |
|--------|------|----------|
| `FINGERPRINT_MISSING` | 指纹缺失 | 存储中无指纹记录 |
| `FINGERPRINT_MISMATCH` | 指纹不匹配 | 存储指纹与期望指纹不一致 |
| `FINGERPRINT_CORRUPTED` | 指纹损坏 | 指纹数据无法解析、digest 不匹配或 resourceID 为空 |
| `ErrResourceReplaced` | 资源已被替换 | 当前 resourceID 与启动时 `boundResourceID` 不一致（入口拒绝；操作后检测返回错误） |
| `ErrFingerprintExists` | 指纹已存在 | `WriteFingerprint` 条件写入遇到已有记录，不覆盖 |

## 验证门控位置

### 检索路径（Retrieve）

```
Retrieve() → checkFingerprint() → embedding → vector search → verifyBoundResource()
```

**文件**：`server/core/retriever.go`

### 索引路径（Index）

```
Index() → checkFingerprint() → sync indexing → verifyBoundResource() → async embedding
```

**文件**：`server/core/indexer.go`

### 异步索引路径（IndexAsync）

```
IndexAsync() → checkFingerprint() → embedding → vector store → verifyBoundResource()
```

**文件**：`server/core/indexer.go`

## 验证时机（无进程内缓存）

C5 已移除 F4 引入的 30 秒 TTL 缓存：`checkFingerprint` 每次调用都从存储重读 fingerprint、resourceID 与实际 schema，因此不存在“缓存未过期即放行”的窗口（F4 那套 TTL 描述仅保留在历史附录中，不代表现状）。

| 时机 | 源码位置 | 行为与影响 |
|---|---|---|
| 入口（Retrieve/Index/IndexAsync） | `rag.go` `checkFingerprint` | 重读 + digest/指纹/schema 校验 + resourceID 比对；失败则拒绝本次操作，不进入 embedding/存储 |
| 操作后（E3） | `rag.go` `verifyBoundResource`，调用点 `indexer.go:167`、`indexer.go:272`、`retriever.go:198` | 重读 resourceID 与 `boundResourceID` 比对；可检测**持续到复查时刻**的替换并返回错误，但**不能撤销已发生的写入，也不能阻止写入已被替换的资源** |

条件写入：`WriteFingerprint` 先读后写，已有记录返回 `ErrFingerprintExists`（保留在接口与实现中，当前生产创建路径不调用）。

## 测试覆盖

### 单元测试

1. **指纹类型测试**（`fingerprint_test.go`）：
   - 创建验证
   - 相等性比较
   - 有效性检查
   - 规范化 JSON
   - 摘要计算
   - 序列化/反序列化

2. **验证门控测试**（`fingerprint_gate_test.go`）：
   - 每次从存储重读（无缓存）场景
   - 指纹不匹配 / 缺失 / 损坏场景
   - 基础设施未初始化场景
   - Retrieve/Index/IndexAsync 门控行为
   - resourceID 与 boundResourceID 不一致（入口拒绝）

3. **本批（E1-E3）新增测试**（提交 `fce88e2`，共 26 个 `func Test`，按 diff 统计）：
   - `schema_validation_test.go`、`fingerprint_es_test.go`、`fingerprint_qdrant_test.go`：ES 缺失/非字符串 type、缺失/错误 similarity；Qdrant Manhattan、未知枚举 0/99、全部已知枚举映射
   - `qdrant_indexer_test.go`：`NewQdrantIndexer` 构造不发起网络调用
   - `fingerprint_gate_test.go`、`indexer_test.go`、`retriever_test.go`：`verifyBoundResource` 匹配/变更/空值/未绑定/nil store；Index 与 Retrieve 的操作后检测
   - **边界**：有测试不等于独立验收；本批没有独立执行记录，外部复现（overlay）只属于上一轮 79ea0ab 的缺陷复现，不构成本提交的契约通过。

### 集成测试

现有测试已更新以支持指纹基础设施：
- `indexer_test.go`：所有 Index 相关测试
- `retriever_test.go`：所有 Retrieve 相关测试

## 约束与限制

1. **不兼容旧索引**：没有指纹的旧索引不会被回填，验证直接失败（`ErrFingerprintMissing`），需按当前配置重建索引以建立身份
2. **模型变更需重建索引**：更换 embedding 模型后，必须重建索引
3. **维度必须一致**：即使模型相同，维度不一致也会导致验证失败
4. **Provider 名称大小写不敏感**：存储时统一转换为小写

## 未来改进

1. **指纹版本迁移**：支持指纹格式升级
2. **多模型支持**：允许同一索引使用多个 embedding 模型
3. **指纹备份**：支持指纹导出/导入
4. **渐进式迁移**：支持索引重建期间的双写模式

## 状态

- 固定目标提交 `fce88e2eb15dd07d7ad8cd3c5d49f528b0e0cb7b`（父提交 `79ea0ab`，原始基线 `2dc0b82`，分支 `codex/stage2c2-fingerprint`）。
- E1/E2：源码可确认的修复已落地（见附录 E1-E3），**无本批独立复核**，不构成验收通过。
- E3：仅为操作后检测（`verifyBoundResource`），原子目标绑定仍待决（Option B/C），不得写成闭环。
- 验收状态未变更：`ErrEmbeddingGuardNotReady` 仍开启；未进入 2C-3，未执行 2D 在线探针。
- 真实 ES/Qdrant/MySQL 与在线 embedding 未验证；Qdrant incarnation/跨集群方案仍待设计确认。

## 附录：独立复核阻塞修复记录（F1-F8）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| F1 | 创建与身份发布分离 | 创建时原子携带 fingerprint（ES `mappings._meta`）；Qdrant 创建前生成 resourceID 写入 SQL；回读验证 schema 维度 |
| F2 | Qdrant 身份仅绑定名称 | 新增 resource_id 列绑定物理资源 UUID；Ready 标志标记完成状态 |
| F3 | 指纹可覆盖 | 条件写入：已有记录返回 ErrFingerprintExists，不执行更新 |
| F4 | 永久缓存跳过验证 | 当时引入 30s TTL + resourceID 重验证；该 TTL 方案已在 C5 中整体移除，当前行为见正文“验证时机” |
| F5 | 启动隐式 AutoMigrate | 替换为 CheckFingerprintTableReady()，只检查不修改 |
| F6 | varchar(64) 不足 | 扩展为 varchar(128)，容纳 sha256: 前缀 + 64 hex |
| F7 | 容忍损坏数据 | 严格 JSON 解析（UseNumber 拒绝浮点维度）；VerifyDigest 重算核验 |
| F8 | ES 请求忽略取消 | 入口 ctx.Err() 预检查；typed API 通过 .Do(ctx) 传播 |

## 附录：第二轮独立复核修复记录（R1-R7）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| R1 | Factory 层指纹写入缺失 | 当时在 CreateIndex 后立即 WriteFingerprint；该独立写入已在 C1/C3 中移除，现由 CreateIndex 原子携带 fingerprint |
| R2 | ES resourceID 未绑定真实 UUID | 当时比对 `_meta` resourceID 与 settings.index.uuid；D2 后 resourceID 只读 `settings.index.uuid`，不再从 `_meta` 比对；Qdrant ReadFingerprint 验证 collection 存在 |
| R3 | 并发写入保护不足 | 条件写入语义：已存在返回 ErrFingerprintExists |
| R4 | 空 resourceID 未拒绝 | VerifyFingerprint + checkFingerprint 均 fail closed 拒绝空 resourceID |
| R5 | ParseFingerprint 未验证 digest | 重算摘要核验 + 拒绝尾随 JSON 数据 |
| R6 | ES API 未传播 context | 所有 ES API 调用通过 .WithContext(ctx) 传播取消信号 |
| R7 | Qdrant CreateIndex 失败自动删除 | 移除无所有权自动删除逻辑，失败时保留 collection |

状态：2C-2 C1-C6 修复完成，待第四轮独立复核。

## 附录：第三轮独立复核修复记录（C1-C6）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| C1 | 受控创建流程不统一 | factory 单次存在性检查，错误传播不丢弃；消除独立 WriteFingerprint 调用 |
| C2 | ES CreateIndex 非原子 | typed API mappings._meta 携带 fingerprint 原子写入；创建后真实 UUID 回读 |
| C3 | 独立 WriteFingerprint 生产路径 | 消除独立写入；并发创建走 ES resource_already_exists → 验证路径 |
| C4 | schema 维度未校验 | ES 和 Qdrant 回读实际维度与 fingerprint.Dimensions 比对 |
| C5 | TTL 缓存延迟检测 | 移除 TTL 缓存；checkFingerprint 每次从存储重读，资源变更立即检测 |
| C6 | JSON 解析未严格 EOF | 第二次 Decode 必须返回 io.EOF，拒绝尾随 `}`/`]`/垃圾数据 |

状态：2C-2 C1-C6 修复完成，待第四轮独立复核。

## 附录：第四轮独立复核修复记录（D1-D4）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| D1 | Factory 流程 SDK 隐式创建 | CreateIndex 先于 core.New；Qdrant AlreadyExists 归一化为 ErrIndexAlreadyExists；db=nil fail-fast。**注意**：改顺序只解决正常空库启动，第五轮复核 E2 证实 core.New 构造图时仍触发 SDK 创建（本批 E2 才移除该构造） |
| D2 | ES 安全加固 | 移除 deleteIndexBestEffort 和 PutMapping 后补；UUID 只读自 settings.index.uuid；创建后不修改 _meta |
| D3 | 共享严格 schema 校验 | `schema_validation.go`（`ValidateSchemaStrict`）统一校验器：必需字段检查；维度 json.Number 整数校验（拒绝浮点截断）；distance/similarity 映射。第五轮复核发现空语义值仍放行，已由 E1 收口 |
| D4 | 资源身份固定绑定 | boundResourceID 启动时记录；checkFingerprint 每次比对；不匹配返回 ErrResourceReplaced |

### D4 设计理由

**ES**：`settings.index.uuid` 是不可变的物理资源标识符。`ReadFingerprint` 每次从 GetIndex API 读取真实 UUID；`boundResourceID` 在启动时固定，运行时每次比对。若外部删除并重建同名索引，UUID 必然变化，立即触发 `ErrResourceReplaced`。

**Qdrant**：SQL 中随机生成的 UUID 无法证明 collection 的化身（incarnation）——外部删除并重建同名 collection 后 SQL 记录仍相同。当前最小契约是 SQL resourceID + Ready 标志 + collection 存在性检查；完整化身追踪（例如基于 Qdrant collection info 的 immutable ID）推迟到设计 §5.8。

**适用边界（D4 未覆盖的部分）**：D4 的比对只能拒绝“两次入口之间”的资源替换。若替换发生在入口之后、embedding/写入完成之前，D4 无法阻止写入，需 E3 的操作后检测才能发现（且仍不可撤销），见附录 E3。

状态：D1-D4 修复于 79ea0ab，第五轮复核结果见附录 E1-E3（D2 闭环；D1/D3/D4 当时仍有缺口）。

## 附录 E1-E3：第五轮独立复核后的修复（基线 79ea0ab，提交 fce88e2）

> 本节只陈述源码可确认的行为与尚未独立验证的部分；括号内为源码位置。本批**无独立复核结论**，不应据此更新验收状态。

### E1：Schema 校验空值旁路

**第五轮复核在 79ea0ab 观察到的问题**（独立报告 E1/P1，外部 overlay 复现；复现 PASS 只表示“观察到缺陷”）：
- `ValidateSchemaStrict` 在 `Distance` 为空时跳过距离比较，`FieldType` 为空时跳过类型检查；
- ES 缺失/非字符串 `type`、`similarity` 经提取后变成空字符串；
- Qdrant 距离只覆盖 Cosine/Dot/Euclid，其余枚举变成空字符串，以致锁定 SDK 中合法的 `Distance_Manhattan=4` 以及未知枚举（0/99）被接受。

**本提交的源码行为**（`server/core/vector/schema_validation.go`）：
- `ValidateSchemaStrict(fp, fields, backend)` 新增 `backend` 参数：`"es"` 要求 `FieldType == "dense_vector"`；`"qdrant"` 忽略 `FieldType`（named vector 无类型字段）。
- `Distance` 必填：空字符串直接返回 `ErrFingerprintMismatch`，不再“无需验证”。
- `distanceMatchesSimilarity`：映射 ES 的 `cosine`/`dot_product`/`l2_norm` 与 Qdrant 的 `Cosine`/`Dot`/`Euclid`；`Manhattan` 不匹配任何指纹 similarity；未知距离返回 false。
- `mapQdrantDistance`（`qdrant.go`）：显式映射 Cosine/Dot/Euclid/Manhattan，未知枚举返回 error（不再降级为空串）。
- 维度走 `strictDims`：拒绝 float64/string/nil，`json.Number` 仅接受整数。

**边界（未独立验证）**：
- ES 提取路径（`es.go` schema 回读）在 `type` 缺失/非字符串、`similarity` 缺失或不在顶层时仍产出空值；当前是靠校验器拒绝空值实现 fail closed，而不是提取阶段就报错。ES 返回形态（例如 similarity 出现在其他位置）是否都被覆盖未经独立验证。
- 本批新增了对应测试，但“新增了测试”不等于独立验收；外部 overlay 复现属于上一轮 79ea0ab 的缺陷证据，不能反过来当成本提交的契约通过。

### E2：Qdrant 图构造中的 SDK 自动创建

**第五轮复核在 79ea0ab 观察到的问题**（独立报告 E2/P2）：受控 `CreateIndex` 先执行解决了正常空库启动，但 `core.New` 构造图时仍通过 eino SDK `NewIndexer` 触发 `ensureCollection`；若第一次存在性检查后 collection 消失，SDK 仍会创建 unnamed collection（随后被严格 schema 检查拒绝，未发布错误实例）。

**本提交的源码行为**（`server/core/indexer/qdrant_indexer.go`）：
- 删除 `einoIndexer` 字段与 `einoqdrant.NewIndexer` 构造调用；`Store` 直接走 `StoreWithNamedVectors`。
- 全仓已无 `einoqdrant` 引用；`QdrantIndexer.asyncIndexer` 字段仍保留，但当前无赋值/使用点。

**边界（未独立验证）**：
- “集合只由受控 `CreateIndex` 创建”是本仓源码审计结论，不是独立环境复验；第五轮复核针对的是 79ea0ab，本提交尚未复核。不得写成“SDK 隐式创建已完全消除”。
- Qdrant 侧仍无物理 incarnation 追踪（见 D4 设计理由），SQL `resource_id` 不能证明集合化身；存在后消失的真实 factory 回归仍需复核。

### E3：操作后资源检测（非原子绑定）

**问题（第五轮复核 E3/P1，79ea0ab）**：`IndexAsync` 只在入口 `checkFingerprint` 一次，后续 embedding 与 Store 使用可变逻辑名。独立测试在 embedding 返回前把同名目标换成 UUID B/模型 B，bulk 仍写入模型 A 的向量且 `IndexAsync` 返回成功——下一次入口才拒绝。

**本提交的源码行为**：
- `rag.go`：`verifyBoundResource` 读取当前 resourceID 并与 `boundResourceID`（`VerifyFingerprint` 时固定）比对，不一致返回 `ErrResourceReplaced`；`boundResourceID` 为空时跳过（测试兼容路径）。
- 调用点：`Index`（`indexer.go:167`，同步 Store 之后）、`IndexAsync`（`indexer.go:272`，embedding+Store 之后）、`Retrieve`（`retriever.go:198`，结果聚合之后）。

**能力与影响（必须区分）**：

| 场景 | 实际行为 |
|---|---|
| 两次入口之间被替换 | 入口 `checkFingerprint` 比对 `boundResourceID` 并拒绝，本次操作不进入 embedding/存储 |
| 入口之后、操作完成前被替换 | 操作后 `verifyBoundResource` 返回错误；但**写入已经发生且不会回滚**；检索侧只体现为“结果不返回给调用方”（读取本身也已发生） |
| 替换发生在复查之后 | 检测不到，等下一次入口再发现 |

因此 E3 的准确状态是“**增加了操作后检测，原子目标绑定仍待决**”，不是闭环：它不能阻止写入已被替换的资源，也不能撤销副作用。`Index/IndexAsync` 是“写入已发生 + 返回错误”，`Retrieve` 是“不返回结果”，两者影响不同，不得合并表述。

**待决设计（均未获批，不得写成既定要求）**：
- Option B：ES 条件写入（`if_seq_no` + `if_primary_term`）。仓库内**没有**证据表明该机制能绑定物理索引 UUID 或 Qdrant incarnation；它约束的是文档级并发，且 Qdrant 无等效机制。**需专项设计验证**。
- Option C：不可变物理名称 + 逻辑→物理映射表。涉及 DDL、迁移与生命周期约束，属**待批准**架构设计。


### E3 受控物理目标首版（用户已批准范围，实现待复核）

仅支持具体单一 ES 物理索引，拒绝 alias/表达式；新增默认 false 的 vector.managedResourceLifecycle 显式部署确认。工厂先绑定名称/UUID/指纹再构造图；embedding 返回后、BM25 与后台取文档等实际 I/O 前验证同一目标。Qdrant incarnation 未决，生产路径继续禁用。不新增表/DDL/自动切换/清理，不解除新 profile 门禁。最后一次检查与 I/O 仍非原子，依赖独占管理、名称永不复用、固定集群和禁止自动创建的部署前提，不能声称抵御任意外部删除重建。详见 stage2c2-e3-design.md。
