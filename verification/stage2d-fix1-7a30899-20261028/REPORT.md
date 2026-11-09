# Stage 2D S-1 fix batch 1 —— 五项独立复核问题的修复与离线证据

- 固定起点（父提交）：`7a30899b0e205ad714b5a8ac4db152eb200bcb76`
- 祖父提交：`a12af924f0441363b46e0fec41fca29bdc30993a`
- 分支：`codex/stage2c2-fingerprint`
- 复核来源：`verification/independent-2d-7a30899-20261028/REPORT.md`（P1/P1/P2/P2/P2）
- 环境：go1.27.1 windows/amd64；gcc 16.2.0（UCRT64）

本批**只**修五项问题、更新草案过时表述并补充本机 fake 回归测试。
未调用任何真实模型 / 真实 ES / Qdrant / MySQL，未执行在线探针，未解除门禁，未更新验收状态。

---

## 1（P1）默认构建可绕过远端隔离

**根因**：远端能力是公开结构体字段 `ChainOptions.AllowRemoteEndpoints`，默认构建里存在
"传 true 就放行"的路径，隔离强度依赖调用者自律。

**修复**：
- 删除该字段；`ChainOptions` 只剩 `APIKey`。
- 远端能力改为构建期常量：`endpoint_policy.go`（`//go:build !online`，恒 `false`）
  与 `endpoint_policy_online.go`（`//go:build online`，`true`）。
- `CheckEndpoints(plan)`（导出、无 allowRemote 参数）在 `NewChain`、`Preflight`、
  `Readiness` 中共用；默认构建对非回环端点直接 `ErrRemoteNotAllowed`。
- `RunOnline` 仅在 online 构建编译，默认构建不存在该入口。

**测试**（`endpoint_policy_test.go`，默认构建）：
- `TestDefaultBuild_ChainOptionsHasNoRemoteSwitch`：`RemoteEndpointsAllowed()==false`；
  反射断言字段集合恰为 `[APIKey]`（新增字段会让测试失败）。
- `TestDefaultBuild_RemoteEndpointsRejectedWithZeroSends`：三个子用例（明文远端、
  HTTPS 远端、携带已批准授权）均构造失败，类别 `category=validation`，本机 fake 代理命中 0。
- `TestProxyControl_EnvironmentIsHonored`：控制组，证明代理环境变量确实生效，
  因此"命中 0"不是空断言。
- `TestDefaultBuild_CheckEndpointsRejectsRemote`：策略层拒绝远端、放行回环。

---

## 2（P1）在线入口允许远端明文 HTTP 并发送凭据

**根因**：online 构建对非回环端点只判断"是否放行"，未强制 HTTPS，也没有禁止
端点内嵌 userinfo；错误信息还可能回显完整 URL。

**修复**（`probe.go` 的 `CheckEndpoints`）：
- 非回环端点必须 `https`，明文 → `ErrPlaintextRemote`；回环 fake 仍允许 http。
- `url.User != nil` → `ErrEndpointUserInfo`（凭据只来自环境变量）。
- 错误只回显字段名与 `Hostname()`；解析失败时不再回显原始 URL 字符串。
- `RunOnline` 先跑 `Preflight`（含端点策略），再校验授权与凭据，全部在发送前。

**测试**（`online_endpoint_test.go`，`-tags online`，对端全为本机）：
- `TestOnlineBuild_HasRemoteCapability`：https 远端通过策略、http 远端被拒、回环可用。
- `TestOnlineBuild_PlaintextRemoteRejectedBeforeAnySend`：`ErrPlaintextRemote`，零发送。
- `TestOnlineBuild_UserInfoEndpointRejectedWithoutEcho`：`ErrEndpointUserInfo`，
  错误文本不含占位用户名/密码。
- `TestOnlineBuild_QuerySecretNotEchoedInEvidence`：查询参数中的占位秘密不出现在
  错误与 Readiness 详情中。
- `TestOnlineBuild_CredentialValueNeverAppearsInEvidence`：凭据缺失只报变量名；
  凭据存在时其值不进入任何对外文本。
- `TestOnlineBuild_LoopbackFakeStillRunsThroughRunOnline`：online 入口对本机 fake
  仍可完整跑完三步（HTTPS 要求只作用于非回环）。

---

## 3（P2）被禁止的重定向被误判为传输错误并重试

**根因**：`isTransportError` 把所有 `*url.Error` 都视为传输错误。但
`providers.ErrRedirectProhibited` 由 `CheckRedirect` 产生，也被 `*url.Error` 包裹，
于是"重定向被禁止"会触发重试，重复命中原端点并重复消耗预算。

**修复**（`errcat.go`）：改为**允许集合**策略。
先排除重定向、预算耗尽、ctx 取消/deadline、携带 HTTP 状态码的错误；
只有 `*url.Error` 的底层原因属于 EOF / `net.Error` 时才重试。
`shouldRetry` 仍先看 `MaxAttempts` 与 `RetryOnTransportError`，预算 owner、
每次尝试计数与 `errors.Is` 错误链均保持不变。

**测试**（`chain_test.go`）：
- `TestRedirect_ProhibitedIsNotRetried`：307 指向第二个本机服务；断言原端点命中 1、
  目标命中 0、`errors.Is(err, providers.ErrRedirectProhibited)`、`Attempts=1`、
  `CommittedAttempts=1`、`StopReason=category=redirect_prohibited`、后续步骤不启动。

---

## 4（P2）在线入口未在发送前校验全部步骤

**根因**：入口只检查"步骤切片非空"，逐步校验发生在执行时；当"第一步合法、第二步非法"时，
Readiness 说未就绪，而 `RunOnline` 仍会先真实发出第一步。

**修复**（`preflight.go`）：新增 `Preflight(plan, steps)`（计划 → 端点策略 → 全部步骤）
与 `ValidateSteps(plan, steps)`（逐步 `validate` + `validateAgainst`）。
`Chain.Run`、`RunOnline`、`Readiness` 的 `plan` / `steps` 项全部复用同一组函数；
`RunStep` 仍保留运行期校验（防绕过）。

**测试**：
- `TestPreflight_InvalidLaterStepSendsNothing`（默认构建）：chat 命中 0、`report.Steps=0`、
  `StopReason=category=validation`、Readiness 同源拒绝、`RunStep` 运行期校验仍生效。
- `TestOnlineBuild_PreflightRejectsLaterInvalidStepBeforeAnySend`（online）：错误含
  `preflight` 与 `steps[1]`、代理命中 0、`Ready()==false`。

---

## 5（P2）证据报告包含未经脱敏的服务端原始错误

**根因**：`Report.StopReason` 直接写入 `err.Error()`；服务端 4xx/5xx 正文（可能回显
请求内容或凭据）因此进入证据；`ErrAlreadyStopped` 也回显原始错误。

**修复**（`errcat.go`）：
- `sanitizeStopReason` 只输出固定类别名，必要时追加可可靠提取的 HTTP 状态码
  （`category=http_status status=400`），永不输出 `err.Error()`。
- 类别覆盖：canceled / deadline / budget_exhausted / redirect_prohibited / http_status /
  transport / identity_mismatch / embedding_profile_not_ready / resource_replaced /
  target_binding_not_ready / dimension_mismatch / not_authorized / scope_not_supported /
  missing_credentials / validation / already_stopped / unexpected。
- **不吞错误**：原始 error 仍逐层 `%w` 返回，调用方可用 `errors.Is` 识别取消、预算、
  身份等 sentinel；只有写进证据的摘要被脱敏。
- `Chain.stopSummary` 替代原来的 `stopReason error`；`ErrAlreadyStopped` 只拼接摘要。

**测试**：
- `TestStopReasonIsSanitized`：服务端返回 400 + 占位敏感标记正文；`StopReason` 恰为
  `category=http_status status=400`，不含标记、不含主机/协议；返回的 error 仍含标记
  （证明未吞错）；`ErrAlreadyStopped` 的文本不含标记。
- online 侧的三个"不回显"用例（见问题 2）。

---

## 6 保持不变的边界

- 方案 A 的**本机 fake ES 真实绑定**、完整 fingerprint、guard 前后检查均未改动：
  `TestProbeChain_ProductionBindingAndScope` 仍断言构建期 2 次读取、每次 embedding
  前后各 1 次 guard 读取、fake ES 无非读取请求。
- W-1..W-5、预算 owner/lease、取消、清理、Qdrant 禁用、默认未授权边界未被本批修改。
- 未新增映射表、未新增 DDL、未改动生产代码与门禁。

---

## 7 命令、退出码与命中测试

完整清单见 `results.txt`，原始日志同目录。摘要：

| # | 命令 | 退出码 | 日志 |
|---|---|---|---|
| 1 | `go test -p 1 ./core/probe/... -count=1 -v` | 0 | `test-probe-default.log` |
| 2 | `go test -tags online -p 1 ./core/probe/... -count=1 -v` | 0 | `test-probe-online.log` |
| 3 | `go test -race -p 1 ./core/probe/... -count=1 -v`（UCRT64 gcc） | 0 | `race-probe-default.log` |
| 4 | `go test -race -tags online -p 1 ./core/probe/... -count=1 -v` | 0 | `race-probe-online.log` |
| 5 | `go test -race ... -count=5 -run "TestW[1-5]\|...\|TestDefaultBuild\|TestProxyControl"` | 0 | `race-targeted-repeat-default.log` |
| 6 | `go test -race -tags online ... -count=5 -run "TestW[1-5]\|...\|TestOnlineBuild\|TestProxyControl"` | 0 | `race-targeted-repeat-online.log` |
| 7 | `go test -p 1 ./... -count=1` | 0 | `test-full-serial.log` |
| 8 | `go build ./...` | 0 | `build-vet-online-vet.log` |
| 9 | `go vet ./...` | 0 | 同上 |
| 10 | `go vet -tags online ./core/probe/...` | 0 | 同上 |
| 11 | `gofmt -l core/probe` | 0（无输出） | 同上 |

环境失败：无。上一批次 race 曾因系统 gcc 8.1.0 出现 `0xC0000139`；本批统一使用
UCRT64 gcc 16.2.0，race 正常完成。

---

## 8 未验证边界（不得据本报告扩大结论）

- **真实模型端点**：未调用。U-1（端点/模型）、U-3（预算数值）、U-5（凭据命名）仍待用户给出；
  `RunOnline` 的在线路径本批**未执行**。
- **真实 ES / Qdrant / MySQL**：未连接。方案 A 的绑定读取的是本机 fake ES，不产生任何
  真实 ES 事实；S-2/S-3 未启动。
- **独立复核**：本批修复**尚未**被独立复核；在复核通过前，草案 §9 中相关行只能标
  "离线验证通过 / 待独立复核"。
- **验收状态**：`PROJECT_PROGRESS.md`/`CHANGES.md` 与任何门禁均未修改。
- 本报告不构成 Stage 2D 通过、不构成 Stage 2 完成。

---

## 9 结论

五项问题均已修复，并有调用实际探针入口（`NewChain`/`Chain.Run`/`Chain.RunStep`/
`RunOnline`/`Readiness`/`CheckEndpoints`）的回归测试覆盖；对端全部为本机 fake
（fake ES、fake 模型、fake 代理）。默认构建与 online 构建的普通测试、UCRT64 race、
定向重复 race、串行全量测试、build、vet 与 online 标签静态检查全部通过（exit 0）。
提交后停止，等待独立复核。
