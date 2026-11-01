# Stage 2A 阻塞修复验证报告

- 日期：2026-10-19
- CWD：`c:\Zhy\code\go-rag\server`
- 执行方式：`run-step.ps1` 统一封装，日志含 CMD/CWD/EXIT/DATE 头

## 退出码汇总

| Step | 命令 | 日志 | EXIT | 结论 |
|---|---|---|---|---|
| 1 | `gofmt -l core/providers/` | gofmt.log | 0 | 无格式差异（输出为空） |
| 2 | `go build ./...` | build.log | 0 | 构建通过 |
| 3 | `go vet ./...` | vet.log | 0 | 无告警 |
| 4 | `go test ./core/providers/... -count=1 -v` | test-providers.log | 0 | 44 RUN / 44 PASS / 0 FAIL / 0 panic |
| 5 | `go test ./core/providers/... -count=10 -v -run "..."` | test-key-stress.log | 0 | 14 用例 × 10 轮，0 FAIL / 0 panic |
| 6 | `go test -race ./core/providers/... -count=1 -timeout=180s` | race-providers.log | 1 | **运行异常、race 未完成**（详见下节） |
| 7 | `go test -p 1 ./... -count=1` | test-full.log | 0 | 14 包 ok / 0 FAIL，耗时 47.2s |

## Step 4 定点测试明细

- 顶层 RUN 30 个，含子测试共 44 个 RUN，44 个 PASS，0 SKIP，0 FAIL，0 panic。
- 包结果：`ok github.com/wangle201210/go-rag/server/core/providers 0.177s`

## Step 5 压测命中用例（每个 ×10 轮，全 PASS）

1. TestBudget_ConcurrentFinalize
2. TestBudget_ConcurrentReserve_MaxRequests
3. TestBudget_DeadlineNotRefreshedByRetry
4. TestBudget_NegativeProcessLimit
5. TestBudget_NegativeUsageConservative
6. TestBudget_OverflowSlotRelease
7. TestBudget_OwnerCancelPropagates
8. TestBudget_SlotRelease_NonStream
9. TestBudget_SlotRelease_StreamPaths
10. TestBudget_TransferKeepsAbsoluteDeadline
11. TestBudget_TransferToBackground
12. TestProfile_CacheKey_NoCollision
13. TestProfile_CacheKeyNormalization
14. TestRegistry_SnapshotIsolated

合计 140 次顶层执行（含子测试共 230 个 RUN），`--- FAIL` / `panic` / `DATA RACE` 匹配为 0。
包结果：`ok github.com/wangle201210/go-rag/server/core/providers 0.741s`

## Step 6 race 异常记录

- 现象：`exit status 0xc0000139`（DLL entry point not found），测试二进制启动即失败，包报 FAIL 但非测试断言失败。
- 记录：**运行异常、race 未完成、根因：MinGW-w64 8.1.0 缺少 Go race 运行时所需入口点**。
- 与既有平台级问题记录一致（core/indexer、core/common 亦曾复现），非代码竞态证据。
- 按规范仅尝试一次，未重复运行 race 二进制，未以普通并发测试替代 race 结论；race 证据保持"未完成"状态。

## Step 7 全项目测试明细

- ok（14 包）：core (4.061s)、core/common、core/indexer、core/providers、core/rerank、core/retriever、core/vector、core/websearch、internal/cmd (2.187s)、internal/controller/rag、internal/dao、internal/logic/chat、internal/logic/knowledge、internal/logic/rag (15.391s)
- FAIL：0；panic：0
- 其余包为 `[no test files]`（api、config、grader、types、dao/db、dao/internal、logic/websearch、mcp、model/*、server 等），属正常。

## 总体结论

- 除 race 因已知 Windows 平台异常（0xc0000139）未能完成外，gofmt / build / vet / 定点测试 / 10 轮压测 / 全量测试全部通过（EXIT 0）。
- 所有日志均逐行核查过 RUN 与 panic/FAIL 关键字，无被 EXIT 0 掩盖的失败输出。
- race 证据缺口需在平台问题（MinGW-w64 工具链）解决后补跑。

## 附属文件

- `run-step.ps1`：统一日志封装运行器
- `summarize.ps1` / `stress-count.ps1`：日志统计脚本
