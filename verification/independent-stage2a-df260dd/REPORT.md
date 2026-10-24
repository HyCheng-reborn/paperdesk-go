# Stage 2A 剩余修复独立增量复核

**结论：存在阻塞（仅 E1 必要 race 证据缺口）；本轮未发现剩余源码阻塞。R1/R2 已闭合，暂不正式验收 2A。** 不需要继续修改业务代码来处理已闭合问题；不宣布 Stage 2 完成。

## 固定目标和保护

- 日期：2026-10-19。
- 完整目标：`df260ddfb12694e7064d74fb63cc1c228f8ebbfe`。
- 增量基线：`2780e398f2a3091f60c0d6c34ce7661e1716244d`，已核实为目标祖先。结合此前 `5bf62f0`、`2780e39` 的源码审查及设计基线 `212ccd8cc4c5743e6e34dfe5cb73e02b7a794b84`，不重复无关历史审计。
- 继续复用 `C:/Zhy/code/go-rag-review-1d3` linked worktree；原仓库 `C:/Zhy/code/go-rag` 的分支/工作区未切换、清理或改配置；未 clone/fetch。
- 切换前两份未跟踪复现 review_previous_test.go、review_remaining_test.go 已保存到本目录 preserved/，含 SHA256。切换目标后及所有干净验证结束时 status 为空。完成干净验证后才恢复两份原样复现文件，哈希一致；最终仍为这两个未跟踪文件，见 final-worktree-status.txt。
- 已读完整 14 文件增量、两处生产修改及六个新增测试、最新方案/进度/变更、本批验证日志，沿用前轮锁定依赖/适用 AGENTS 核验；本目标审查目录未发现新增 AGENTS.md。go.mod/go.sum 未变。

## 源码修复结论

### R1：预留阶段溢出 —— 已闭合

目标位置：`server/core/providers/budget.go:63`–`:109`（canReserve/applyReserve）。

canReserve 在上限判断之外分别检查 attemptsUsed+1、inFlight+1、inputReserved+InputTokens、outputReserved+MaxOutput 的可表示性；0 表示不限时也会检查。scope.reserve 仍在同一 ProcessBudget.mu 内先检查两个作用域，再整体 apply。拒绝路径未改任何计数，不会部分预扣。

原失败复现 `TestReviewFix_ReservationOverflow` 现通过；提交内 Input/Output 极大预留和 CrossScope 零残留测试也通过。此前负 usage、total-only、负配置、结算溢出及 slot 释放回归仍通过。

说明：本实现不在“无限上限”准入时预先结算未来 usage；只确保当前实际更新的各账本不回绕，后续结算受检并 fail closed。未因此要求改成精确金额预算或扩大已通过的设计。

### R2：resolver 内部 Profile 暴露 —— 已闭合

目标位置：`server/core/providers/registry.go:295`（resolve 调用 `r.creds(ctx,p.Clone())`，具体行号以该目标文件为准）。

resolve 向回调传深拷贝，内部用于 CacheKey、BuildRole 的对象不再传出；Clone 同时复制 KeyOptions。Chat/Embedding/Rerank 的构建均经共享 resolve，因此此单点修复覆盖三条路径。

原 `TestReviewFix_ResolverCannotMutateRegistryIdentity` 通过；提交内 CannotMutateProfile、CannotMutateKeyOptions、HoldsCopy 通过，验证即时修改、map 修改、保留副本后修改以及缓存命中稳定。不是只验证 fake 自身行为，而是经过实际 NewRegistry→GetChat→resolve→BuildChatRole 构造链；构造不发模型请求。

### 前序 B1/B2/B3

原 7 个独立复现和 task-root 装配补证全部保持通过。本次恢复的两份测试内容不变，没有为通过而改变断言或修改生产代码。

B2 的装配前提继续保留：TaskBudget owner 使用服务 root，同步 ReserveCtx 使用请求 context，后台接管后使用 task context；不把 HTTP 请求 context 作为整个后台任务 owner。真实 HTTP/stream 中途取消和终结接线尚属 2B。

## 唯一剩余阻塞：E1 race 未完成

- 干净目标包级 race：exit 1，`exit status 0xc0000139`。
- 干净目标关键 20 项十轮 race：exit 1，同样启动异常，实际 RUN=0。
- 准确结论：**运行异常、race 未完成、根因待确认**。

环境记录为 Go 1.27.1 windows/amd64、CGO=1、CC=gcc、MinGW-W64 gcc 8.1.0；这些事实不能单独证明 DLL 异常根因，也不能排除代码竞态。普通并发重复与无 DATA RACE 输出不代替 race 通过。本轮没有反复系统排障、安装/升级工具链、修改系统配置。

设计和前轮验收要求本包及关键并发用例 race 完成，因此保留 E1 为正式验收门禁。源码修复可视为闭合；补齐 race 前不能表述为 2A 已验收。

## 独立实测：干净目标层

全部测试 CWD：`C:/Zhy/code/go-rag-review-1d3/server`，PowerShell；GOPROXY=off、GOSUMDB=off；不添加 online/integration 标签。只运行默认离线测试及 httptest，本轮未观察到真实 DB/ES/Qdrant/模型调用。各日志保存 SHA、完整命令、CWD、时间和真实退出码；脚本为 run-clean.ps1。

| 完整命令 | 退出码 | 结果及日志 |
|---|---:|---|
| go test ./core/providers/... -count=1 -timeout=120s -v | 0 | 36 个顶层 / 50 含子项 PASS；ordinary.log |
| go test -race ./core/providers/... -count=1 -timeout=120s | 1 | 启动异常，未完成；race.log |
| go test -race ./core/providers/... -run 下列正则 -count=10 -timeout=180s -v | 1 | 启动异常，实际命中 0；race-stress.log |
| go test -p 1 ./... -count=1 -timeout=180s | 0 | 14 包 ok；full.log |
| go build ./... | 0 | build.log |
| go vet ./... | 0 | vet.log |

关键命令使用的准确正则（20 个已提交顶层用例，非已执行成功）：

```
^(TestBudget_(ReservationOverflow_(Input|Output|CrossScope)|NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)|TestRegistry_Resolver(CannotMutateProfile|CannotMutateKeyOptions|HoldsCopy))$
```

actual-test-names.txt 记录普通测试实际命中名称；environment.log 保存 Go/CGO/C 编译器路径和版本。没有用普通十轮重复替代失败的 race 命令。

## 独立复现层

干净目标全部命令结束后，原样恢复：

- `C:/Zhy/code/go-rag-review-1d3/server/core/providers/review_previous_test.go`，SHA256 `B0ADDFCD689DC900DB4E9A8764A91CEB38A41F4116BF267961C2E804DD2B7386`。
- `C:/Zhy/code/go-rag-review-1d3/server/core/providers/review_remaining_test.go`，SHA256 `5FF18A783A01F4EBF447EEB2C322580CD370ACC4BCF0FAE79560946192EE75ED`。

命令：`go test ./core/providers/... -run ^TestReview -count=1 -timeout=60s -v`，exit 0，10 个顶层 / 12 含子项 PASS。包括原 7 项、两个此前失败项、一个有效 task-root 装配补证。

该结果只属于“目标 + 独立复现测试”，不冒充目标自带测试。副本位于 preserved/，完整新增 diff 为两份 `.go.diff`，restored-test-hashes.txt 证实恢复文件与保存文件一致，reproduction.log 记录完整结果。历史报告与测试未覆盖、未删除。

## 实现者日志核对及非阻塞建议

- 本批 6 个新增测试均在目标提交中；没有依赖原工作区未跟踪文件。
- 50 PASS 为 36 顶层 + 14 子项，与独立重跑一致。
- test-key-stress.log 的 290 PASS 是 **20 个顶层×10=200 顶层 PASS，加90子项**；不是290个顶层用例，也不是race。
- 交付摘要称串行全量13包，提交日志实际14包，与独立重跑一致。该统计差异不是阻塞，无需为其另起修复轮次。
- 提交 race 日志仍在 NOTE 中写确定的 MinGW 缺入口点根因；本报告统一记根因待确认，不因日志措辞另立业务修复。
- CrossScope 新测试验证的是普通额度不足的双作用域零残留，不单独证明“不同请求聚合到进程预留后溢出”的场景；目前共享单锁与先验证后应用的生产逻辑可直接核实，未发现相应正确性缺陷，可作为后续补强建议。

## 实际装配和后续边界

预算 owner/lease 核心与 Registry→BuildRole→SDK/Rerank→HTTP 的构造链仍分开，未自动接入默认业务入口或 HTTP/stream finalize。本次未新增生产启用配置，也没有变更索引、数据或依赖。

2B 承接默认工厂、业务装配、实际发送/重试计数及 body/stream 生命周期绑定；2C 维度/模型身份 guard 就绪前，不得启用新 Embedding profile。在线协议、实际 usage/服务能力及2D付费探针仍需授权。A-4、真实 ES/Qdrant、SQL driver 取消等未在本轮验证。不要求本批提前实现后续阶段。

后续仅需在既有可运行环境补齐所列 race 证据；没有依据要求再改 R1/R2 业务实现。若仍无可运行环境，报告 E1 保持未完成，等待安排，不反复排障或伪造通过。

本轮未修改生产代码、方案或进度文档；未提交、推送或合并。报告完成后停止。
