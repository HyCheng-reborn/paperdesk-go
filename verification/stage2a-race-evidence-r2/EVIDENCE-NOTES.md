# Stage 2A E1 race 补证 — 执行结果（本环境单次尝试）

**日期**：2026-10-19
**固定目标（完整 SHA，已用 Git 对象核实 = 主仓当前 HEAD）**：`df260ddfb12694e7064d74fb63cc1c228f8ebbfe`（分支 `review/stage1a`，`fix(stage2a): reservation overflow and resolver profile isolation`）。
**工作区核对**：主仓 `server/core/providers/` 全部文件均自该目标提交树；工作区其余为 autocrlf 行尾幻影 M 与无关未跟踪文件（`verification/*`、`.tmpbuild.log` 等），不影响 providers 包内容。目标未 clone/fetch，未切换/清理主仓，未操作 Codex 审查 worktree（`C:/Zhy/code/go-rag-review-1d3`）。
**环境核对**：Go 1.27.1 windows/amd64；CGO_ENABLED=1；CC=gcc；CXX=g++；MinGW-W64 gcc 8.1.0（`C:\Program Files\mingw64\bin\gcc.exe`）；GOFLAGS 空；GOOS=windows GOARCH=amd64。未安装/升级工具链、未搭建 WSL、未改系统或 Git 配置。未连接真实设施、未调用模型 API、未加 online/integration 标签。

## 命令与结果（各单次；与旧环境失败日志分开存放于本目录）

1. `go test -race ./core/providers/... -count=1 -timeout=180s`
   - 退出码：**1**
   - 输出：`exit status 0xc0000139` → `FAIL github.com/.../core/providers 0.094s`（race 测试二进制启动异常，未能执行）
   - 日志：`race-package-r2.log`
2. `go test -race ./core/providers/... -run "^(TestBudget_(ReservationOverflow_(Input|Output|CrossScope)|NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)|TestRegistry_Resolver(CannotMutateProfile|CannotMutateKeyOptions|HoldsCopy))$" -count=10 -timeout=180s -v`（20 项完整正则）
   - 退出码：**1**
   - 输出：`exit status 0xc0000139`（启动异常）
   - 实际统计：RUN-LINES=0、PASS-LINES=0、DATA-RACE-LINES=0、NO-TESTS-TO-RUN=0 —— race 二进制从未运行，**无一项实际执行**（不得当作通过）。
   - 日志：`race-stress-r2.log`、正则见其 `ACTUAL-REGEX` 行

## 结论（与独立报告一致）

- E1 **未完成**：本环境（同一 Windows/MinGW 8.1.0）经**实际执行**复现 `0xc0000139` 启动异常，非仅凭版本推断；两次命令均 exit 1、race 覆盖实际为 0。
- 状态：**运行异常、race 未完成、根因待确认**。未反复排障、未安装/升级工具链、未搭建 WSL、未修改系统/Git 配置、未伪造通过、未新增业务修复提交。
- 未将普通测试或无 DATA RACE 输出当作 race 通过；未把 no-tests-to-run / RUN=0 当作通过。
- R1/R2 源码修复已由独立复核判定闭合；本任务不重跑 Stage 1 历史压测、不改业务代码/测试语义/方案/验收状态。

## 剩余

- 需在一个**现成、已授权、确可运行 Go race** 的环境（不同于本异常环境）中，对同一干净目标 `df260dd…` 重跑上述两条命令并补齐 race 证据。
- 2A 在 E1 race 证据补齐前不得表述为已验收；不宣布 Stage 2 完成。本任务未提交、未推送。

---

## 更新（2026-10-19，环境修复后）— E1 已闭合

用户手动将工具链切为 MSYS2 UCRT64 gcc 16.2.0 / Binutils 2.47（进程级 PATH/CC，系统级未改）。同一干净目标 `df260ddfb12694e7064d74fb63cc1c228f8ebbfe` 上实际重跑两条命令均成功：
- 命令1 `-race -count=1`：`ok ... core/providers`，EXIT 0，无 DATA RACE（`race-package-r2-pass.log`）。
- 命令2 冻结20项正则 `-race -count=10 -v`：EXIT 0，顶层 `--- PASS`=200（20×10）、FAIL=0、DATA RACE=0、no-tests-to-run=0（`race-stress-r2-pass.log`）。

以上方为本次验收的正式结果；本文上方 0xc0000139 段落为旧 MinGW 环境历史证据，保留不删。详见同目录 `PASS-SUMMARY.md`。**E1 正式闭合。** 仍未提交/未推送；不宣布 Stage 2 完成。
