# Stage 2D S-1 停止语义：方案 B 离线设计（v0.1，待批准，未实现）

> **性质**：离线设计草案，**未实现、未批准、不构成验收**。本文不改变任何已批准契约，
> 不解除门禁，不授权任何在线调用，也不声称"停止标记后物理零发送"已达成。
> **固定起点**：`91c872fa75c5b51b33dd479cebae33aeba56aeb0`（父提交 `1e81aa4`），分支 `codex/stage2c2-fingerprint`。
> **触发来源**：`verification/independent-2d-91c872-20261029/REPORT.md`（待决停止语义 A/B/C）；
> 本批只交付设计与离线证据，是否采用 B 由用户决定。

---

## 1. 问题陈述

当前实现（`server/core/probe/sendgate.go`、`chain.go`）能保证：

- 停止标记发布后**零新闸门准入**（`commitSend` 在 `c.mu` 读侧观察 `stopped`，与 `stop` 写侧构成全序）；
- 停止标记发布后**零新重试**；
- 未通过闸门的已准入尝试返回 `ErrAlreadyStopped` 且零字节发出。

**不能保证**的是原始表述的字面含义"停止标记后零新发送"：第 2 步（通过闸门）与
第 3 步（交给 transport）之间没有锁，中间被抢占的请求仍可能在标记之后开始传输。
独立复核用可控同步点与人工句柄各复现过该窗口（`hits=1`）。

方案 B 的目标不是"消灭这个窗口"（物理上无法撤销已发出的请求），而是：

1. 让窗口**有界且可观测**：`stop` 返回时可以回答"是否仍有已过闸门但尚未 handoff 的请求"；
2. 让"停止成功返回"成为一个**更强的、可验证的**语义点；
3. 在没有确认时**明确报告未确认**，而不是静默给出过强结论。

---

## 2. 术语与线性化点

| 事件 | 定义 | 线性化点 | 停止标记后 | 停止成功返回后 |
|---|---|---|---|---|
| E1 入口已准入 | `beginAttempt` 在 `c.mu` 写侧登记在途句柄 | `c.mu.Lock` 内完成 `inflight[handle] = struct{}{}` | 不再发生（新入口被拒 `ErrAlreadyStopped`） | 不再发生 |
| E2 已通过闸门 | `commitSend` 在 `c.mu` **读侧**返回 nil | `RLock` 临界区内对 `stopped` 的读取结果 | 不再发生（读侧必然看到 `stopped=true`） | 不再发生 |
| E3 交给 transport（handoff） | 调用 `g.inner.RoundTrip(req)` 的第一条指令 | 该调用进入内层 RoundTripper | **可能在标记之后发生**（B 用有界等待把它收敛） | **不再发生**（B 的停止返回条件） |
| E4 对端已收到 | 对端（fake/代理）计数或真实服务端处理 | 对端自身的处理点 | 不可观测、不可承诺 | 不可观测、不可承诺 |

要点：

- **停止标记（closing）的线性化点**：`stop` 在 `c.mu` 写侧把 `stopped` 置 true 的那一次写入。
  与 E2 的读侧共同构成 RWMutex 全序：任一 `commitSend` 要么在标记之前完成（E2 成立），
  要么在标记之后看到 `stopped=true` 并被拒绝。
- **停止成功返回的线性化点**：`stop` 返回且确认"E2 成立但 E3 尚未发生"的集合为空
  （见 §3 的 `pendingHandoff == 0` 判据）。此时"不再开始新的 handoff"成立；
  已在 transport 内的请求仍然存在，且**不可撤销**。
- **`RoundTrip` 返回 ≠ 发送开始**：`RoundTrip` 可能阻塞在等待响应头（服务端已接收但未响应、
  或连接建立后服务端迟迟不写头）。因此"`RoundTrip` 已经返回"既不能证明"字节已经发出"，
  也不能证明"对端已经处理"。B 只把 handoff（调用进入内层）作为可等待的事件，
  绝不把"请求完成"纳入停止条件（否则 `stop` 会随网络无限等待）。

---

## 3. 方案 B：两阶段停止 + 有界等待

### 3.1 阶段 1：发布 closing 标记（拒绝新准入）

```
stop(err):
  c.mu.Lock()
  if !stopped { stopped = true; stopSummary = sanitize(err) }
  cancels := snapshot(inflight)
  c.mu.Unlock()                     // 标记与快照在同一次写临界区内完成
  apply(cancels)                    // 锁外取消，避免持锁调用用户代码
```

与现状一致，唯一增加的是阶段 2。

### 3.2 阶段 2：有界等待"已过闸门、未 handoff"的集合归零

新增一个**只增不减的观测计数** `handoffPending`（`atomic.Int64`）与配套判据：

```
RoundTrip(req):
  c.mu.RLock()
  if stopped { RUnlock; return ErrAlreadyStopped }   // E2 线性化点
  handoffPending.Add(1)                              // 登记"已过闸门、未 handoff"
  c.mu.RUnlock()
  defer handoffPending.Add(-1)                       // handoff 完成即注销
  return g.inner.RoundTrip(req)                      // E3
```

`stop` 的阶段 2：

```
waitUntil := now + StopWaitLimit                  // 有界，且 StopWaitLimit 可配置（0 表示不等待）
for handoffPending.Load() > 0 {
    if now >= waitUntil { return StopResult{Confirmed: false, Pending: n} }
    sleep(min(pollInterval, waitUntil - now))     // 轮询 + 上限；不得持 c.mu
}
return StopResult{Confirmed: true, Pending: 0}
```

设计约束：

- **不得持 `c.mu` 等待**：等待期间 `c.mu` 必须可被其它 `commitSend`/`beginAttempt` 获取，
  否则 `stop` 与发送路径互相阻塞，退化为死锁。
- **不得无界等待**：`StopWaitLimit` 是显式上限；到达上限即返回"未确认"。
- **不得用睡眠放大保证**：轮询只用于观测计数，不改变 E3 的发生顺序；
  因此 `Confirmed=true` 的含义严格限定为"截至返回时刻没有处于 E2→E3 之间的请求"，
  不排除"已经 handoff 但尚未写出任何字节"的请求，也不排除对端已收到。
- `defer handoffPending.Add(-1)` 放在调用内层之前登记、内层返回后注销，
  故计数覆盖"进入内层前的最后一条指令"到"内层返回"整段；
  `RoundTrip` 长时间等待响应头只会让"未确认"更容易出现，不会让 `stop` 提前返回假阳性。

### 3.3 返回与调用方责任

`stop` 返回 `StopResult`：

| 字段 | 含义 |
|---|---|
| `Confirmed bool` | 是否在 `StopWaitLimit` 内观察到 `handoffPending == 0` |
| `Pending int64` | 返回时刻仍处于 E2→E3 之间的请求数 |
| `Waited time.Duration` | 实际等待时长 |
| `MarkerAt time.Time` | closing 标记的发布时刻（线性化点） |

- `Confirmed == true`：可以声明"停止成功返回后不再开始新的 handoff"。
  仍**不能**声明"物理零发送""对端零接收""服务端零计费"。
- `Confirmed == false`：必须返回一个新的 sentinel（候选 `ErrStopUnconfirmed`），
  调用方责任是把它当作**未确认**处理：不得据此判定"零新发送"，
  不得据此回滚已承诺的预算，不得据此关闭证据或宣称验收通过；
  可重试等待（再调用 `Wait`）或如实记录"存在 Pending 个未确认请求"。
- 两种情况都**不承诺撤销**已 handoff 的请求；取消 ctx 只让传输层尽快放弃。

### 3.4 与预算承诺的关系（B **不**闭合"停止标记后零新预算提交"）

预算 `Reserve → Commit` 发生在角色适配器内部（`budgetEmbedRole` /
`bailian.validatedEmbedRole`），位置在 E2 **之前**；`stop` 不参与预算账本。
因而已准入的尝试在标记之后仍可能完成一次 Commit（本批有确定性特征化测试
`TestStopMarkerDoesNotRetractBudgetCommit`）。B 的两阶段停止只收紧 E2→E3，
**不改变**这一点。

若要把"停止标记后零新预算提交"也做成保证，需要预算侧参与（记为 **B+**，未实现）：

- 由 `providers.ProcessBudget` 的 `Commit`（在 `pb.mu` 内）先咨询一个只读准入判据；
- 该判据与 `stop` 的标记发布必须同属一个全序；
- 由于 `Commit` 已在 `pb.mu` 内，判据不能在持 `pb.mu` 时再去取 `c.mu`
  （`stop` 若也取 `pb.mu` 就会形成 `c.mu → pb.mu` 与 `pb.mu → c.mu` 的反向环）；
- 因此 B+ 要么把预算提交移入 probe 自己的包装层（牺牲"生产角色内部记账"的路径真实性），
  要么改 `providers` 的预算 API 与锁序（影响全部消费者，属于已批准契约的变更）。

三条路都需要用户批准，本批**不实现**，只在文档与证据中把它保留为待决边界。

无论 A/B/B+，都不允许：静默退款以让计数好看、吞掉错误链、泄漏 lease。
本批保留：已承诺账本保守结算、`Abort` 不退款、slot 恰好释放一次、
`errors.Is` 链保留 `ErrAlreadyStopped` 与原始原因。

---

## 4. 保证矩阵（B，假设 `StopWaitLimit > 0`）

| 声明 | 停止标记后 | 停止成功返回（Confirmed）后 | 超时（Unconfirmed）后 |
|---|---|---|---|
| 新入口准入 | 不发生 | 不发生 | 不发生 |
| 新闸门准入（E2） | 不发生 | 不发生 | 不发生 |
| 新重试 | 不发生 | 不发生 | 不发生 |
| 新 handoff（E3） | **可能发生**（窗口有界，最多 `StopWaitLimit`） | 不发生 | **可能发生**（Pending > 0） |
| 已 handoff 请求被撤销 | 不承诺 | 不承诺 | 不承诺 |
| 对端收到 / 计费 | 不承诺 | 不承诺 | 不承诺 |
| 新增预算承诺 | **可能发生**（仅限标记前已准入的尝试） | 同左（B 不闭合） | 同左 |
| 预算保守结算 / lease 收敛 | 保持 | 保持 | 保持 |

---

## 5. 并发与实现边界

- `handoffPending` 用原子计数即可：`stop` 只读，`RoundTrip` 增/减；
  不存在"先读 0 再被 +1"的风险——因为**新准入在阶段 1 已被拒绝**，
  阶段 2 之后不可能再出现新的 +1（唯一来源是阶段 1 之前已过闸门的请求）。
- 阶段 2 与 `beginAttempt`/`commitSend` 不共享临界区，因此不会引入新的锁序；
  `c.mu` 在等待期间始终可获取。
- `stop` 可被并发调用（多个步骤同时失败）：首个停止原因不变，阶段 2 只做观测与等待，
  幂等；重复调用不会延长或缩短既有结论。
- `StopWaitLimit` 需在用户批准时给出取值（候选 0 / 100ms / 1s）：0 等价于"不等待但如实报告
  Pending"，是最弱也最诚实的配置；取值越大，`Confirmed` 的覆盖率越高，代价是停止延迟。
- 停止延迟直接影响 `Run`/`RunStep` 的返回时间与上层超时预算，需要计入 §5.3 的预算矩阵。

---

## 6. 与方案 A / C 的比较

| 维度 | A：接受较弱契约 | B：两阶段 + 有界等待 | C：串行化登记-发送 |
|---|---|---|---|
| 停止标记后的新 handoff | 可能（无界） | 可能，但 `stop` 返回时已确认归零或明确未确认 | 不发生（handoff 在登记段内开始） |
| 停止成功返回后的新 handoff | 可能 | 不发生（Confirmed） | 不发生 |
| 实现位置 | 只改文档与断言 | 闸门加原子计数 + `stop` 有界轮询 | 闸门与 `stop` 共享同一临界区 |
| 对吞吐的影响 | 无 | 无（计数无锁争用） | 有：每次发送都要与 `stop` 争用同一把锁 |
| 停止延迟 | 0 | 至多 `StopWaitLimit` | 取决于临界区持有时间；若持锁跨越 `RoundTrip` 会随网络等待，**违反有界要求** |
| 死锁风险 | 无 | 无（不持锁等待） | 有：必须严格禁止持锁跨越 `RoundTrip`，否则 `stop` 与发送互相阻塞 |
| 诚实性 | 需把契约改写为"零新闸门准入" | 可声明更强的"停止返回后零新 handoff" | 同 B，但代价与风险最高 |
| 是否闭合预算承诺问题 | 否 | 否（需 B+） | 否（除非同时改预算） |

方案 C 的"登记段"若只覆盖"过闸门 + 标记 handoff 标志"（不含 `RoundTrip`），
它其实等价于 B 的 `handoffPending` 判据，只是用互斥代替原子计数，代价是吞吐；
若覆盖 `RoundTrip`，则违反"不得无界等待"。因此 C 不属于本批推荐路径。

---

## 7. 待批准的开放问题

1. 是否接受 B 的语义（"停止返回后零新 handoff"，而不是"标记后物理零发送"）？
2. `StopWaitLimit` 取值与是否暴露给调用方（`ChainOptions`）？
3. 是否引入 `ErrStopUnconfirmed` 与 `StopResult` 的对外可见性（仅内部报告 vs 公开 API）？
4. 是否把 B+（预算侧参与）纳入范围？纳入则需修改 `providers` 的预算 API 与锁序契约。
5. 是否允许在 `Run`/`RunStep` 的返回值中携带停止确认状态（会影响既有错误语义）。

在上述问题获得明确决定之前：实现保持现状，本项继续标注"未闭合"，
不更新验收状态，不把 B 的设计写成"已实现"。

---

## 8. B 实现后的离线验证要求（供批准后使用）

- 确定性同步点：`sendGate.beforeCommit`（固定"已过闸门、未 handoff"）、
  `Chain.beforeCancel`（固定"标记已发布、取消未施加"）；
- 用例矩阵：`Pending` 归零路径（Confirmed）、`StopWaitLimit` 超时路径（Unconfirmed）、
  并发 `stop` 幂等、`RoundTrip` 阻塞在响应头（不得让 `stop` 等待完成）、
  预算不退款与 lease 收敛、`errors.Is` 链；
- 对端计数只能表述为"本机 fake 的命中数"，不得写成"网络字节"或"真实服务端处理"；
- 全部测试仅本机 fake/httptest/本机代理；不访问真实模型、ES、Qdrant、MySQL 或互联网。
