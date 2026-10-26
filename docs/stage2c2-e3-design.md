# Stage 2C-2 E3：受控物理目标首版

代码起点：fce88e2eb15dd07d7ad8cd3c5d49f528b0e0cb7b。2026-10-25 用户已批准本节的最小支持范围；实现待复核，不代表 2C-2 验收通过，不解除 ErrEmbeddingGuardNotReady，不开始 2C-3/2D。

## 支持范围

首版只接受一个明确的具体 ES 索引名称，拒绝 alias、多目标、通配符、远程集群选择器和日期表达式。名称采用保守 ASCII 子集 `[a-z0-9][a-z0-9._-]{0,254}`，不接受前导点/下划线/连字符。ES Get 响应必须仅包含这个精确名称，不能以 alias 解析结果替代。

Qdrant SQL resourceID 不是后端 incarnation。本批继续禁用 Qdrant 生产初始化和 core.New/VerifyFingerprint 的相应路径，返回 ErrResourceBindingNotReady；低层适配器及离线测试保留，不能据其存在宣称生产可用。namespace/incarnation 决策不在本批实现。

不新增映射表、DDL、自动 alias 切换、数据迁移或清理任务。不把物理名称与 settings.index.uuid 混成一个 physical_name 字段。

## 必须显式确认的部署前提

```yaml
vector:
  managedResourceLifecycle: false
```

默认 false，必须为真正的布尔值；字符串 "true"、数字等拒绝。设为 true 是运维对以下事实的明确确认，不是程序检测出的证明，也不能仅因为想启用功能就设置：

1. 固定集群/后端命名空间与客户端连接目标，运行中不切换 endpoint、集群或恢复到不同资源。
2. 独占管理目标生命周期：所有活跃实例与后台任务排空前不得删除或替换；新一代资源使用新的物理名称，旧名称永不复用。
3. 应用写入账户不能删除、管理 alias 或覆盖 identity/mapping；受控创建由具备最小创建权限的初始化身份完成，之后运行权限收紧。不能仅凭账户无 DELETE 权限推断其他管理者不会删除。
4. 后端必须禁止写入时自动创建索引（例如部署层禁用自动创建并限制应用权限）；资源缺失时必须失败，不能自动制造无身份的新资源。
5. 人工恢复先停服务并等待后台收敛，保留原身份/失败证据，使用新名称受控创建并从原始资料重建。不得默认删除 SQL 身份记录后同名重建，不自动认领未知旧索引。

不能落实这些条件时保持 false，生产初始化拒绝；本批不调用真实 ES 管理 API 修改部署设置，不声称已验证真实权限或集群配置。true 也不会解除新 embedding profile 门禁。

## 绑定及检查顺序

factory 在任何创建前验证生命周期确认和名称语法。仅受控 CreateIndex 可以新建资源；已有身份、schema、真实 UUID 必须验证。创建后、构造图前 BindPhysicalTarget 捕获不可变的名称、预期指纹值与 UUID，安装 VectorIOGuard。

core.New 使用配置快照，要求生命周期确认和已安装 guard，不能直接绕过工厂构造未绑定生产图。同步/异步 Store 与 content/QA 检索组件使用同一个具体名称和 guard。SDK 的隐式 Qdrant 创建仍保持移除。

| 路径 | 保护位置 |
|---|---|
| Index 同步写入 | 入口 checkFingerprint；embedding 前及返回后 guard；操作后 verifyBoundResource |
| IndexAsync / 后台 QA 写入 | 同一 guard 与固定目标；embedding 返回后失败不交付向量给 SDK；操作后验证 |
| content / QA 向量检索 | embedding 前后 guard，返回向量前验明目标；聚合后验证 |
| BM25 | 发起实际检索前 guard；最终结果返回前验证 |
| 后台按 ID 读取、知识库列表、删除文档 | Config helper 在发起请求前 guard，使用配置快照中的固定名称 |

模型 A embedding 期间若目标从 UUID A 改成 B，post-embedding guard 返回 ErrResourceReplaced；实际图回归要求 bulk/search 次数为零。身份内容在同 UUID 下被改动也会比较完整预期 fingerprint，而非只比较 ID。错误使用 %w 保留链；取消不放行。启动最终验证失败时 Shutdown 收敛已构造实例，不删除资源。

## 保证边界

这不是索引级原子 CAS。最后一次检查与后端 I/O 之间仍有时间窗口；任意管理员违反不替换/不复用前提，仍可能造成错误写入。原始独立复现作为历史反例保留；新测试证明指定替换时序被拦截，不能冒充证明任意替换都安全。部署前提排除的行为必须明确记录，不把概率低当保证。

直接具体物理名称避免 alias 改指向影响本次目标；它本身不提供名称不可复用能力。资源被删除后，自动创建禁令使后续写入失败；这里的后端行为在离线 fake 中仅模拟，真实部署仍需单独核验。if_seq_no/if_primary_term 不作为物理 UUID 绑定机制，也不依据客户端序列化推断服务端新索引行为。

## 验证与交付

离线测试覆盖真实异步图、真实检索图、UUID 替换发生在 embedding 内时零 bulk/search、正常路径、alias/多结果拒绝、表达式拒绝、默认策略关闭、Qdrant 继续禁用、配置严格解析，以及现有预算/维度/取消/清理回归。关键场景使用 race 重复 20 次；串行全量、build、vet 与受影响 race 留存原始日志。

真实 ES/Qdrant/MySQL、真实权限/自动创建设置、跨集群切换与在线模型探针未验证。所有更强的逻辑映射/迁移/切换方案仍待后续设计，不在本次批准范围内。实现与验证结果单独交付，不更新仓库验收结论。
## 受控 ES bulk 的错误清理

重复 race 暴露锁定版本 SDK 在创建 bulk worker 后遇到 embedding/字段映射错误时提前返回、没有执行 Close。受控路径不再使用该后台 bulk 队列：managedESIndexer 复用原字段转换及 embedding 组件，按原 embedding 文本上限分批，完成 embedding、序列化和最终目标 guard 后发送同步 bulk。每次请求绑定同一具体物理名，使用 ctx，关闭响应体，检查每个回执的 ID、索引与状态；不接受调用级 embedding/目标覆盖。已有 embedding 回调上下文与预算 owner 保持。

失败或取消时没有尚待 flush 的后台 worker，批次可以失败并收敛；此前已成功的批次不自动回滚，也不宣称 bulk 全局事务。该路径只在已绑定的生产图中启用，未绑定的低层组件兼容路径不是生产绕过门禁的入口。