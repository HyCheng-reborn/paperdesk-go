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

指纹存储在 index mapping 的 `_meta.fingerprint` 字段：

```json
{
  "mappings": {
    "_meta": {
      "fingerprint": {
        "version": "v1",
        "embedding_model_id": "text-embedding-v4",
        "dimensions": 1024,
        "provider_name": "bailian",
        "similarity": "cosine",
        "digest": "sha256:..."
      }
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

### Qdrant

指纹存储在 SQL 表 `index_fingerprints`：

```sql
CREATE TABLE index_fingerprints (
    index_name VARCHAR(255) PRIMARY KEY,
    fingerprint JSON NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

**API**：
- `INSERT ... ON DUPLICATE KEY UPDATE` - 写入指纹
- `SELECT ... WHERE index_name = ?` - 读取指纹

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

指纹缓存使用 `fingerprintCache` 结构：

```go
type fingerprintCache struct {
    mu    sync.RWMutex
    items map[string]*vector.Fingerprint
}
```

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

2C-2 实施完成，待独立复核。
