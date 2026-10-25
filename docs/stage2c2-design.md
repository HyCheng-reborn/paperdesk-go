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
- 缓存条目绑定 resourceID，过期后重验证
- resourceID 不匹配 = 资源被替换 = fail closed

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
- `PUT /<index>/_mapping` - 写入指纹
- `GET /<index>/_mapping` - 读取指纹
- `GET /<index>` - 获取索引 UUID（resourceID）

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
2. 检查索引是否存在
3. 读取存储的指纹
4. 如果存储指纹缺失：写入期望指纹并回读验证
5. 如果存储指纹存在：与期望指纹比较
6. 验证通过：缓存指纹
7. 验证失败：返回错误，拒绝启动

### 运行时验证（checkFingerprint）

1. 检查指纹基础设施是否初始化
2. 检查缓存中的指纹
3. 缓存命中且有效：返回成功
4. 缓存未命中：从存储读取并验证
5. 验证通过：更新缓存
6. 验证失败：返回错误，拒绝操作

## 错误码

| 错误码 | 含义 | 触发条件 |
|--------|------|----------|
| `FINGERPRINT_MISSING` | 指纹缺失 | 存储中无指纹记录 |
| `FINGERPRINT_MISMATCH` | 指纹不匹配 | 存储指纹与期望指纹不一致 |
| `FINGERPRINT_CORRUPTED` | 指纹损坏 | 指纹数据无法解析或格式无效 |

## 验证门控位置

### 检索路径（Retrieve）

```
Retrieve() → checkFingerprint() → embedding → vector search
```

**文件**：`server/core/retriever.go`

### 索引路径（Index）

```
Index() → checkFingerprint() → sync indexing → async embedding
```

**文件**：`server/core/indexer.go`

### 异步索引路径（IndexAsync）

```
IndexAsync() → checkFingerprint() → embedding → vector store
```

**文件**：`server/core/indexer.go`

## 缓存机制

### 5.2 Fingerprint 缓存（TTL + UUID 重验证）

- 内存缓存条目：`{fingerprint, resourceID, verifiedAt}`
- TTL = 30 秒
- 缓存未过期：快速通过
- 缓存过期：从存储重读 fingerprint + resourceID
  - resourceID 不匹配 → ErrResourceReplaced（资源被替换）
  - fingerprint 不匹配 → ErrFingerprintMismatch
  - digest 验证失败 → ErrFingerprintCorrupted
- 条件写入：WriteFingerprint 使用先读后写语义，已有记录返回 ErrFingerprintExists

**缓存键格式**：`<vector_store_type>:<index_name>`
- 示例：`es:rag-index`、`qdrant:rag-index`

**并发安全**：使用 `sync.RWMutex` 保护

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
   - 缓存命中场景
   - 缓存未命中场景
   - 指纹不匹配场景
   - 指纹缺失场景
   - 基础设施未初始化场景
   - Retrieve/Index/IndexAsync 门控行为

### 集成测试

现有测试已更新以支持指纹基础设施：
- `indexer_test.go`：所有 Index 相关测试
- `retriever_test.go`：所有 Retrieve 相关测试

## 约束与限制

1. **不兼容旧索引**：没有指纹的旧索引在首次验证时会被写入期望指纹
2. **模型变更需重建索引**：更换 embedding 模型后，必须重建索引
3. **维度必须一致**：即使模型相同，维度不一致也会导致验证失败
4. **Provider 名称大小写不敏感**：存储时统一转换为小写

## 未来改进

1. **指纹版本迁移**：支持指纹格式升级
2. **多模型支持**：允许同一索引使用多个 embedding 模型
3. **指纹备份**：支持指纹导出/导入
4. **渐进式迁移**：支持索引重建期间的双写模式

## 状态

2C-2 R1-R7 修复完成，待第三轮独立复核。

## 附录：独立复核阻塞修复记录（F1-F8）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| F1 | 创建与身份发布分离 | 创建时原子写入 fingerprint + resourceID；回读验证 schema 维度 |
| F2 | Qdrant 身份仅绑定名称 | 新增 resource_id 列绑定物理资源 UUID；Ready 标志标记完成状态 |
| F3 | 指纹可覆盖 | 条件写入：已有记录返回 ErrFingerprintExists，不执行更新 |
| F4 | 永久缓存跳过验证 | 30s TTL + resourceID 重验证；resourceID 变化 = 资源替换 |
| F5 | 启动隐式 AutoMigrate | 替换为 CheckFingerprintTableReady()，只检查不修改 |
| F6 | varchar(64) 不足 | 扩展为 varchar(128)，容纳 sha256: 前缀 + 64 hex |
| F7 | 容忍损坏数据 | 严格 JSON 解析（UseNumber 拒绝浮点维度）；VerifyDigest 重算核验 |
| F8 | ES 请求忽略取消 | 入口 ctx.Err() 预检查；typed API 通过 .Do(ctx) 传播 |

## 附录：第二轮独立复核修复记录（R1-R7）

| 编号 | 问题 | 修复方式 |
|------|------|----------|
| R1 | Factory 层指纹写入缺失 | CreateIndex 后立即 WriteFingerprint；check existence → create → write → verify |
| R2 | ES resourceID 未绑定真实 UUID | ES ReadFingerprint 比对 _meta resourceID 与 settings.index.uuid；Qdrant ReadFingerprint 验证 collection 存在 |
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
| D1 | Factory 流程 SDK 隐式创建 | CreateIndex 先于 core.New 消除 SDK 预创建；Qdrant AlreadyExists 归一化为 ErrIndexAlreadyExists；db=nil fail-fast |
| D2 | ES 安全加固 | 移除 deleteIndexBestEffort 和 PutMapping 后补；UUID 只读自 settings.index.uuid；创建后不修改 _meta |
| D3 | 共享严格 schema 校验 | schema.go 统一校验器：必需字段检查；维度 json.Number 整数校验（拒绝浮点截断）；distance/similarity 映射 |
| D4 | 资源身份固定绑定 | boundResourceID 启动时记录；checkFingerprint 每次比对；不匹配返回 ErrResourceReplaced |

### D4 设计理由

**ES**：`settings.index.uuid` 是不可变的物理资源标识符。`ReadFingerprint` 每次从 GetIndex API 读取真实 UUID；`boundResourceID` 在启动时固定，运行时每次比对。若外部删除并重建同名索引，UUID 必然变化，立即触发 `ErrResourceReplaced`。

**Qdrant**：SQL 中随机生成的 UUID 无法证明 collection 的化身（incarnation）——外部删除并重建同名 collection 后 SQL 记录仍相同。当前最小契约是 SQL resourceID + Ready 标志 + collection 存在性检查；完整化身追踪（例如基于 Qdrant collection info 的 immutable ID）推迟到设计 §5.8。

状态：2C-2 D1-D4 修复完成，待第五轮独立复核。
