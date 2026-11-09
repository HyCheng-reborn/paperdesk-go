# 用例 → 问题 → 验证点

P1 =「模型版本身份与实际请求不一致」；P2 =「直接 `common.NewEmbedding` 入口未比较完整 fingerprint」。
全部用例只使用内存 transport / `httptest` / 本地 fake，不触达真实模型、ES、Qdrant、MySQL。

## P1：版本身份与 wire 请求的一致性（fail closed）

| 用例 | 文件 | 验证点 |
| --- | --- | --- |
| `TestRequestModelIDRejectsVersionedIdentity`（6 子例） | `server/core/providers/adapter_request_model_test.go` | 共享解析规则：`text-embedding-v4` → 原值（含首尾空白 trim）；`…@v1` → `ErrModelVersionNotExpressible`；`m@`、`@v1`、`m@v1@v2` → `ErrModelIDInvalid`（非法版本拒绝保留） |
| `TestP1_VersionedProfileFailsClosedForAllBuilders`（chat/embed/rerank） | 同上 | Profile 身份仍为 `m@v1`（`ModelID()` 不变），但 `RequestModel()` 失败；三个共享构造器全部 fail closed，且 round-tripper 计数 = 0（一个 HTTP 请求都不发） |
| `TestP1_WireModelEqualsBoundIdentity` | 同上 | 读取**真实 HTTP 请求体** `model`：embed 请求体 model == `embedRole.ModelID()`（`text-embedding-v4`），chat 请求体 model == `chatProfile.ModelID()`；不是只断言 `ModelID()` |
| `TestP1_BailianBuildersFailClosedForVersionedProfile` | `server/core/providers/bailian/request_model_test.go` | 百炼构造链：`BuildEmbed`（委托 `providers.BuildEmbedRole`）与 `BuildChat`（直接使用 `RequestModel()`）对 `m@v1` 全部 fail closed，请求计数 = 0 |
| `TestP1_VersionedProviderConfigFailsClosed` | `server/core/common/embedding_p1_p2_test.go` | 配置 `m@v1`：`ExpectedEmbeddingFingerprint` 与 `NewEmbedding` 都在身份解析阶段失败；错误链同时含 `providers.ErrModelVersionNotExpressible` 与 `common.ErrEmbeddingConfigInvalid`；`Registry.GetEmbedder` 未被调用 |
| `TestComputeFingerprintMetaRejectsVersionedProviderConfig` | `server/core/fingerprint_provider_identity_test.go` | Rag 指纹门禁（`core.ComputeFingerprintMeta`）对 `text-embedding-3-large@v1` 返回 `ErrModelVersionNotExpressible`（不会生成带版本的指纹） |
| `TestP1_VersionedProviderEmbeddingConfigFailsClosed` | `server/internal/logic/rag/stage2c3_fix2_regression_test.go` | 真实 YAML → `readRagConfig` → `ProviderProfile`：`ComputeFingerprintMeta` 与 `buildRegistry`+`GetEmbedder` 均 fail closed |
| `TestP1_FactoryFailsClosedBeforeTouchingES` | `server/internal/logic/rag/stage2c3_fix2_factory_test.go` | 真实 YAML → `readRagConfig` → `defaultRagFactory.Create`：在指纹计算阶段即失败，且 ES fake 收到 0 个请求（不会读取/创建/认领索引） |
| `TestNewProviderEmbedding_VersionedConfigFailsClosed` | `server/core/common/provider_embedding_test.go` | 直接入口对带版本配置 fail closed，`Registry.GetEmbedder` 调用次数 = 0 |
| `TestR2_VersionMismatch` / `TestR2_VersionMatch` | 同上 | 带版本配置无论 role 版本不同/相同都 fail closed（不再只把版本写进角色标签），且未调用 Registry |
| `TestP12_EmbeddingIdentityFailsClosed/role_adds_version_to_unversioned_config` | `server/core/common/embedding_p12_p22_test.go` | 配置无版本、role 额外声明 `@v2` → `ErrEmbeddingIdentityMismatch`（同维度不同身份拒绝保留） |
| `TestP12_EmptyVersionConfigRejected`（`m@`、`@v1`、`m@v1@v2`） | 同上 | 非法版本表示继续被拒绝 |
| `TestNewProviderEmbedding_ModelMismatch` / `TestNewProviderEmbedding_DimensionMismatch` / `TestP12_EmbeddingIdentityFailsClosed/different_model_same_dimensions` | 同上 | 同维度不同身份仍然拒绝（未被本次修复放宽） |
| `TestP1_WireModelEqualsBoundIdentity` + `TestBuildEmbedRole_SupportsDimensionsRequest`（既有） | `server/core/providers/adapter_test.go` | 模型选项行为（`supports_dimensions_request`）未回退；相关 profile 已去除版本，`ModelID()` 期望为 `text-embedding-v4` |

## P2：直接入口与 Rag 门禁共用完整 fingerprint

| 用例 | 文件 | 验证点 |
| --- | --- | --- |
| `TestP2_ExpectedFingerprintUsesSharedConstructor` | `server/core/common/embedding_p1_p2_test.go` | `common.ExpectedEmbeddingFingerprint` 与 `vector.NewExpectedEmbeddingFingerprint` 的 `Equal` 与 `Digest` 完全一致，`Similarity == cosine`：期望指纹只有一份构造 |
| `TestP2_DirectEntryComparesFullFingerprint/matching_cosine_binding_is_accepted` | 同上 | 正常匹配：cosine 绑定 + 同身份 → `NewEmbedding` 成功且 `EmbedStrings` 可用 |
| `TestP2_DirectEntryComparesFullFingerprint/dot_binding_is_rejected` | 同上 | 不同 similarity：provider/model/dims 全同、仅绑定 similarity=dot（schema `dot_product`）→ `ErrEmbeddingIdentityMismatch` + 错误链含 `vector.ErrFingerprintMismatch`，错误文本点名 `similarity`，`Registry.GetEmbedder` 调用次数 = 0 |
| `TestComputeFingerprintMetaSharesDirectEntryExpectation` | `server/core/fingerprint_provider_identity_test.go` | Rag 门禁与直接入口的期望指纹 `Equal` + `Digest` 相同，similarity 均为 cosine |
| `TestP2_DefaultFactoryRejectsNonCosineTarget` | `server/internal/logic/rag/stage2c3_fix2_factory_test.go` | 默认工厂：既有索引 stored/`_meta`/schema 全部为 dot（只差 similarity）→ `defaultRagFactory.Create` 返回 `vector.ErrFingerprintMismatch`（含 `similarity`），且写请求计数 = 0（不认领、不重建、不覆盖） |
| `TestP13_FactoryPathBindsTargetForDirectEntry`（既有） | `server/internal/logic/rag/stage2c3_fix_regression_test.go` | 默认工厂正常匹配路径仍可成功构建并绑定 cosine 目标；直接入口在缺少绑定时 fail closed |

## 支撑用例（共用测试设施）

| 用例/设施 | 文件 | 说明 |
| --- | --- | --- |
| `newTestBindingEnvWithSimilarity` / `esDistance` | `server/core/common/binding_testenv_test.go` | 生成指定 similarity（cosine/dot/euclidean → cosine/dot_product/l2_norm）的 httptest ES 索引与可信绑定，供 P2 负向用例使用；原 `newTestBindingEnv` 复用同一实现 |
| `esFingerprintServerWithSchema` | `server/internal/logic/rag/stage2c3_fix_regression_test.go` | 在原 ES fake 上增加 similarity 参数与写请求计数（HEAD/GET 之外的请求计数），供工厂层 P1/P2 用例断言「无写操作」 |
| `resetProcessBudget` | `server/internal/logic/rag/stage2c3_fix2_factory_test.go` | 进程级 `processBudget` 单例的清理/恢复，避免用例间串扰 |

## 未被本用例集覆盖（与报告口径一致）

- 未证明默认工厂曾错误写入非 cosine 索引；P2 只证明当前入口使用完整 fingerprint 比较，且工厂不会认领/覆盖不同 similarity 的既有索引。
- 未使用真实模型服务验证「服务端是否接受 `m@v1`」：两个受支持协议（openai-compatible / dashscope-native）的 wire `model` 字段没有独立版本参数，因此按 fail closed 处理，而不是假设服务端能解释 `@version`。
