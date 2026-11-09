# Stage 2D S-1 第五批：第四轮独立复核问题的处理报告（针对 `91c872f`）

> **性质**：离线修复 + 证据 + 设计草案。**不构成验收，不解除门禁，不执行任何在线探针。**
> **固定起点**：`91c872fa75c5b51b33dd479cebae33aeba56aeb0`（父提交 `1e81aa406501219d4ce1ad7c2083c4819cfcf644`），分支 `codex/stage2c2-fingerprint`。
> **复核来源（只读，未修改）**：`verification/independent-2d-91c872-20261029/REPORT.md` 及其补充复现 `verification/independent-2d-91c872-counts/`、`verification/independent-2d-91c872-stopreview/`。
> **本批提交范围**：`server/core/providers/bailian/embedding.go`、`server/core/probe/bailian_identity_test.go`、`server/core/probe/budget_stop_test.go`、`design/stage2d-plan.md`（v0.9）、`design/stage2d-stop-b-plan.md`、本证据目录。**不含** `providers` 预算 API/锁序变更，**不含**所有 `verification/independent-*` 目录。

---

## 1. 结论摘要

| 复核问题 | 本批处理 | 状态 |
|---|---|---|
| P1 百炼真实注册的 embedding 角色不满足 `ProfiledEmbedRole`，`NewChain` 恒失败 | `bailian.validatedEmbedRole` 新增**防御性** `Profile()` + 编译期断言；身份门禁未放宽 | **已修复，离线验证通过** |
| P2 停止标记后仍可能新增预算承诺 | 定为**不可能在 probe 范围内闭合**（承诺发生在闸门之前、`stop` 不参与账本）；新增确定性特征化测试，如实更正过强表述 | **部分处理，边界待决**（需用户决定 A/B/B+/C） |
| P3 以方案 B 为候选的离线设计 | 交付 `design/stage2d-stop-b-plan.md`（线性化点、有界等待、超时未确认、A/C 对比、B 不闭合预算） | **设计已交付，未实现、未批准** |
| P4 证据表述 | 统一 `Attempts`=预算承诺、`GateAdmissions`/`TransportCalls` 为不同观测点、fake ES=本机 httptest HTTP、钩子只固定时序 | **已更正** |

**前置遗留的定性**：P1 的接口强制来自更早的 `2c952ce`（`common.NewEmbedding` 对 `ProfiledEmbedRole` 的 fail-closed 检查），百炼适配器自那时起就不满足该接口。它是**前置遗留阻塞**，不是固定起点 `91c872f` 引入的新回归；但因为它直接阻塞本批 S-1 百炼方案的真实构造路径，本批必须修复。基线复现（日志 `20`）与修复后同一复核源码（日志 `22`）构成对照证据。

---

## 2. P1：百炼 Embedding 身份接口阻塞

### 2.1 根因

1. `server/core/chain.go` 的 `init` 注册了百炼 builder，`Registry.GetEmbedder()` 在百炼配置下返回**真实类型** `*bailian.validatedEmbedRole`（日志 `01`：`provider=bailian role=*bailian.validatedEmbedRole`）。
2. `common.NewEmbedding` 在构造期强制断言 `providers.ProfiledEmbedRole`（`EmbedRole` + `Profile() ProviderProfile`），缺失即返回 `ErrEmbeddingIdentityMismatch`，错误文本为 `... does not expose verifiable provider identity`。
3. 该类型只实现了 `Dimensions()`/`ModelID()`，没有 `Profile()`；`server/core/providers/roles.go` 的注释却声称它满足该接口——**注释与实现不一致**，是本轮复核在 `counts.log` 三次 `exit 1` 的直接原因。

### 2.2 修复

`server/core/providers/bailian/embedding.go`：

- 增加编译期断言 `_ providers.ProfiledEmbedRole = (*validatedEmbedRole)(nil)`，让"注释声称满足接口"变成编译期事实；
- 新增 `Profile()`，返回 `*v.profile.Clone()`：`ProviderProfile` 是值类型，`Clone()` 同时深拷贝 `KeyOptions`，因此调用方拿到的是**防御性副本**，无法修改角色内部状态；
- `nil` 接收者/`nil` profile 返回零值 Profile（不会 panic，也不会伪造身份）；
- 身份来源就是 `BuildEmbed` 收到的同一份 `cfg.Profile`（`adapter.go` 里 `p.Clone()` 后的副本），所以"报告的 provider/模型/维度"与实际请求、维度校验使用的是同一组值。

**门禁未放宽**：没有为缺失身份的其它角色提供任何旁路，`common.NewEmbedding` 的比较顺序（profile 存在性 → provider → 模型 → 维度）与错误链完全未改。日志 `30` 用变异把 `Profile()` 变成零身份后，复核原始用例立刻重新失败（`embedding role reports empty provider name`）。

### 2.3 测试与证据（真实注册路径，非桩）

`server/core/probe/bailian_identity_test.go`（`package probe`，与生产同包，直接走 `NewChain`）：

| 用例 | 覆盖点 | 关键实测（日志 `01`/`04`/`05`/`06`/`07`） |
|---|---|---|
| `TestProviderRegistrationBuildsProductionChain/bailian` | 真实注册 + Registry + 本地 fake ES 绑定 + 本地 fake HTTP，三步全跑通 | `role=*bailian.validatedEmbedRole identity=probe-embed/8 counts=1/1/1 committed=3 inFlight=0`；chat/embed/rerank 命中各 1 |
| 同上 `/openai` | 共享生产路径对照 | `role=*providers.budgetEmbedRole identity=probe-embed/8 counts=1/1/1 committed=3 inFlight=0` |
| `TestBailianEmbeddingProfileIsDefensiveCopy` | 篡改返回值的 provider/模型/版本/维度/`KeyOptions`，含两次快照之间不共享 map | 角色自身与后续快照均保持 `bailian/probe-embed/8`、`KeyOptions["..."]="probe"` |
| `TestBailianEmbeddingIdentityMismatchFailsClosed`（3 子例） | 不同 provider / 模型 / 维度在构造期拒绝，零模型请求 | `provider name: role="bailian" expected="openai"`；`model: role="probe-embed" expected="probe-embed-alt"`；`profile dims=8 config dims=16`；`ErrEmbeddingIdentityMismatch` 链保留，embed 命中 0 |
| `TestBailianEmbeddingCancelAndConcurrentBudget` | 预算 owner/lease、取消、并发 | 取消：`committed=1 inputUsed=4 inFlight=0`、`Attempts=1`、命中 1（保守结算，不退款）；并发 4：`committed=4 inFlight=0`、命中 4、预留归零 |

**补充复现对照**（复核方原始源码，未改一行）：

| 日志 | 场景 | 结果 |
|---|---|---|
| `20-baseline-bailian-counts.txt` | 固定起点 worktree（`.review-stage2c1-2dc0b82`，detached HEAD = `91c872f`）+ 复核 `overlay.json`，`-count=3` | **exit 1**，三次均为 `embedding role *bailian.validatedEmbedRole does not expose verifiable provider identity` |
| `21-baseline-openai-counts.txt` | 同一基线 worktree + 复核 openai 对照，`-count=3` | exit 0，三次 `committed/gate/transport=1/1/1` |
| `22-fixed-bailian-counts.txt` | 同一复核 bailian 源码 overlay 到本批工作区，`-count=3` | **exit 0**，三次 `chat/embed/rerank` 均 `1/1/1` |
| `23-fixed-openai-counts.txt` | 修复后 openai 对照，`-count=3` | exit 0，三次 `1/1/1` |
| `30-mutation-bailian-zero-profile.txt` | 变异：`Profile()` 返回零身份 + 复核 bailian 用例 | **exit 1**，`identity or dimensions mismatch ... embedding role reports empty provider name` |
| `31-mutation-bailian-shallow-profile.txt` | 变异：`Profile()` 返回 `*v.profile`（浅拷贝，共享 `KeyOptions`） | **exit 1**，`bailian_identity_test.go:185: second snapshot KeyOptions = "tampered" ... (internal map leaked)` |

`20`/`21` 证明基线失败**只**来自百炼身份接口（共享路径本身正常）；`22`/`23` 证明修复后同一反例转为通过；`30`/`31` 证明这些用例与断言不是空转。

---

## 3. P2：停止标记后的预算承诺（事实、能保证与不能保证）

### 3.1 事实

预算 `Reserve → Commit` 发生在角色适配器内部（`budgetEmbedRole` / `bailian.validatedEmbedRole`），位置在发送闸门 `commitSend` **之前**；`Chain.stop` 只发布停止标记并取消在途句柄的 ctx，**不参与预算账本**。因此"已准入（`beginAttempt`）、尚未到闸门"的尝试横跨停止标记时，仍会完成一次 Commit，随后才被闸门拒绝。

复核的 `beforeCancel` 钩子只用于把"标记已发布"与"取消已施加"确定性地分开，固定的是时序，**不代表常规负载的触发概率**；模型零命中也不等于预算零承诺。

### 3.2 本批新增的确定性特征化测试

`server/core/probe/budget_stop_test.go::TestStopMarkerDoesNotRetractBudgetCommit`（`-count=3` 与 `-race -count=5` 均通过）：

```
OBSERVED commit after stop marker: before=0 after=1 gate=0 transport=0 hits=0 inputUsed=5 inFlight=0;
NOT a guarantee of zero commits after the marker
```

同时断言的不变量（保守结算，不允许为了让计数好看而破坏）：

- `CommittedAttempts` 不回落（`Abort` 已 commit 不退款）；
- `InFlight` / `InputReserved` / `OutputReserved` 归零（lease 恰好终结一次，无泄漏）；
- `InputUsed > 0`（保守结算而非静默退款）；
- `errors.Is(err, ErrAlreadyStopped)` 保留（错误链不被吞掉）；
- 闸门/transport 均为 0、对端 0 命中（承诺 ≠ 发送）。

### 3.3 能保证 / 不能保证

- **能保证**：停止标记之后零新**入口准入**、零新**闸门准入**、零新重试；未过闸门的已准入尝试返回 `ErrAlreadyStopped` 且对端零命中；已承诺账本保守结算、lease 收敛。
- **不能保证**：字面"停止标记之后零新预算提交"。这是因为承诺点在闸门之前，且 `stop` 与预算账本之间没有全序关系；把这条做成保证需要 `providers` 预算侧参与（`pb.mu` 内咨询一个与 `stop` 同全序的判据），属**已批准契约之外的变更**，本批不实现。
- 本批**没有**放宽任何已批准契约，也**没有**把较弱语义写成通过。

### 3.4 文档与证据的更正

- `design/stage2d-plan.md` §9.1 的"已能保证"下新增注记：该项**不**包含"零新预算提交"；新增 §9.2 专节记录事实、能保证/不能保证与 B+ 闭合路径；§9 表格新增"发送证据计数与预算承诺区分（v0.9 P2）"行，状态记为"部分处理，边界待决"。
- `verification/stage2d-fix4-1e81aa4-20261028/REPORT.md` 末尾追加**更正节**（不改动原文结论）：说明"停止后零新闸门准入、零新重试"不可外推为"零新预算提交"。
- 本目录 `results.txt` 与本节同样区分"承诺/闸门/transport/对端"四个观测点。

---

## 4. P3：方案 B 的离线设计（`design/stage2d-stop-b-plan.md` v0.1，未实现、未批准）

要点摘录（细节见该文档）：

- **三个不同的线性化点**：closing 标记 = `stop` 在 `c.mu` 写侧写入 `stopped=true`；handoff = 调用 `g.inner.RoundTrip` 的第一条指令；停止成功返回 = 观察到"已过闸门、未 handoff"的集合为空。
- **四状态不可混淆**：入口已准入（`beginAttempt`）/ 已通过闸门（`commitSend` 读侧，与 `stop` 写侧同一 RWMutex 全序）/ 已交给 transport（不可撤销）/ 对端已收到（不可观测，不作承诺）。
- **`RoundTrip` 返回 ≠ 发送开始**：它可能阻塞在等待响应头；B 只把 handoff 作为可等待事件，绝不把"请求完成"纳入停止条件，因此不产生无界等待。
- **有界等待**：`handoffPending atomic.Int64` + `StopWaitLimit` 轮询；**不得持 `c.mu` 等待**、**不得无界等待**、**不得用睡眠放大保证**。
- **超时"未确认"**：返回 `StopResult{Confirmed:false, Pending:n, Waited, MarkerAt}` 与 `ErrStopUnconfirmed`；调用方责任是当作未确认处理（不得据此宣称零新发送、不得回滚已承诺预算、不得据此关闭证据）。
- **B 不闭合预算承诺**：B 只收紧"闸门 → handoff"，`Reserve/Commit` 仍在其之前；要闭合需 B+（改 `providers` 预算 API/锁序，禁止 `pb.mu → c.mu` 反向锁序），需批准。
- **A/B/C 比较**：A 只改文档（诚实但保证最弱）；B 可用原子计数取得"停止返回后零新 handoff"，无吞吐代价；C 若把 `RoundTrip` 纳入临界区则违反有界要求，若只覆盖登记则等价于 B 但更贵。
- **明确不承诺**：不撤销已发送请求、不承诺服务端零计费、不在没有证据时声称"标记后物理零发送"。

该设计**没有**修改任何代码、门禁或验收状态；采用与否、`StopWaitLimit` 取值、`StopResult`/`ErrStopUnconfirmed` 的可见性、是否纳入 B+ 等 5 个开放问题均等待用户决定（见该文档 §7）。

---

## 5. P4：证据表述规范（本批统一遵守）

| 概念 | 本批表述 |
|---|---|
| `Attempts` | 预算**已承诺**次数（含受控重试），不是发送数、不是对端命中数 |
| `GateAdmissions` | 通过发送闸门的次数（停止标记后恒为 0） |
| `TransportCalls` | 开始调用底层 `RoundTripper` 的次数，**不证明网络字节或对端已收到** |
| fake ES | 本机 `httptest.NewServer` 的 HTTP 服务（ES 客户端经本机 HTTP 读取索引元数据），**不是**真实 ES，也不经模型 `sendGate` |
| 测试钩子 | `Chain.beforeCancel`、`sendGate.beforeCommit` 仅测试用同步点（生产 nil），只固定时序、不代表触发概率 |
| 复核 SKIP / 变异 / 实现者自测 | 一律不写作"独立复核通过"；本批的 `20`–`24`、`30`/`31` 明确标注为"补充复现/变异"，不是独立复核 |

---

## 6. 验证矩阵（完整命令见各日志首行，汇总见 `results.txt`）

环境：Windows/amd64、Go 1.27.1、UCRT64 gcc 16.2.0、`GOPROXY=off`、`GOSUMDB=off`、`CGO_ENABLED=1`、`GOFLAGS=''`；WORKDIR `server/`；全部为本机 fake/httptest，**无在线探针、无真实模型/ES/Qdrant/MySQL/互联网**。

**干净目标（本批代码与测试）**：`01`–`04` 默认构建、`05`–`07` online 标签（对端仍为本机 fake）、`08` 串行全量、`09` build、`10`/`11` vet（含 online）。全部 `exit 0`；无 `DATA RACE`；无 `--- SKIP`；`04` 身份/预算/取消/停止用例各重复 5 次，`07` 各重复 3 次。

**补充复现与变异（复核原始源码，未修改）**：`20` 基线 bailian `exit 1`（复现阻塞）、`21` 基线 openai `exit 0`、`22` 修复后 bailian `exit 0`、`23` 修复后 openai `exit 0`、`24` 复核 beforeCancel 预算复现 `exit 0` 且 `before=0/after=1/modelHits=0`（三次）、`30`/`31` 变异 `exit 1`。

**偏差与处理（如实记录）**：

1. Windows 上 `go test` 偶发 `probe.test.exe: The process cannot access the file because it is being used by another process`（杀毒/索引占用临时二进制）。脚本对本机可识别的**瞬时锁**做有限重试（≤12 次、每次 3s），并在日志里逐次写 `ATTEMPT n: transient windows binary lock, retrying`；真实断言失败不重试。当前日志中 `02`、`20`、`22`、`23`、`24`、`30` 出现过该瞬时锁并被重试消化，最终退出码与断言结果均在日志末行。
2. 本目录首轮的 `30` 曾写成两个 `-overlay` 参数——Go 只取最后一个，导致变异不生效、用例空转 `exit 0`。已改为把变异与复核用例合并进**同一张** overlay 映射表（`overlay-mutation-zero-profile-with-review.json`）后重跑，得 `exit 1` 与预期的 `reports empty provider name`。首轮日志已被覆盖，此偏差保留在此说明。

---

## 7. 剩余边界与待决定事项

1. **停止语义仍未闭合（需用户决定）**：只保证"停止后零新闸门准入、零新重试"；"已过闸门、未 handoff"的请求仍可能在标记之后开始传输（旧复核 5/5 与本批 `TestSendGate_PostGatePreTransportWindowIsNotClosed` 均为证）。选项 A/B/C 见 `design/stage2d-plan.md` §9.1，B 的细节见 `design/stage2d-stop-b-plan.md`。
2. **"停止标记后零新预算提交"未满足（需用户决定）**：事实见 §3；闭合需 `providers` 预算侧参与（B+），属已批准契约变更。
3. **不承诺**：撤销已发送请求、服务端零计费、对端零接收；`TransportCalls` 不证明网络字节。
4. **未做**：真实模型/ES/Qdrant/MySQL 验证（S-2/S-3）、在线探针（需 U-1/U-3/U-5 与在线授权）、验收状态更新、PR 推送/合并。

## 8. 工作区状态（本批结束时）

- 分支 `codex/stage2c2-fingerprint`，父提交 `91c872f`；本批只提交上表列出的授权文件（含本证据目录；overlay `*.json` 受仓库 `.gitignore` 的 `*.json` 规则影响，需 `git add -f`）。
- **保留不动**：`.git-commit-msg.txt`、`verification/stage2d-offline-a12af92-20261028/CLAIMS.md` 的既有改动；全部 `verification/independent-*` 复核目录（不纳入提交）；既有 worktree（`.review-stage2c1-*`）只读使用。
- 未推送、未合并、未修改 PR；未解除门禁；未更新 `PROJECT_PROGRESS.md` / `CHANGES.md` 的验收表述。
