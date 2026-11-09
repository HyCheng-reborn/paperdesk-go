# Stage 2D S-1 第四轮修复报告（针对 `1e81aa4` 的第三轮独立复核）

**性质**：离线修复 + 离线证据。**未执行任何在线探针**；所有对端均为本机 fake 模型 / 本机 fake ES（`httptest` 本机 HTTP）/ 本机 fake 代理 / 外部 overlay。未访问互联网，未连接真实模型、ES、Qdrant、MySQL，未使用真实凭据。

- 固定起点：`1e81aa406501219d4ce1ad7c2083c4819cfcf644`
- 父提交：`bdd3a6dedb4a0284e1db8925de489c80d5aba3d3`
- 分支：`codex/stage2c2-fingerprint`
- 复核报告：`verification/independent-2d-1e81aa4-20261028/REPORT.md`
- 补充复现（只读、未修改）：`verification/independent-2d-1e81aa-gate/`、`verification/independent-2d-1e81aa-validation/`
- 本批证据目录：`verification/stage2d-fix4-1e81aa4-20261028/`

## 1. 逐项修复

### 1.1 问题 1：停止标记与实际发送的关系

**四状态拆分**（`server/core/probe/sendgate.go` 文件头与 `probe.go` 包文档）：

| 状态 | 可观测点 | 停止标记之后 |
|---|---|---|
| 1. 入口已准入 | `beginAttempt` 在 `c.mu` 写侧登记在途句柄 | 不会发送：闸门拒绝 `ErrAlreadyStopped`，零字节发出 |
| 2. 已通过发送闸门 | `commitSend` 在 `c.mu` 读侧返回（与 `stop` 写侧同一把 RWMutex，**线性化点**） | 不再受影响，请求继续流向 handoff |
| 3. 已交给底层 transport | 调用 `g.inner.RoundTrip` 开始 | **不可撤销**，只能取消 ctx 让传输层尽快放弃 |
| 4. 对端已收到 | 仅 fake/代理自身计数可观测 | 不属于本包可观测事实，也不作承诺 |

**能保证**：停止标记之后零新闸门准入、零新重试；第 1 类尝试返回 `ErrAlreadyStopped` 且零字节发出。

**不能保证**：字面“停止标记之后零新发送”。第 2 与第 3 步之间没有锁；若持锁跨越整个 handoff，`stop` 将无界等待（被明确禁止），因此不改用该设计。

**确定性测试**（`sendgate_test.go`）：

- `TestSendGate_PostGatePreTransportWindowIsNotClosed/cancel_applied_before_handoff`：请求被固定在“已过闸门、未 handoff”，`stop` 同步返回（含取消）后放行 → 本轮 0 命中。**该结果不构成保证**：取消与 handoff 并发，落后时请求照常发出。
- `TestSendGate_PostGatePreTransportWindowIsNotClosed/stop_marker_does_not_block_handoff`：用 `Chain.beforeCancel`（仅测试钩子，生产 nil）把“标记已发布”与“取消已施加”确定性分开，放行 handoff → **hits=1**，且步骤以 `ErrAlreadyStopped` 结束、in-flight 归零。日志 01 明确打印 `window NOT closed`，测试名称与注释均不声称契约通过。
- 原 20,000 句柄形状用例 `TestSendGate_OriginalReviewStopWindowShapeHasNoSend` 保留（5 次迭代 0 命中），只证明第 1 类尝试零发送。

**独立复跑**（复核原始源码，overlay，未修改）：

| 日志 | 结果 |
|---|---|
| `20-review-gate-admission.txt` | exit 0；`-race -count=5`，每轮 5 次试验 → **5/5 命中**（停在标记时 0，放行后 1） |
| `21-review-gate-ctx.txt` | exit 0；同样 **5/5 命中**，且 `context at release=<nil>`：请求是在 ctx 仍有效时被交给 transport 的，不是“取消失败” |

人工登记 100,000 个句柄与包装 RoundTripper 只用于构造“标记发布 → 逐个取消”的时序，**不代表常规触发概率**。

**结论：该项未闭合。** 保留具体测试、权衡与待批准方案（`design/stage2d-plan.md` §9.1 的三个选项：接受较弱契约 / 两阶段停止 + 有界等待 / 串行化登记-发送），**不把较弱语义写成原契约通过**，等待用户决定。任何选项都不承诺撤销已发出的请求，也不引入无限持锁、死锁或无界等待。

### 1.2 问题 2：发送证据计数

`StepReport` 现在区分四类可观测事实：

| 字段 | 含义 | 是否等价于“对端收到” |
|---|---|---|
| `Attempts` | **预算已承诺**的尝试次数（= `CommittedAttempts` 增量，含受控重试） | 否：预算承诺在闸门之前，可能被闸门拒绝且零命中 |
| `GateAdmissions` | 通过发送闸门的次数（停止标记之后恒为 0） | 否 |
| `TransportCalls` | 开始调用底层 `RoundTripper` 的次数 | 否（不证明字节到达/服务端已处理） |
| `CommittedAttempts` | 请求级 owner 的预算承诺计数 | 否 |

实现：`sendTrace`（两个 `atomic.Int64`）经请求 ctx 传递（`withSendTrace`/`sendTraceFrom`），`sendGate.RoundTrip` 在 `commitSend` 成功后记录，`RunStep` 按每次尝试累加。

**保留原有保守结算**：闸门拒绝时 `Attempts=1`、`CommittedAttempts=1`，不回滚预算；lease 收敛（`InFlight`/预留归零）；错误链保留 `errors.Is(err, ErrAlreadyStopped)`。复核用例 `22` 的 `Attempts=1/committed=1/hits=0` 断言仍成立，但现在可通过新字段看到 `GateAdmissions=0/TransportCalls=0`（`TestStepReport_CountsSeparateCommitFromSend/gate_rejected_keeps_committed_budget`）。

三子例实测（日志 01/04，均 PASS）：

| 子例 | Attempts | GateAdmissions | TransportCalls | 对端命中 |
|---|---|---|---|---|
| `gate_rejected_keeps_committed_budget` | 1 | 0 | 0 | 0 |
| `sent_once` | 1 | 1 | 1 | 1 |
| `retry_counts_each_attempt`（`EmbedAbortFirst=1` + `MaxAttempts=2`） | 2 | 2 | 2 | 2 |

### 1.3 问题 3：目标规范化与就绪一致性

**选择**：各入口一致**明确拒绝非规范输入**（`requireCanonical` / `requireCanonicalLower`），不做静默 TrimSpace/ToLower；**不改变已验收的存量 fingerprint 格式**。

- `Plan.Validate`：`chat/embed/rerank.model` 必须无首尾空白；`target.provider_name`/`target.similarity` 还必须已小写。
- `NewLocalFakeESTarget`（fake ES 绑定入口）：在 `NewFingerprint`/`BindPhysicalTarget` 之前执行同一组规则，错误包装 `ErrNotConfigured`。
- `Readiness`/`Preflight`/`NewChain`/`RunOnline` 复用同一 `Plan.Validate`，因此结论一致。

覆盖与结果（`TestPlan_CanonicalIdentityRejectedEverywhere`，日志 01）：

| 输入 | 结果 |
|---|---|
| `Embed.Model = "  probe-embed  "` | `ErrNotConfigured: embed.model must not have leading/trailing whitespace (declare "probe-embed")`；`Ready()=false`；执行入口零发送 |
| `Chat.Model = "probe-chat "` | 同上（`chat.model`） |
| `Similarity = "  COSINE  "` | 同上（`target.similarity`），提示 `declare "COSINE"` |
| `Similarity = "COSINE"` | `target.similarity must be lowercase (declare "cosine")` |
| `ProviderName = "OpenAI"` | `target.provider_name must be lowercase (declare "openai")` |
| 标准 `cosine` | `Ready` 就绪、三步执行成功；`NewExpectedEmbeddingFingerprint`、实际绑定、`Marshal→Parse` 的 **digest 一致** |
| 真正不匹配的 `dot` | 仍为 `ErrFingerprintMismatch`（`production expectation` 摘要比较） |

online 构建（`online_endpoint_test.go` 表新增 3 例）：`spaced_embedding_model`、`spaced_uppercase_similarity`、`uppercase_provider` 均 `Ready()=false`、`RunOnline` 在预检返回 `ErrNotConfigured`、模型与 fake 代理零命中（日志 05/06/07）。

**独立反例复跑**（复核原始源码，online overlay）：

| 日志 | 结果 |
|---|---|
| `23-review-space-model.txt` | **exit 1**：复核用例的前提 `Ready()==true` 已不成立；错误直接给出 `embed.model must not have leading/trailing whitespace` |
| `24-review-normalized-similarity.txt` | **exit 1**：同上，`target.similarity must not have leading/trailing whitespace (declare "COSINE")` |

这是“非规范输入在就绪阶段即被拒绝”的证据，不是把复核用例改写为通过。

### 1.4 问题 4：证据表述更正

- 本机 fake ES 是 `httptest.NewServer` 的**本机 HTTP 服务**（ES 客户端经本机 HTTP 读取索引元数据），**不是“纯本地对象调用”**；它不经过模型 `sendGate`，也**不证明任何真实 ES 行为**。
- 旧复现的 `SKIP` 只表示该时序本轮未观察到，**不写作独立通过**；变异测试只证明用例非空转；01–11 是实现者自测。三者均不能替代独立复核。
- 已在 `verification/stage2d-fix3-bdd3a6d-20261028/REPORT.md` 追加“第四轮后的更正”节（保留原文，不改历史记录），并同步修订 `design/stage2d-plan.md` v0.8。

## 2. 修改文件

| 文件 | 改动 |
|---|---|
| `server/core/probe/sendgate.go` | 四状态文档；`sendTrace`/`withSendTrace`/`sendTraceFrom`；闸门准入与 handoff 分别计数；`commitSend` 注释改为“不得描述为已发送” |
| `server/core/probe/chain.go` | 每次尝试注入 trace 并累加 `GateAdmissions`/`TransportCalls`；`beforeCancel` 测试钩子（生产 nil）；`beginAttempt`/`stop` 注释更新 |
| `server/core/probe/probe.go` | 包文档停止语义；`TargetSpec.Similarity` 文档；`Plan.Validate` 规范形式校验；`requireCanonical`/`requireCanonicalLower`；`StepReport` 新字段与语义 |
| `server/core/probe/fake_es.go` | 绑定入口统一拒绝非规范身份 |
| `server/core/probe/sendgate_test.go` | 文件头更正；`roundTripFunc`；窗口特征化与计数分离测试 |
| `server/core/probe/preflight_target_test.go` | 规范化/就绪/绑定/digest 回归 |
| `server/core/probe/online_endpoint_test.go` | online 表新增非规范输入三例 |
| `design/stage2d-plan.md` | v0.8：§8 批次范围、§9 表格、§9.1 待批准选项 |
| `verification/stage2d-fix3-bdd3a6d-20261028/REPORT.md` | 追加更正节（保留原文） |

## 3. 命令与结果（干净目标）

环境：Windows/amd64、Go 1.27.1、UCRT64 gcc 16.2.0（`<TOOLCHAIN>/ucrt64/bin/gcc.exe`）、`GOPROXY=off`、`GOSUMDB=off`、`GOFLAGS=`、`CGO_ENABLED=1`。完整命令见 `run-tests.ps1` / `run-overlays.ps1`（每个日志首行含实际命令与 WORKDIR）。

| 日志 | 命令（WORKDIR=`server/`） | 结果 |
|---|---|---|
| `01-default-targeted.txt` | `go test ./core/probe ./core/rerank -count=1 -v -run 'TestSendGate\|TestStepReport_CountsSeparateCommitFromSend\|TestPlan_CanonicalIdentityRejectedEverywhere\|TestPlan_TargetRulesMatchProduction\|TestPreflight_RejectsNilRerankDocumentBeforeAnySend'` | exit 0；全部 PASS（rerank 包为 `no tests to run`） |
| `02-default-full.txt` | `go test ./core/probe ./core/rerank -count=1` | exit 0 |
| `03-default-race-full.txt` | `go test -race ./core/probe -count=1` | exit 0；无 `DATA RACE` |
| `04-default-race-repeat.txt` | `go test -race ./core/probe -count=5 -run '<停止/计数/规范化>'` | exit 0；无 `DATA RACE` |
| `05-online-full.txt` | `go test -tags online ./core/probe -count=1` | exit 0 |
| `06-online-race-full.txt` | `go test -tags online -race ./core/probe -count=1` | exit 0；无 `DATA RACE` |
| `07-online-race-repeat.txt` | `go test -tags online -race ./core/probe -count=3 -run '<目标规则/授权边界>'` | exit 0 |
| `08-serial-all.txt` | `go test -p 1 ./... -count=1` | exit 0；全部包 `ok` |
| `09-build.txt` | `go build ./...` | exit 0 |
| `10-vet.txt` | `go vet ./...` | exit 0 |
| `11-vet-online.txt` | `go vet -tags online ./...` | exit 0 |

补充复现与变异：

| 日志 | 结果 |
|---|---|
| `20-review-gate-admission.txt` | exit 0；5 次运行 × 5 试验，**5/5/5/5/5 命中**（marker 之后 1 次 embedding） |
| `21-review-gate-ctx.txt` | exit 0；5 次运行 × 5 试验，**5/5/5/5/5 命中**；`context at release=<nil>` |
| `22-review-attempt-count.txt` | exit 0；`hits=0`、`Attempts=1`、`committed=1`（复核断言仍成立） |
| `23-review-space-model.txt` | **exit 1**（`Ready()=false`，`embed.model` 规范形式拒绝） |
| `24-review-normalized-similarity.txt` | **exit 1**（`Ready()=false`，`target.similarity` 规范形式拒绝） |
| `30-mutation-sendgate-boundary.txt` | **exit 1**（剥离 `commitSend` → 三角色 `RoundTrip() = <nil>`） |
| `31-mutation-sendgate-counts.txt` | **exit 1**（`gate_rejected` 观测到 `gate 1 / transport 1`，对端实际收到请求） |
| `32-mutation-canonical.txt` | **exit 1**（5 个非规范子例 `Plan.Validate()=<nil>`；绑定入口重现原始 `FINGERPRINT_CORRUPTED` 摘要不一致） |

## 4. 保留项回归（本批未削弱）

- 授权与环境变量凭据、默认构建远端隔离、远端 HTTPS、禁 userinfo：`TestOnlineBuild_*`、`TestProxyControl_EnvironmentIsHonored`、`endpoint_policy_test.go` 全部通过（05/06/07）。
- 完整预检、Rerank 共享校验与错误链：`TestPreflight_RejectsNilRerankDocumentBeforeAnySend`（默认与 online）通过。
- 预算 owner/lease、W1–W5、取消与清理：`TestW[1-5]_*`、`TestSendGate_ConcurrentStopConverges`、`TestSendGate_InFlightRequestIsNotRevokedAndNotRetried` 通过（01/02/03/04）。
- 完整 fingerprint、Qdrant 禁用及既有门禁：未修改相关代码；`vector` 全量测试在 `08` 中通过。
- 未新增映射表/DDL，未解除任何门禁，未更新验收状态。

## 5. 剩余边界与待批准事项

1. **停止语义未闭合（需用户决定）**：只保证“停止后零新闸门准入、零新重试”。已过闸门、未 handoff 的请求仍可能稍后发送且不可撤销；复核 5/5 与本批确定性用例 1 次命中均为证。选项见 `design/stage2d-plan.md` §9.1（A 接受较弱契约 / B 两阶段停止 + 有界等待 / C 串行化登记-发送）。在用户决定前不得写入验收通过。
2. **预算与发送不一一对应**：闸门拒绝仍消耗一次预算承诺（刻意保留）；`Attempts` 不可当作发送数或对端命中数。
3. **static 预检 ≠ 资源未变**：预检只判定配置自洽；真实目标在预检后是否被替换由运行期绑定/guard 负责。
4. **fake ES 的边界**：本机 `httptest` HTTP 只证明绑定/guard/身份链被真实执行，不覆盖真实 ES 行为（S-2）。
5. **未执行在线维度**：真实模型、真实 ES/Qdrant/MySQL、费用、权限与在线结果均未验证；本批零真实调用。
6. **人工句柄/测试钩子**：仅用于确定性构造时序，不代表常规触发概率，也不进入生产路径（`beforeCancel` 生产为 nil）。

## 6. 工作区与提交边界

- 仅提交本批授权文件：`server/core/probe/` 的六个文件、`design/stage2d-plan.md`、本证据目录、以及 `verification/stage2d-fix3-bdd3a6d-20261028/REPORT.md` 的追加更正。
- 不提交/不修改：`verification/independent-2d-*` 复核目录、既有 `.git-commit-msg.txt`、既有 `verification/stage2d-offline-a12af92-20261028/CLAIMS.md`。
- 不推送、不合并、不修改 PR；提交后停止，等待独立复核或用户对停止语义的决定。

---

## 7. 更正节（后续批次追加，2026-10-29）

> 本节由针对 `91c872f` 的第五批**追加**，不改动上文任何结论；更正的是上文可被外推的表述。
> 证据目录：`verification/stage2d-fix5-91c872-20261029/`。

1. **“停止后零新闸门准入、零新重试”不可外推为“零新预算提交”**。预算 `Reserve → Commit` 发生在角色适配器内部、发送闸门之前，`stop` 不参与预算账本；因此一个“已准入、尚未到闸门”的尝试横跨停止标记时，仍会完成一次 Commit，随后才被闸门拒绝（对端零命中）。本节写作时的上文 §3/§5 未区分这一点。
2. **确定性证据**：`TestStopMarkerDoesNotRetractBudgetCommit`（`server/core/probe/budget_stop_test.go`）与复核原始复现（fix5 日志 `24`，`-count=3`）均观测到 `before=0 / after=1 / modelHits=0`；`Chain.beforeCancel` 只固定时序，不代表触发概率。
3. **仍然成立的保证**：零新入口准入、零新闸门准入、零新重试；已承诺账本保守结算（`Abort` 不退款）、lease 恰好终结一次、`errors.Is(err, ErrAlreadyStopped)` 链保留。
4. **状态**：`design/stage2d-plan.md` §9.1/§9.2 已把“停止标记后零新预算提交”记为**未满足的待决边界**（预算侧参与记作 B+，未实现、需批准）；本项在用户决定前不得写作已通过。
