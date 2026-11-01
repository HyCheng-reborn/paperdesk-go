# 用例清单（Stage 2C-3 修复）

所有用例都调用生产共享实现（`common.NewEmbedding` / `common.GetQAModelForRag` /
`common.GetChatModelForRag` / `config.ValidateStrictBoolField` /
`vector.BindPhysicalTarget` / `defaultRagFactory.Create` + `readRagConfig`），
本机只使用 `httptest` 假服务与 fake role，不接触真实模型 / ES / Qdrant / MySQL。

## P1-1 QA 回退绕过 Registry 预算

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestP11_QAAndEmbeddingShareRegistryBudget` | `core/common/chat_budget_test.go` | 真实 `providers.NewRegistry`（budget=1）+ 真实 HTTP 假模型：QA 无 QA profile 时经 QA→Chat 回退发出 **1 次** chat 请求；随后同一 Registry 的 Embedding 被 `ErrBudgetExhausted` 拒绝且 embedding HTTP = **0**；`Snapshot` 仅 1 次 committed attempt。 |
| `TestP11_LegacyFallbackRequiresBudgetOwner` | 同上 | Registry 未暴露进程预算时 legacy 回退 `ErrLegacyRoleBudgetUnavailable`（fail closed）；暴露预算时第二次 `Generate` 被拒且内层模型只被调用 1 次。 |
| `TestP11_LegacyChatUsesRegistryBudget` | 同上 | legacy Chat 经 `GetChatModelForRag` 得到真实 HTTP 模型，第一次请求通过，第二次 HTTP 请求被预算拒绝（实际外部请求数 1）。 |

## P1-2 Embedding 身份校验

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestP12_EmbeddingIdentityFailsClosed` | `core/common/embedding_p12_p22_test.go` | 6 个子例：role 不实现 `ProfiledEmbedRole`、profile provider 为空、同维度不同 Provider、同维度不同模型、同模型不同版本、role 空版本（`m@`）全部 `ErrEmbeddingIdentityMismatch`。 |
| `TestP12_EmptyVersionConfigRejected` | 同上 | 配置身份 `m@`、`@v1`、`m@v1@v2` 被拒绝（`ErrEmbeddingConfigInvalid`/`ErrEmbeddingIdentityMismatch`），不与 `m` 互为别名。 |
| `TestR2_ConfigNoVersion_RoleHasVersion` / `TestR2_VersionMatch` / `TestR2_VersionMismatch` | `core/common/provider_embedding_test.go` | 版本表示的单向严格匹配（原有用例，绑定环境下重跑）。 |

## P1-3 可信物理绑定

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestP13_UntrustedBindingIsRejected` | `core/common/embedding_p12_p22_test.go` | 直接入口 `NewEmbedding` 拒绝：缺失绑定、`any` 类型不对、`&vector.TargetBinding{}` 零值、目标名不符、外来 ES client —— 全部 `ErrEmbeddingProfileNotReady`。 |
| `TestP13_GuardWithoutBinding_Fails` | `core/common/provider_embedding_test.go` | 仅有任意非 nil `VectorIOGuard` + 生命周期 true + 非空 IndexName **不能**代替绑定。 |
| `TestNewProviderEmbedding_GuardPropagation` | 同上 | 绑定后的 guard 在 `EmbedStrings` 前后各重读一次具体目标（假 ES 请求计数增长）。 |
| `TestTargetBindingZeroValueFailsClosed` | `core/vector/target_test.go` | `Check` / `Verify` / `Guard()` 对零值绑定均 `ErrResourceBindingNotReady`。 |
| `TestPhysicalTargetBindingCapturesIdentityAndPropagatesCancellation` | 同上 | 绑定捕获值副本（调用方改 fingerprint 不影响）、取消传播 `context.Canceled`、资源替换 `ErrResourceReplaced`。 |
| `TestFingerprintChatOnlyProviderPreservesStoredIdentity` | `core/fingerprint_provider_identity_test.go` | 非具体 ES 实现不可绑定（`ErrResourceBindingNotReady`），且不重写已存身份。 |
| `TestP13_FactoryPathBindsTargetForDirectEntry` | `internal/logic/rag/stage2c3_fix_regression_test.go` | 工厂路径（`defaultRagFactory.Create` → `BindPhysicalTarget` → `core.New`）在可验证的假 ES 目标上成功；同一身份绕过工厂的直接入口 `common.NewEmbedding` 无绑定 → `ErrEmbeddingProfileNotReady`。 |

## P2-1 legacy Chat 独立配置

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestP21_LegacyChatUsesDedicatedEndpointAndCredential` | `internal/logic/rag/stage2c3_fix_regression_test.go` | 真实 YAML（`embedding.*` 与 `chat.*` 各自 key/baseURL/model）→ `readRagConfig` → `GetChatModelForRag` → HTTP：命中 chat 端点、`Bearer chat-test-key`、model=`legacy-chat`，embedding 端点 0 次。 |
| `TestP21_MixedProviderChatUsesRegistryNotLegacyChat` | 同上 | 混合角色另一方向：YAML 同时有 `provider.chat_model` 与 legacy `chat`，经 `buildRegistry` 后 Chat 走 Provider 端点/凭据（`Bearer provider-test-key`），legacy 端点 0 次。 |
| `TestP11_LegacyChatUsesRegistryBudget` | `core/common/chat_budget_test.go` | legacy Chat 使用 `ChatAPIKey`/`ChatBaseURL` 且纳入预算。 |

## P2-2 Embedding 选项

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestP22_OptionsAreFilteredWithoutSilentLoss` | `core/common/embedding_p12_p22_test.go` | 锁定 Eino v0.6.0 真实 option 语义：`markerA`、同模型 `WithModel(bound)`、`markerB` 三个选项按原顺序原样传递（无丢失/重排，`GetCommonOptions` 仍见绑定模型）；换成 `WithModel("other-model")` 时返回 `ErrEmbeddingModelOverride` 且内层 embedder 未被调用。 |
| `TestR2_FilterEmbeddingOptions_StripsWithModel` | `core/common/provider_embedding_test.go` | 与上同一生产函数 `filterEmbeddingOptions` 的回归（拒绝而非静默丢弃）。 |

## P2-3 显式 YAML null

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestStrictBool_ExplicitNull_Rejected` | `core/config/strict_bool_test.go` | 显式 null（父映射存在该 key、值为 nil）→ `must be a boolean` 错误。 |
| `TestStrictBool_MissingKeyInPresentParent_NoError` | 同上 | 父映射存在但没有该 key → 视为缺失，默认 false，不报错。 |
| `TestStrictBool_MissingField_NoError` / `_True_` / `_False_` / `_StringTrue_` / `_Number_` / `_Nonsense_` / `_ReadError_` | 同上 | 缺失默认、true/false 正常、字符串/数字/无意义值报错、读取错误透传。 |
| `TestP23_ExplicitNullIsNotMissing` | `internal/logic/rag/stage2c3_fix_regression_test.go` | 真实 YAML → `readRagConfig` → `config.ValidateStrictBoolField`：缺失（false 默认，不下发 dimensions）、`null`、`~`、`"true"`、`1` 报错；`true`/`false` 正常。 |

## Provider 名称规范化（专项）

| 用例 | 位置 | 验证点 |
| --- | --- | --- |
| `TestProviderNameNormalizationIsConsistent` | `core/common/embedding_p12_p22_test.go` | `"  OpenAI "` 在 fingerprint（`NewFingerprint`）、Registry Profile（`NewProviderProfile`）与配置身份解析中统一规范化为 `openai`；混合大小写配置 + 规范化绑定 + 规范化 role 可成功构造 Embedding。 |
