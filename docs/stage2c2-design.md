# Stage 2C-2 索引 Fingerprint 实施设计

## 0. 基线与范围

| 项目 | 值 |
|------|-----|
| 2C-1 最终验收 SHA | `2dc0b82`（`codex/stage2c1-dimensions` 分支尖端） |
| 验收报告 | `verification/independent-2c1-2dc0b82-20261021/REPORT.md` — PASS |
| 设计分支 | `docs/stage2c2-fingerprint-design`，从 `2dc0b82` 创建 |
| 交付物 | `docs/stage2c2-design.md` |
| 不含 | 编码实施、2C-3 guard 解除、2D 在线探针、真实 API 调用 |

## 1. 身份字段与比较规则

### 1.1 三层身份分离

| 层级 | 含义 | 字段 | 用途 |
|------|------|------|------|
| **向量空间身份** | 决定向量是否可比较 | `provider_name`, `embedding_model_id`, `dimensions`, `similarity` | fingerprint 核心 |
| **索引 schema 身份** | 结构兼容 | 向量字段名(`content_vector`, `qa_content_vector`)、每字段维度、每字段相似度 | mapping 校验 |
| **资源实例身份** | 物理资源定位 | `vector_store_type`(es/qdrant), `index_name` | 绑定知识库 |

### 1.2 Fingerprint 结构

```
Fingerprint {
  Version:          "v1"           // 格式版本
  EmbeddingModelID: "model" 或 "model@version"  // = ProviderProfile.ModelID()
  Dimensions:       int            // = ProviderProfile.Dimensions / EmbedRole.Dimensions()
  ProviderName:     string         // 归一化小写
  Similarity:       "cosine"       // 来自 vector.Config
}
```

### 1.3 参与/不参与兼容判断的字段

| 参与 | 不参与 |
|------|--------|
| provider_name（不同供应商 = 不同向量空间） | profile name（仅路由） |
| embedding_model_id（含版本） | API key / credentials |
| dimensions | endpoint URL |
| similarity | region |

### 1.4 比较规则

- **全字段精确匹配**，任何字段不同 = 不兼容
- `Version` 不识别 → fail closed（本阶段无向前/向后兼容）
- 任何字段为空/缺失 → fail closed
- 归一化：provider_name 小写、similarity 小写、model_id 保留 provider 规范形式、dimensions 为整数
- 摘要算法：SHA-256(canonical JSON, 排序 key, 无空白) → hex string
- **明确声明**：fingerprint 不能证明供应商模型权重未变化

## 2. 存储方案

### 2.1 方案评估

| 方案 | ES 侧 | Qdrant 侧 | 优点 | 缺点 |
|------|--------|-----------|------|------|
| **A: ES `_meta` + SQL 表** | 原生 `_meta` | 新增 SQL 表 | ES 原子一致；Qdrant 必要 | 双存储路径 |
| B: 统一 SQL 表 | SQL 记录 | SQL 记录 | 接口统一 | ES: 外部重建时记录过期 |
| C: ES `_meta` + Qdrant 特殊 point | 原生 `_meta` | magic UUID point | 不需 SQL | Qdrant 侧脆弱，point 可被删 |

### 2.2 唯一推荐：方案 A

理由：
1. ES `_meta` 与索引创建原子一致，无外部状态漂移风险
2. Qdrant 无 collection 级自定义元数据（已从 `go-client v1.15.2` 源码确认），SQL 表是唯一可靠选择
3. 应用层通过统一接口查询 fingerprint（ES 读 `_meta`，Qdrant 读 SQL），上层无需感知差异

### 2.3 SQL 表设计（`index_fingerprints`）

```sql
CREATE TABLE index_fingerprints (
  id               BIGINT AUTO_INCREMENT PRIMARY KEY,
  vector_store_type VARCHAR(16) NOT NULL,  -- 'es' | 'qdrant'
  index_name       VARCHAR(255) NOT NULL,
  fingerprint_json TEXT NOT NULL,           -- 完整 Fingerprint JSON
  fingerprint_digest VARCHAR(64) NOT NULL,  -- SHA-256 hex
  embedding_model_id VARCHAR(255) NOT NULL,
  dimensions       INT NOT NULL,
  provider_name    VARCHAR(64) NOT NULL,
  similarity       VARCHAR(16) NOT NULL,
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_store_index (vector_store_type, index_name)
);
```

- **显式迁移入口**：独立 `MigrateFingerprintTable()` 函数，不在普通启动时自动执行
- 普通启动不得自动改表

### 2.4 ES `_meta` 格式

```json
{
  "go-rag-fingerprint": {
    "version": "v1",
    "embedding_model_id": "text-embedding-v4",
    "dimensions": 1024,
    "provider_name": "bailian",
    "similarity": "cosine",
    "digest": "sha256:abcdef..."
  }
}
```

### 2.5 API 能力核实（已从源码确认）

| API | 能力 | 来源 |
|-----|------|------|
| ES `create.Request{Mappings: {Meta_: Metadata{...}}}` | 创建时写 `_meta` | `go-elasticsearch/v8 v8.16.0` `types.TypeMapping.Meta_` 字段 |
| ES `indices/get` → `IndexState.Mappings.Meta_` | 读取 `_meta` | ES REST API 原生支持 |
| Qdrant `GetCollectionInfo()` | 获取 collection 信息（但无自定义元数据） | `go-client v1.15.2` |

## 3. 资源生命周期

### 3.1 状态转换表

| 场景 | 行为 |
|------|------|
| **新建**（索引不存在） | 创建 → 写 fingerprint → 回读验证 → 发布到缓存 |
| **已存在 + 匹配** | 复用；加载到缓存 |
| **已存在 + 缺失 fingerprint** | Fail closed；日志含错误码和人工指引 |
| **已存在 + 不匹配** | Fail closed；日志含期望 vs 实际 fingerprint |
| **已存在 + 损坏** | Fail closed；视同缺失 |
| **并发创建** | ES: 两进程写相同 fingerprint（相同配置）→ 回读验证。SQL: 唯一约束防重复 |
| **创建响应不确定** | 重新检查存在性 → 读取 fingerprint → 验证 → 继续或 fail |
| **部分失败**（资源已建，fingerprint 写失败） | 删除资源；不发布；报错 |
| **删除重建** | 旧 fingerprint 随资源删除；新 fingerprint 写入 |
| **别名变化** | 新名 = 新资源身份；旧 fingerprint 留在旧名直到清理 |
| **重启** | 从 source of truth 重读 fingerprint → 与当前配置比对 → 不匹配则 fail closed |

### 3.2 发布前检查（必须全部通过）

1. 资源物理存在且可访问
2. Fingerprint 已写入 source of truth 且可读回
3. Fingerprint 与当前 embedding 配置匹配
4. 三层身份一致

### 3.3 关键约束

- 物理名称唯一 ≠ 资源从未被替换（外部 delete+recreate 无法可靠检测）
- 受控边界：仅检测通过本系统创建/管理的资源；外部直接操作 ES/Qdrant 不在检测范围
- **绝不覆盖不匹配的已有 fingerprint**（保留证据供调试）
- **绝不把当前配置补写为未知旧索引的历史身份**

## 4. 旧索引策略

### 4.1 默认行为

| 条件 | 向量读 | 向量写 | 非向量文本查询 |
|------|--------|--------|---------------|
| Fingerprint 存在且匹配 | 允许 | 允许 | 允许 |
| Fingerprint 存在但不匹配 | **拒绝** | **拒绝** | 允许（诊断用） |
| Fingerprint 缺失 | **拒绝** | **拒绝** | 允许（诊断用） |
| Fingerprint 损坏 | **拒绝** | **拒绝** | 允许（诊断用） |

### 4.2 错误响应内容

- 错误码：`FINGERPRINT_MISSING` / `FINGERPRINT_MISMATCH` / `FINGERPRINT_CORRUPTED`
- 期望 fingerprint（当前配置计算）
- 实际 fingerprint（索引中读取，若有）
- 人工指引："请使用当前 embedding 配置重建索引" 或 "联系管理员"

### 4.3 本阶段不提供

- 无自动迁移
- 无原地重建
- 无通用绕过开关
- 无 "只读旧索引" 向量模式

### 4.4 非向量读取能力

- ES `match` 查询 `content` 字段（无向量组件）
- **不得**包含任何向量操作（kNN、script_score with vector）
- 明确标记为诊断专用，非生产绕过路径
- 若文本路径需要向量回退，也必须被阻断

### 4.5 与现有兼容需求的冲突

- 任何假定旧索引可直接向量读写的代码路径将在 2C-2 后中断
- 冲突路径将在设计文档中集中列出，附建议处理方式

## 5. 实际校验入口

### 5.1 接线位置

| 入口 | 文件 | 校验类型 | 时机 |
|------|------|----------|------|
| ES CreateIndex | `server/core/vector/es.go` | 写 fingerprint 到 `_meta` | 索引创建后 |
| Qdrant CreateIndex | `server/core/vector/qdrant.go` | 写 fingerprint 到 SQL | collection 创建后 |
| RAG 初始化 | `server/core/rag.go` `New()`/`InitWith()` | 读 + 验证 fingerprint | 启动/首次使用 |
| 检索路径 | `server/core/retriever.go` | 检查缓存 fingerprint | embedding 查询前 |
| 索引路径 | `server/core/indexer.go` | 检查缓存 fingerprint | embedding 文档前 |
| 异步索引 | `server/core/indexer/indexer.go`, `indexer_async.go` | 检查缓存 fingerprint | 向量写入前 |

### 5.2 Fingerprint 缓存

- 内存 `map[string]*Fingerprint`，key = `(vector_store_type, index_name)`
- `sync.RWMutex` 保护并发
- 延迟初始化（首次索引访问时）或启动时预加载
- 失效：配置变更、vector store 重连、shutdown
- Read-through：缓存未命中时从 source of truth 读取 → 验证 → 填充

### 5.3 阻断顺序

```
Fingerprint 验证 → Embedding 调用 → 向量存储操作
```

- 步骤 1 失败 → 步骤 2、3 不执行
- 在模型调用和数据副作用前阻断

### 5.4 配置快照

- 每个操作在开始时取配置快照
- 同一操作内使用同一快照，消除 TOCTOU
- 残余窗口：操作之间配置可能变更

## 6. 批次与测试

### 6.1 Batch 2C-2a: Fingerprint Core + Storage

**新增/修改文件**：
- `server/core/vector/fingerprint.go` (NEW) — Fingerprint 类型、比较、摘要
- `server/core/vector/interface.go` — 扩展接口或新增 FingerprintStore
- `server/core/vector/es.go` — `_meta` 读写 + GetFingerprint
- `server/core/vector/qdrant.go` — SQL 读写 + GetFingerprint
- `server/internal/model/gorm/` — 新增 `IndexFingerprint` GORM 模型
- `server/internal/dao/` — 显式迁移入口
- `server/core/rag.go` — 启动时生命周期检查

**测试**：
- Fingerprint schema 归一化、摘要计算、比较
- ES `_meta` 往返（httptest fake ES）
- SQL 表 CRUD（SQLite 内存测试）
- 生命周期：新建、已存在匹配、已存在不匹配、缺失、损坏
- 并发初始化
- 部分失败回滚
- 创建中取消

### 6.2 Batch 2C-2b: Validation Gates + Cache

**新增/修改文件**：
- `server/core/rag.go` — 缓存接入
- `server/core/retriever.go` — 检索前校验门
- `server/core/indexer.go` — 索引前校验门
- `server/core/indexer/indexer.go`, `indexer_async.go` — 写入前校验门

**测试**：
- 同维度不同模型 → 拒绝
- 身份缺失 → 拒绝 + 正确错误码
- 身份损坏 → 拒绝
- 缓存命中/未命中/失效
- 错误链传播
- 真实装配入口（`core.New` + fake vector store + fake embedder）
- 不使用复制生产逻辑的测试实现

## 7. 已核实事实 / 设计决定 / 待验证假设

### 已核实事实
- ES 客户端支持 `_meta` 读写（`types.TypeMapping.Meta_`）
- Qdrant 客户端无 collection 级自定义元数据
- SQL DB (GORM) 已存在，3 张业务表
- `EmbedRole.ModelID()` 和 `Dimensions()` 已实现
- `ProviderProfile.CacheKey()` SHA-256 归一化已实现
- 13+1 处维度硬编码已在 2C-1 解决
- 当前无任何 fingerprint 代码

### 设计决定
- Fingerprint 格式 v1：`{version, embedding_model_id, dimensions, provider_name, similarity}`
- 存储：ES `_meta` + SQL 表（方案 A）
- 旧索引：默认拒绝向量操作，保留文本诊断路径
- 阻断顺序：fingerprint 验证 → embedding 调用 → 向量操作
- 不覆盖不匹配的已有 fingerprint

### 待验证假设
- `indices/get` API 响应中 `IndexState.Mappings.Meta_` 的实际路径需实现时确认
- SQL 迁移是独立函数还是集成到 `dao.Init()` 带显式 flag — 实施时决定
- `config/helpers.go` 旧路径是否也写 fingerprint — 实施时决定
- 文本诊断路径是否需要额外配置开关 — 实施时决定

## 8. 推荐契约

```
Fingerprint = SHA-256({
  version: "v1",
  embedding_model_id: ProviderProfile.ModelID(),
  dimensions: int,
  provider_name: lowercase string,
  similarity: "cosine" | "dot" | "euclidean"
})

写入时机: CreateIndex 成功后立即写入
验证时机: 启动/首次访问/每次操作前
不匹配行为: 拒绝向量读写，返回错误码 + 人工指引
旧索引: 不补写、不覆盖、不默认放行
```

## 9. 新增依赖

无。ES/Qdrant 客户端库、GORM、crypto/sha256 均为已有依赖。

## 10. 执行步骤

1. 从 `2dc0b82` 创建分支 `docs/stage2c2-fingerprint-design`
2. 编写 `docs/stage2c2-design.md`，包含上述全部内容
3. 提交：`docs(stage2c2): add fingerprint persistence, identity matching, and fail-closed design`
4. 推送至 origin（不 force、不合 main）
5. 报告最终 SHA 及遗留问题
