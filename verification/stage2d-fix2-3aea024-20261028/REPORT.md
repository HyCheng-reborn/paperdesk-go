# Stage 2D S-1 增量复核修复（v0.6）— 证据报告

- 固定起点（被复核提交）：`3aea024981d5137e6e64a6084ff99fcd7cf53871`
- 父提交（本批的父）：`3aea024981d5137e6e64a6084ff99fcd7cf53871`；被复核提交的父提交：`7a30899b0e205ad714b5a8ac4db152eb200bcb76`
- 分支：`codex/stage2c2-fingerprint`
- 被复核报告：`verification/independent-2d-3aea024-20261028/REPORT.md`（及其隔离复现目录 `verification/independent-2d-3aea024-isolation/`）
- 本批性质：离线修复 + 离线回归；**未执行任何在线探针**，所有对端均为本机 fake/httptest/fake 代理，未访问互联网，未连接真实模型/ES/Qdrant/MySQL。
- 本报告与其日志随本批提交一同入库；实际提交 SHA 由批次完成后的说明给出（不在本文件内回填，避免自引用）。

## 1. 五项问题与修复对照

| # | 复核问题 | 根因 | 修复 | 回归测试（实际入口） |
|---|---|---|---|---|
| 1 | online 构建可绕过 `RunOnline` 授权：直接 `NewChain` + `RunStep` 向非回环端点发送 | 授权/凭据只在 `RunOnline` 检查；`Chain` 的构造入口没有任何对应边界 | `ChainOptions` 按构建标签分定义（默认 `[APIKey]`，online `[APIKey, Credentials]`）；`resolveConstructionCredential` + `remoteSendAuthorizationError` 落在唯一构造入口 `NewChain`；`Run`/`RunStep` 经 `checkSendBoundary` 复述边界；回环端点不受限制 | `TestOnlineBuild_DirectNewChainRequiresAuthorization`（4 子项）；原反例 `TestReviewOnlineDirectChainSkipsAuthorization`（overlay 复跑） |
| 2 | 另一并发步骤触发预算耗尽并停止后，在途步骤仍发起新重试 | 停止只是一个普通布尔；重试前检查与发送之间存在竞争窗口；已准入的尝试不会被取消 | `beginAttempt` 在同一把锁下检查 `stopped` 并登记在途句柄；`stop` 锁内记录首个脱敏摘要并取走全部句柄、锁外取消；重试前 `isStopped()` 命中即包装 `ErrAlreadyStopped` 并停止 | `TestConcurrentStop_RefusesNewRetryAndConverges`；原反例 `TestBudgetReviewRetriesAfterAnotherStepStopsChain`（overlay 复跑，命中数保持 1） |
| 3 | 完整预检不校验后续角色的模型身份（`m@v1`），先发一步再拒绝 | 预检只校验字段与步骤形状；adapter 的 `RequestModel()` 校验发生在懒构建时 | 新增 `roles.go`：`roleProfiles` 与 `ValidateRoles` 复用生产 `NewProviderProfile` + `(*ProviderProfile).RequestModel`，接入 `Preflight`、`Readiness(plan)` 与 `NewChain` | `TestPreflight_RejectsVersionedRoles`、`TestNewChain_RejectsVersionedRoleBeforeAnySend`、`TestChainRun_VersionedRoleStopReasonIsSanitized`、`TestOnlineBuild_PreflightRejectsVersionedLaterRoleBeforeAnySend`；原反例 `TestBudgetReviewVersionedChatPassesPreflightThenSends`（overlay 复跑） |
| 4 | `Plan` 浅拷贝导致运行期读取 `Budget.Unlimited` 与调用方写并发（真实 DATA RACE） | `Plan` 值拷贝仍共享 `Budget.Unlimited` 底层数组；步骤的 `Texts`/`Docs` 同样被共享 | 新增 `clone.go`：`Plan.Clone`（深拷贝 `Unlimited`）、`step.clone`/`cloneSteps`/`cloneDocuments`（含 `MetaData`）；`NewChain` 首行克隆，`Run`/`RunStep` 入口克隆 | `TestNewChain_FixedBudgetAndAuthorizationAfterConstruction`、`TestNewChain_MutablePlanRaceRegression`、`TestRunStep_ClonesStepInputs`；原反例 `TestBudgetReviewMutablePlanRace`（overlay + `-race` 复跑） |
| 5 | 空凭据误报就绪：Readiness 只看变量是否存在，执行时才拒绝空值 | 两处凭据判定分裂，空值/纯空白/空白名/多声明口径不一致 | `Credentials.Validate/Resolve/Presence/Missing` 成为唯一实现；Readiness、`RunOnline`、online 构造期共用；错误与证据只出现变量名 | `TestCredentials_SharedRules`、`TestCredentials_PresenceTreatsEmptyAsMissing`、`TestOnlineBuild_CredentialBoundarySharedByReadinessAndExecution`；原反例 `TestReviewReadinessAcceptsEmptyCredential`（overlay 复跑） |

## 2. 逐项实测证据

### 问题 1（直接入口授权边界）

- `TestOnlineBuild_DirectNewChainRequiresAuthorization`：
  - 未授权 + Chat 回环 / Embed、Rerank 远端 → `NewChain` 返回 `ErrNotAuthorized`，本机模型命中 0，fake 代理命中 0；
  - 已授权 + 字面量 `APIKey` → `ErrNotConfigured`（非回环只接受环境变量声明），错误不含密钥值，零发送；
  - 已授权 + 环境变量凭据 → 构造成功且构造期代理命中 0；
  - 未授权 + 全回环 fake + `APIKey` → 构造成功，`RunStep(chat)` 命中 1（未授权的本机离线 fake 未被破坏）。
- 原反例在修复后的表现：`TestReviewOnlineDirectChainSkipsAuthorization` 在 `zz_review_isolation_test.go:8` 失败，信息为 `unauthorized NewChain rejected: probe: online execution is not authorized`——即绕过路径已被关闭。

### 问题 2（停止后不得重试）

- `TestConcurrentStop_RefusesNewRetryAndConverges`：Chat 在途阻塞（`MaxConcurrency=1`），Embed 因唯一并发额度返回 `ErrBudgetExhausted` 并触发全局停止；放行后 Chat 返回 `ErrAlreadyStopped`，`ChatHits` 保持 1，`EmbedHits=0`，`InFlight=0`，`CommittedAttempts=1 && AttemptsUsed=1`，`InputReserved/OutputReserved=0`；停止后再次 `RunStep` 仍为 `ErrAlreadyStopped`。
- 语义变更（已同步更新测试）：`TestW3_ConcurrencyLimitAppliesAtProbeEntry` 的第一个步骤现在会因全局停止而收到 `ErrAlreadyStopped`（在途尝试被取消），并断言 `ChatHits==1`。
- 原反例：`TestBudgetReviewRetriesAfterAnotherStepStopsChain` 现在在 `zz_review_race_test.go:19` 失败，信息为 `first err=probe: run already stopped ... hits before=1 after=1`——不再出现 1 → 2 的重试命中。

### 问题 3（预检角色模型身份）

- `TestPreflight_RejectsVersionedRoles`：`later_chat_versioned` / `later_rerank_versioned` / `first_embed_versioned` 三例，`Preflight` 返回 `providers.ErrModelVersionNotExpressible`，`Ready()==false`，plan 项 detail 含 `model version`。
- `TestNewChain_RejectsVersionedRoleBeforeAnySend`：构造即失败，本机三模型端点命中合计 0。
- `TestOnlineBuild_PreflightRejectsVersionedLaterRoleBeforeAnySend`：`RunOnline` 同源拒绝，零步骤报告，三端点命中 0，fake 代理 0。
- `TestChainRun_VersionedRoleStopReasonIsSanitized`：`ClassifyError` 输出 `category=model_version_not_expressible`（不含模型/URL 原文）。
- 原反例：`TestBudgetReviewVersionedChatPassesPreflightThenSends` 现在在 `zz_review_race_test.go:8` 失败，信息为 `expected erroneous readiness true: ... plan OK:false ... chat role: providers: protocol cannot express model version ...`——Readiness 在发送前即拒绝。

### 问题 4（可变引用与数据竞争）

- `TestNewChain_FixedBudgetAndAuthorizationAfterConstruction`：构造后把 `MaxRequests` 改为 100、`Unlimited[0]` 改为 `bogus`、`StepDeadline=0`、`Retry.MaxAttempts=9`、撤销授权；运行仍按构造期 `MaxRequests=1` 在第二步得到 `ErrBudgetExhausted`，`ChatHits=1`、`EmbedHits=0`、`StopReason=category=budget_exhausted`。
- `TestNewChain_MutablePlanRaceRegression`：构造后并发写 `Unlimited[0]` 5000 次 + 100 次 `Run`，`-race` 下无竞争，`CommittedAttempts=100`。
- `TestRunStep_ClonesStepInputs`：调用返回后修改 `Texts`/`Docs` 不影响已完成运行。
- 原反例：`TestBudgetReviewMutablePlanRace` 经 `-overlay` + `-race -count=3` 复跑，3 次均 PASS，日志无 `DATA RACE`。

### 问题 5（凭据边界）

- `TestCredentials_SharedRules`（8 例）：未声明 / 空白变量名 / 双声明 / 重复声明 / 不存在 / 空值 / 纯空白均使 `Validate` 与 `Resolve` 返回同一 `ErrMissingCredentials`；合法单声明返回值；断言错误不含标记值。
- `TestCredentials_PresenceTreatsEmptyAsMissing`：`Presence` 把空值/纯空白视为不可用；`Missing` 保持声明顺序并去重，只回显变量名。
- `TestOnlineBuild_CredentialBoundarySharedByReadinessAndExecution`（6 个非法例 + 1 个合法例）：非法时 `Ready()==false`、`RunOnline` 返回 `ErrMissingCredentials`、报告零步骤、fake 代理命中 0，Readiness detail 与错误均不含凭据值；合法时 `Ready()==true` 且 3 步可执行、代理命中 0（回环）。
- 原反例：`TestReviewReadinessAcceptsEmptyCredential` 现在在 `zz_review_isolation_test.go:17` 失败，detail 为 `PROBE_REVIEW_EMPTY is set but empty (an empty value is not a credential)`——只出现变量名。

## 3. 保留项回归（上一批已修复内容）

- 默认构建禁止非回环远端：`TestDefaultBuild_CheckEndpointsRejectsRemote`、`TestDefaultBuild_RemoteEndpointsRejectedWithZeroSends`、`TestDefaultBuild_ChainOptionsHasNoRemoteSwitch`（反射锁定 `ChainOptions` 字段集合仍为 `[APIKey]`）。
- online 非回环强制 HTTPS / 禁 userinfo：`TestOnlineBuild_HasRemoteCapability`、`TestOnlineBuild_PlaintextRemoteRejectedBeforeAnySend`、`TestOnlineBuild_UserInfoEndpointRejectedWithoutEcho`。
- 被禁止的重定向不重试：`TestRedirect_ProhibitedIsNotRetried`。
- `StopReason` 脱敏 + `errors.Is` 链：`TestStopReasonIsSanitized`、`TestChainRun_VersionedRoleStopReasonIsSanitized`。
- W-1..W-5：`TestW1_RetryReservesAndCommitsEveryAttempt`、`TestW2_ExhaustedBudgetSendsNothingFurther`、`TestW3_ExpiredCallerContextStopsBeforeSend`、`TestW3_DeadlineDuringSendIsNotRetried`、`TestW3_ConcurrencyLimitAppliesAtProbeEntry`、`TestW4_UsageSettlement`、`TestW5_UnexpectedErrorStopsSubsequentSteps`。
- 完整 fingerprint / fake ES guard / Qdrant 禁用：`TestProbeChain_ProductionBindingAndScope`、`TestProbeChain_IdentityMismatchRejected`、`TestProbeChain_GuardRejectsReplacedTarget`、`TestProbeChain_ResponseDimensionMismatchRejected`；本批未新增映射表或 DDL，未修改 Qdrant 绑定路径。

## 4. 命令、退出码与实际命中

| # | 命令（工作目录 `server/`，除注明外） | 退出码 | 实际结果 | 日志 |
|---|---|---|---|---|
| 1 | `go test -p 1 ./core/probe/... -count=1 -v` | 0 | `--- PASS` 32，`--- FAIL` 0 | `test-probe-default.log` |
| 2 | `go test -tags online -p 1 ./core/probe/... -count=1 -v` | 0 | `--- PASS` 39，`--- FAIL` 0 | `test-probe-online.log` |
| 3 | `PATH+=<TOOLCHAIN>/ucrt64/bin`, `CC=<TOOLCHAIN>/ucrt64/bin/gcc.exe`; `go test -race -p 1 ./core/probe/... -count=1 -v` | 0 | `DATA RACE` 0 行 | `race-probe-default.log` |
| 4 | `go test -tags online -race -p 1 ./core/probe/... -count=1 -v` | 0 | `DATA RACE` 0 行 | `race-probe-online.log` |
| 5 | `go test -race -p 1 ./core/probe/ -count=5 -v -run '<授权/并发停止/预检/克隆/W1-W5/重定向/停止原因>'` | 0 | 5 轮全部 PASS，`DATA RACE` 0 行 | `race-targeted-repeat-default.log` |
| 6 | `go test -tags online -race -p 1 ./core/probe/ -count=5 -v -run '<online 授权/预检角色/凭据/W1-W5/并发停止/克隆>'` | 0 | 5 轮全部 PASS，`DATA RACE` 0 行 | `race-targeted-repeat-online.log` |
| 7 | `go test -p 1 ./... -count=1`（串行全量） | 0 | 18 个包全部 `ok`，无 `FAIL` | `test-full-serial.log` |
| 8 | `go build ./...` | 0 | 无输出 | `build-vet-online-vet.log` |
| 9 | `go vet ./...` | 0 | 无输出 | 同上 |
| 10 | `go vet -tags online ./...` | 0 | 无输出 | 同上 |
| 11 | `gofmt -l core/probe` | 0 | 无输出（已格式化） | 同上 |
| 12 | `go test -tags online -race ./core/probe/ -run '^TestBudgetReviewMutablePlanRace$' -count=3 -v -overlay overlay-race.json` | 0 | 原 DATA RACE 反例 3 次 PASS，无 `DATA RACE` | `review-race-repro.log` |
| 13 | `go test -tags online ./core/probe/ -run '^TestReview…$' -v -overlay overlay-isolation.json` | 1（预期） | 三个原反例在各自"缺陷期望"断言处 FAIL | `review-isolation-repro.log` |
| 14 | `go test -tags online ./core/probe/ -run '^(TestBudgetReviewVersionedChat…|TestBudgetReviewRetries…)$' -v -overlay overlay-race.json` | 1（预期） | 两个原反例在各自"缺陷期望"断言处 FAIL | `review-behavior-repro.log` |

第 13/14 条为**预期失败**：这些用例是为复现缺陷而写，断言"缺陷存在"（例如"未授权 NewChain 应被接受"）。修复后它们在断言处失败，正说明反例不再复现；具体失败信息见 §2。

## 5. 原始反例复跑机制（overlay）

- 使用 `go test -overlay` 把复核目录中的原始测试文件以虚拟文件名映射进 `server/core/probe` 包（`overlay-race.json`、`overlay-isolation.json`），**不向仓库写入任何临时测试文件**；`review-race-repro_test.go`、`review-isolation-repro_test.go` 是原文件的逐字节副本，仅用于引用与留证。
- `TestBudgetReviewMutablePlanRace` 不含缺陷断言，修复后应当通过；其余反例断言缺陷行为，修复后应当失败。

## 6. 剩余边界与未验证项

- 仍未执行任何真实模型、真实 ES/Qdrant/MySQL 或互联网调用；在线探针（含 §5.1 矩阵、S-2/S-3）未执行；未解除任何门禁，未更新验收状态。
- 授权声明仍是调用方提供的结构体（`Plan.Authorization`）；本批保证的是"任何向非回环端点发送的构造/执行入口都必须通过该声明的校验并且凭据只能来自环境变量"，不构成对声明真实性的独立证明（该证明仍依赖授权文本与人工批准流程）。
- 并发契约：构造期间调用方不得与 `NewChain` 并发修改入参；构造返回后链只读私有副本；`Run`/`RunStep` 入口只读取一次入参，调用进行中并发修改入参仍属调用方违约。
- 全局停止的语义：`stop` 会取消已准入的在途尝试，因此并发步骤可能收到 `ErrAlreadyStopped`（包装原始取消原因），而不是继续重试；这是本批明确选择的"取消 + 原子准入"契约，已同步更新 W-3 用例。
- fake 代理的零命中断言依赖 `TestMain` 在 `m.Run` 之前设置代理环境变量（`ProxyFromEnvironment` 每进程只解析一次）；控制组 `TestProxyControl_EnvironmentIsHonored` 证明该断言非空。
- 本批未纳入提交：`verification/independent-*`（含本次复核证据）、既有 `.git-commit-msg.txt` 改动、既有 `verification/stage2d-offline-a12af92-20261028/CLAIMS.md` 改动。
