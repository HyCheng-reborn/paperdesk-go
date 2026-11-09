# Stage 2D S-1 第三轮修复（v0.7）— 证据报告

- 固定起点（被复核提交）：`bdd3a6dedb4a0284e1db8925de489c80d5aba3d3`
- 父提交（本批的父）：`3aea024981d5137e6e64a6084ff99fcd7cf53871`；被复核提交的父提交：`3aea024981d5137e6e64a6084ff99fcd7cf53871`
- 分支：`codex/stage2c2-fingerprint`
- 被复核报告：`verification/independent-2d-bdd3a6d-20261028/REPORT.md`
- 补充复现（只读复用，未修改）：`verification/independent-2d-bdd3a6-concurrency/`、`verification/independent-2d-bdd3a6-auth/`
- 本批性质：离线修复 + 离线回归。**未执行任何在线探针**；所有对端均为本机 fake 模型 / fake ES / fake 代理（httptest），未访问互联网，未连接真实模型、ES、Qdrant、MySQL，未使用真实凭据。
- 本报告与其日志随本批提交入库；实际提交 SHA 由批次完成后的说明给出（不在本文件内回填，避免自引用）。

## 1. 三项问题与修复对照

| # | 复核问题 | 根因 | 修复 | 回归测试（实际探针入口） |
|---|---|---|---|---|
| P1 | 停止标记后的发送窗口：`beginAttempt` 登记句柄后，`stop` 在锁外逐个取消；已准入但尚未发送的步骤仍可能在 `stopped=true` 之后发出请求 | 入口准入（登记句柄）与实际发送（HTTP RoundTrip）之间没有同步关系；停止标记发布与发送准入不是一个全序 | 新增 `probe/sendgate.go`：所有模型端点共用同一个 `providers.Transport`，在其上包一层 `sendGate`；`RoundTrip` 在真正发送前调用 `(*Chain).commitSend`，在 `c.mu` 读侧检查 `stopped`，而 `stop` 在写侧发布该标记（`sync.RWMutex` 写者优先 ⇒ 全序）。明确区分“已准入”（`beginAttempt`，允许开始）与“已经在途”（已过闸门）：停止后未过闸门的尝试返回 `ErrAlreadyStopped`、零字节发出；在途请求不声称可撤销，只取消 ctx 且不会被重试 | `TestSendGate_StopMarkerRefusesAdmittedButUnsentAttempt`、`TestSendGate_TransportBoundaryRefusesAfterStopMarker`、`TestSendGate_InFlightRequestIsNotRevokedAndNotRetried`、`TestSendGate_OriginalReviewStopWindowShapeHasNoSend`、`TestSendGate_ConcurrentStopConverges` |
| P2 | 完整预检遗漏 nil Rerank 文档：`Ready()==true`，合法 Chat 先发送，Rerank 才在自己的生产入口拒绝 | `Step.validateAgainst` 只做探针侧字段检查，没有复用生产 `Rerank` 的纯输入校验 | 从生产 `rerank.Rerank` 提取公开纯函数 `rerank.ValidateInput`（空文档 / nil 文档 / 非法 top_k），生产 `Rerank` 与预检共用；预检错误链同时保留 `ErrEmptyStep` 与 `rerank.ErrInvalidInput`，并保留探针自己的 `top_k <= len(docs)` 约束 | `TestPreflight_RejectsNilRerankDocumentBeforeAnySend`（Preflight / Readiness / Run / RunStep 四个入口）、`TestOnlineBuild_NilRerankDocumentRejectedBeforeAnySend` |
| P2 | 非法目标与不匹配 similarity 被误报为就绪：`bad,*` 与 `similarity=dot` 时 `Ready()==true`，执行时才在物理名绑定 / 完整指纹比较处失败 | `Plan.Validate` 只检查字段非空，未复用生产的具体物理名规则与完整期望 fingerprint 规则 | `Plan.Validate` 增加 `vector.ValidatePhysicalName`；新增 `validateTargetIdentity`：用 `vector.NewExpectedEmbeddingFingerprint`（生产期望：当前格式版本 + cosine）与 `vector.NewFingerprint(... declared similarity)` 构造，再按 `Fingerprint.Equal` 全字段比较，失败时保留 `ErrNotConfigured` 与 `vector.ErrFingerprintMismatch` 并给出双方 Digest。Readiness 的 `plan` 项与 `RunOnline`/`NewChain` 同源拒绝 | `TestPlan_TargetRulesMatchProduction`（`bad,*`、多目标、`dot`、非法 similarity 集合、cosine 正例、fake 目标拒绝通配符）、`TestOnlineBuild_TargetRulesReadinessMatchesExecution` |

### 1.1 关键语义（如实记录，不作扩大解释）

- **停止的可观察线性化点**：`stop` 在写锁内设置 `stopped` 与首个脱敏摘要；`commitSend` 在读锁内观察它。`stop` 返回时标记必已发布；此后任何尚未通过闸门的尝试都不会产生网络 I/O。
- **已准入 ≠ 已经在途**：`beginAttempt` 只登记“允许开始”，不代表已发送；只有通过 `sendGate` 的请求才进入“在途”。
- **在途不可撤销**：已交给 HTTP 栈的请求只能取消其 ctx，服务端是否收到不由客户端保证；测试对此只断言“服务端命中数不增加、不重试”，不声称撤销。
- **预算账本**：闸门拒绝发生在 `lease.Commit()` 之后，`CommittedAttempts` 单调 +1，`Abort` 不回退（reserved → used 保守结算）。因此“停止后零发送”**不等于**“零预算占用”；测试显式断言 `CommittedAttempts==1` 且预留归零。
- **闸门覆盖范围**：闸门只包模型端点（Chat / Embedding / Rerank 共用同一 transport）。本机 fake ES 使用自己的 client，不经闸门；停止对 ES 读取的作用仅来自 ctx 取消，不构成本批的“零发送”承诺。
- **静态预检边界**：目标规则只判定**配置自洽性**。真实资源在预检之后是否被替换/变更，不由静态预检保证（运行期由 target binding / guard 重新读取）。

## 2. 逐项实测证据

### 2.1 P1（停止标记后的发送窗口）

`01-default-targeted.txt`（exit 0，18 个顶层用例 / 30 个含子项 PASS）中与 P1 相关的用例：

| 用例 | 断言要点 | 实测 |
|---|---|---|
| `TestSendGate_StopMarkerRefusesAdmittedButUnsentAttempt` | 用 `sendGate.beforeCommit` 把一次已准入的 embedding 尝试固定在“已准入、未在途”；发布停止标记后放行 | 步骤错误 `ErrAlreadyStopped`；`EmbedHits=0`；`InFlight=0`；`InputReserved/OutputReserved=0`；`CommittedAttempts=1`；停止后 `RunStep` 仍拒绝且零命中 |
| `TestSendGate_TransportBoundaryRefusesAfterStopMarker` | 停止返回后，用**未取消**的请求上下文直接调用闸门 `RoundTrip` | `errors.Is(err, ErrAlreadyStopped)`；`EmbedHits=0`（拒绝早于任何网络 I/O） |
| `TestSendGate_InFlightRequestIsNotRevokedAndNotRetried` | Chat 已到服务端（`ChatGate` 阻塞）后停止；`MaxAttempts=2` + `RetryOnTransportError=true` | 放行后步骤返回 `ErrAlreadyStopped`；`ChatHits` 保持 1（未撤销、无新发送、无重试）；`InFlight=0` |
| `TestSendGate_OriginalReviewStopWindowShapeHasNoSend` | 复用复核形状：fake ES guard 阻塞一个已准入步骤 + 20,000 个人工 `beginAttempt` 句柄放大“停止 → 取消”窗口，观察到停止标记后放行 | 5 次迭代，每次 `EmbedHits=0` |
| `TestSendGate_ConcurrentStopConverges` | 8 个并发 chat/embed 入口 + 停止（`-race`） | 全部收敛；`InFlight=0`；预留归零；日志无 `DATA RACE` |

`04-default-race-repeat.txt`：上述用例 + 预检/预算/guard 用例在 UCRT64 `-race -count=5` 下 exit 0，日志无 `DATA RACE`。

原补充复现复跑（复核方源码未修改，详见 §3）：

- `20-repro-stop-window-20k.txt`：`-race -count=5`，**exit 0**；每次运行 5 次迭代，命中数全部为 0；最终 `--- SKIP`（`timing-dependent window not observed`）。合计 25 个样本、0 命中、5 次 Skip。
- `21-repro-stop-window-100k.txt`：`-race -count=1`，**exit 0**；5 次迭代命中数全部为 0；`--- SKIP`。

**不能仅凭 SKIP 声称窗口关闭**，因此追加了变异证据（§4）：剥离 `sendGate.commitSend` 后，同一 20k 形状在 `30c` 中 3/3 次复现“停止后仍有 1 次 embedding 命中”。这说明该形状确实对该缺陷敏感，而修复版在同一形状下为 0 命中。

### 2.2 P2（nil Rerank 文档）

`01-default-targeted.txt` 中 `TestPreflight_RejectsNilRerankDocumentBeforeAnySend`（4 个子项全部 PASS）：

- Preflight：`errors.Is(err, ErrEmptyStep)` 且 `errors.Is(err, rerank.ErrInvalidInput)`；文本含 `nil document at index 0`。
- Readiness：`steps` 项 `OK=false`，detail 含同一原因；`Ready()==false`。
- Run（合法 Chat 在前）：返回同一错误链，`len(report.Steps)==0`，三模型端点命中合计 0。
- RunStep（直接执行 Rerank）：同一错误链，零命中。

`05-online-full.txt` 中 `TestOnlineBuild_NilRerankDocumentRejectedBeforeAnySend`（PASS）：`RunOnline` 同源拒绝、零步骤报告、三端点 0 命中、fake 代理增量 0。

原补充复现复跑：`22-repro-nil-rerank-doc.txt`（默认构建）与 `23-repro-auth-nil-doc.txt`（online + race）均在**第一次断言**处 FAIL、**exit 1**，错误为 `steps[1]: probe: empty probe step: rerank: invalid request input: nil document at index 0`——反例的“缺陷存在”期望不再成立。

### 2.3 P2（目标配置误报就绪）

`01-default-targeted.txt` 中 `TestPlan_TargetRulesMatchProduction`（6 个子项全部 PASS）：

| 子项 | 断言 | 实测 |
|---|---|---|
| `wildcard_physical_name`（`bad,*`） | `Plan.Validate` / Readiness `plan` / `NewChain` 三处 `vector.ErrPhysicalTargetRequired` | PASS，零命中 |
| `multiple_physical_targets`（`index-a,index-b`） | 同上 | PASS，零命中 |
| `similarity_dot` | 三处 `vector.ErrFingerprintMismatch`；detail 含 `production expectation` 与双方 Digest | PASS，零命中 |
| `similarity_not_in_allowed_set`（`hamming`） | `ErrNotConfigured`，detail 含 `similarity` | PASS，零命中 |
| `cosine_target_ready_and_executable` | plan/steps/credentials 三项 OK；`Run` 完成 3 步；命中 1/1/1 | PASS |
| `local_fake_target_rejects_wildcard_name` | 生产绑定规则本身拒绝 `bad,*` | PASS |

`05-online-full.txt` 中 `TestOnlineBuild_TargetRulesReadinessMatchesExecution`（4 个子项 PASS）：`invalid_physical_name` / `multiple_physical_targets` / `mismatched_similarity` 均 `Ready()==false`、`RunOnline` 同源拒绝、零模型命中、代理增量 0；`cosine_ready_and_executable` 就绪且 3 步命中 1/1/1。

原补充复现复跑：

- `24-repro-auth-invalid-target.txt`：**exit 1**，`Ready()` 为 false，plan detail 为 `... target.index_name: vector: a single concrete physical target is required: "bad,*"`。
- `25-repro-auth-mismatched-similarity.txt`：**exit 1**，plan detail 为 `FINGERPRINT_MISMATCH: declared target sha256:37127a66... does not match the production expectation sha256:9835fac0...`。

## 3. 补充复现的复跑方式（原证据只读）

复核方当时在 detached worktree `<WORKSPACE>/.review-stage2c1-2dc0b82` 内用 `-overlay` 把反例映射进包。本次**不修改**该 worktree 与任何复核目录，而是在真实工作区用新的 overlay JSON 将同一份未修改的反例源码映射进 `server/core/probe` 包：

| 复跑 | overlay key（虚拟/既有文件） | value（复核源码，未修改） |
|---|---|---|
| 20 | `server/core/probe/independent_review_stop_test.go` | `independent-2d-bdd3a6-concurrency/review_stop_20k_test.go` |
| 21 | 同上 | `independent-2d-bdd3a6-concurrency/review_stop_test.go`（100,000 句柄） |
| 22 | `server/core/probe/independent_nil_doc_test.go` | `independent-2d-bdd3a6-concurrency/nil_doc_test.go` |
| 23–25 | `server/core/probe/independent_auth_bdd_test.go` | `independent-2d-bdd3a6-auth/review_test.go`（`//go:build online`） |

数值口径：20/21 的“人工句柄”只是把“停止标记 → 取消全部句柄”的耗时放大以构造竞争窗口，**不代表一般负载下的触发概率**；它们的价值是证明该缺陷不是理论问题，以及修复后同一形状不再命中。

## 4. 变异验证（证明用例不是空转）

为防止“用例 PASS 但根本没测到”，在证据目录内保留三份变异副本，仅通过 overlay 作用于测试编译，不修改仓库源码：

| 变异日志 | 剥离的修复 | 期望 | 实测 |
|---|---|---|---|
| `30-mutation-sendgate-boundary.txt` | `sendGate.RoundTrip` 中的 `commitSend` 调用 | 传输边界用例必须失败 | **FAIL / exit 1**：`RoundTrip() = <nil>, want ErrAlreadyStopped` |
| `30b-mutation-sendgate-admitted-unsent.txt` | 同上 | 端到端固定窗口用例在剥离闸门后仍可能因 ctx 已被 `stop` 取消而未发送 | **PASS / exit 0**（如实记录：该用例不能单独区分“有闸门”与“仅靠 ctx 取消”；非空转证明依赖上一条传输边界用例） |
| `30c-mutation-sendgate-review-shape.txt` | 同上 | 原 20k 复核形状应复现命中 | **FAIL / exit 1，3/3 次**：iteration 0/1/3 各命中 1 次 |
| `31-mutation-preflight.txt` | `validateAgainst` 中的 `rerank.ValidateInput` | nil 文档用例必须失败 | **FAIL / exit 1**：Preflight 返回 nil、Readiness `steps` OK、Run/RunStep 到生产 Rerank 才拒绝 |
| `32-mutation-target.txt` | `Plan.Validate` 中的 `validateTargetIdentity` | 目标规则用例必须失败 | **FAIL / exit 1**：`similarity_dot` 与 `similarity_not_in_allowed_set` 的 `Plan.Validate() = <nil>`；通配符子项仍失败于物理名校验（该变异未剥离它） |

## 5. 命令与结果（干净目标测试，默认与 online 分开）

环境：Windows/amd64、Go 1.27.1、UCRT64 gcc 16.2.0（`<TOOLCHAIN>/ucrt64/bin/gcc.exe`）、`GOPROXY=off`、`GOSUMDB=off`、`GOFLAGS=`。完整命令另见 `run-tests.ps1` / `run-overlays.ps1`。

| 日志 | 命令（`WORKDIR=server/`） | 结果 |
|---|---|---|
| `01-default-targeted.txt` | `go test -p 1 -count=1 -v -run '<停止/预检/目标/预算/guard/重定向/脱敏>' ./core/probe/` | exit 0；18 顶层 / 30 含子项 PASS |
| `02-default-probe-rerank-full.txt` | `go test -p 1 -count=1 ./core/probe/... ./core/rerank/...` | exit 0 |
| `03-default-race-full.txt` | `go test -race -p 1 -count=1 ./core/probe/... ./core/rerank/...` | exit 0；无 `DATA RACE` |
| `04-default-race-repeat.txt` | `go test -race -count=5 -run '<停止竞争/预检/预算/guard>' ./core/probe/` | exit 0；无 `DATA RACE` |
| `05-online-full.txt` | `go test -tags online -p 1 -count=1 ./core/probe/...` | exit 0 |
| `06-online-race-full.txt` | `go test -tags online -race -p 1 -count=1 ./core/probe/...` | exit 0；无 `DATA RACE` |
| `07-online-race-repeat.txt` | `go test -tags online -race -count=5 -run 'TestOnlineBuild\|TestSendGate\|TestW[1-5]_' ./core/probe/` | exit 0；无 `DATA RACE` |
| `08-serial-all.txt` | `go test -p 1 -count=1 ./...` | exit 0；18 个包全部 `ok` |
| `09-build.txt` | `go build ./...` | exit 0 |
| `10-vet.txt` | `go vet ./...` | exit 0 |
| `11-vet-online.txt` | `go vet -tags online ./core/probe/...` | exit 0 |

补充复现与变异（同样 `WORKDIR=server/`，overlay 路径见日志首行）：

| 日志 | 结果 |
|---|---|
| `20-repro-stop-window-20k.txt` | exit 0；25 样本 0 命中；5 次 SKIP |
| `21-repro-stop-window-100k.txt` | exit 0；5 样本 0 命中；1 次 SKIP |
| `22-repro-nil-rerank-doc.txt` | **exit 1**（Preflight 已拒绝，缺陷不再复现） |
| `23-repro-auth-nil-doc.txt` | **exit 1**（Preflight 已拒绝） |
| `24-repro-auth-invalid-target.txt` | **exit 1**（Readiness 已拒绝 `bad,*`） |
| `25-repro-auth-mismatched-similarity.txt` | **exit 1**（Readiness 已拒绝 `dot`） |
| `30*` / `31` / `32` | 见 §4；除 `30b` 外均 exit 1 |

## 6. 保留项回归（本批未削弱）

- online 远端授权与环境变量凭据边界、默认构建远端隔离、远端 HTTPS：`chain_test.go`、`endpoint_policy_test.go`、`online_endpoint_test.go` 原有用例全部通过（02/05）。
- 带版本角色拒绝、空凭据一致性、Plan 防御性复制：`preflight_roles_test.go`、`credentials_test.go`、`plan_clone_test.go` 全部通过（02/05）。
- 重定向不重试、证据脱敏与 `errors.Is` 链：`TestRedirect_ProhibitedIsNotRetried`、`TestStopReasonIsSanitized` 等通过（01）。
- W1–W5、预算 owner/lease、取消与资源释放、fake ES 完整绑定：`TestW[1-5]_*`、`TestProbeChain_GuardRejectsReplacedTarget`、`TestProbeChain_ProductionBindingAndScope` 通过（01/02）。
- Qdrant 禁用与现有门禁：未修改相关代码与配置；全量测试通过（08）。

## 7. 剩余边界（不在本批声称已解决）

1. **在途请求不可撤销**：停止后已过闸门的请求可能已到达服务端；本批只保证“不再有新发送/新重试”，并通过服务端命中数不增加来验证。
2. **预算占用**：闸门拒绝发生在预算 `Commit` 之后，`CommittedAttempts` 会单调 +1；零发送不等于零预算消耗，这是刻意保留的保守账本语义。
3. **fake ES 不经闸门**：ES 侧的读取只受 ctx 取消影响，不在“零发送”承诺范围内；真实 ES 行为属 S-2，未验证。
4. **静态预检 ≠ 资源未变**：预检只判定配置自洽；真实目标在预检之后被替换/变更的风险仍由运行期绑定与 guard 负责。
5. **复现概率**：20k/100k 人工句柄用于放大窗口，不代表一般负载触发概率；修复版 0 命中 + 变异版 3/3 命中共同说明形状敏感性，但仍是本机 fake 条件。
6. **未执行在线维度**：真实模型、真实 ES/Qdrant/MySQL、费用、权限与在线结果均未验证；本批零真实调用。
7. **未新增映射表或 DDL**，未解除任何门禁，未更新验收状态。

## 8. 本批工作区与提交边界

提交前 `git status`（`00-environment.txt`）：

- 修改：`.git-commit-msg.txt`（既有，不纳入）、`server/core/probe/{chain.go,chain_test.go,fake_es.go,online_endpoint_test.go,preflight.go,probe.go}`、`server/core/rerank/rerank.go`、`design/stage2d-plan.md`、`verification/stage2d-offline-a12af92-20261028/CLAIMS.md`（既有改动，不纳入）。
- 新增（本批）：`server/core/probe/{sendgate.go,sendgate_test.go,preflight_target_test.go}`、`verification/stage2d-fix3-bdd3a6d-20261028/`。
- 未跟踪的独立复核目录（`verification/independent-*`）全部保持原样、不纳入提交。

只提交本批授权文件；不清理既有工作区、worktree 与 verification 证据；不推送、不合并、不修改 PR。提交后停止，等待独立复核。

---

## 9. 第四轮（v0.8）后的更正（追加，保留上文历史记录）

本节由 `verification/stage2d-fix4-1e81aa4-20261028/` 批次追加。上文保留当时的记录，不再作为最终结论；以本节的更正为准。

1. **停止语义（上文 §7.1）过强**：可保证的是“停止标记之后**零新闸门准入、零新重试**；已准入未过闸门的尝试零字节发出”。已过闸门、尚未交给 transport 的请求仍可能在标记之后发出且不可撤销（第 3 轮复核以 100,000 人工句柄 5/5 观测到命中 1；本批 overlay 复跑仍为 5/5，`context at release=<nil>`，并用确定性钩子用例复现 hits=1）。该窗口**未闭合**，待用户在 `design/stage2d-plan.md` §9.1 中决定；不得写作“零新发送已通过”。
2. **fake ES 表述（上文 §7.3）更正**：本机 fake ES 是 `httptest.NewServer` 的本机 **HTTP** 服务（ES 客户端经本机 HTTP 读取索引元数据），不是“纯本地对象调用”；它不经模型 `sendGate`，也不证明任何真实 ES 行为。
3. **SKIP / 变异 / 自测的定位**：20/21 的 `SKIP` 只表示该时序本轮未观察到，**不是独立通过**；30/31/32 变异只证明用例非空转；01–11 为实现者自测。三者都不能替代独立复核。
4. **计数表述（上文 §7.2）补充**：`StepReport.Attempts` 是预算已承诺次数，不是实际发送次数；实际发送看 `GateAdmissions`/`TransportCalls`，对端收到只能由 fake/代理计数观测（v0.8 新增字段）。

