# Stage 2A E1 race 正式验收 — 通过结果（环境修复后，2026-10-19）

## 验收对象与环境
- **目标提交（冻结一致）**：`df260ddfb12694e7064d74fb63cc1c228f8ebbfe`（分支 `review/stage1a`；= 主仓 HEAD，已核实；`core/providers` 相对目标无内容改动）。
- **环境（进程级，系统级未改）**：Go 1.27.1 windows/amd64；GCC = MSYS2 UCRT64 `16.2.0`（`C:\msys64\ucrt64\bin\gcc.exe`）；Binutils = GNU ld `2.47.20260726`；CGO_ENABLED=1；GOFLAGS 空；CWD `c:/Zhy/code/go-rag/server`。
- 未连真实设施、未调模型 API、未加 online/integration 标签；未改生产/测试代码。

## 命令与结果
| 命令 | 退出码 | 结果 |
|---|---:|---|
| `go test -race ./core/providers/... -count=1 -timeout=180s` | 0 | `ok github.com/wangle201210/go-rag/server/core/providers 1.238s`，无 DATA RACE。日志 `race-package-r2-pass.log` |
| `go test -race ./core/providers/... -run "<冻结20项正则>" -count=10 -timeout=180s -v` | 0 | 包 `ok`；顶层 `--- PASS`=200（20 用例×10），`--- FAIL`=0，`DATA RACE`=0，`no tests to run`=0。日志 `race-stress-r2-pass.log`（ACTUAL-REGEX 见文件与本报告） |

冻结正则（20 项，未删减/更换/降次数）：
```
^(TestBudget_(ReservationOverflow_(Input|Output|CrossScope)|NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)|TestRegistry_Resolver(CannotMutateProfile|CannotMutateKeyOptions|HoldsCopy))$
```

实际命中的 20 个顶层用例（全部 PASS，各 10 轮）：
TestBudget_ConcurrentFinalize、TestBudget_ConcurrentReserve_MaxRequests、TestBudget_DeadlineNotRefreshedByRetry、TestBudget_DualScope_PartialFailure、TestBudget_IdempotentFinalize、TestBudget_NegativeProcessLimit、TestBudget_NegativeUsageConservative、TestBudget_OverflowSlotRelease、TestBudget_OwnerCancelPropagates、TestBudget_ReservationOverflow_CrossScope、TestBudget_ReservationOverflow_Input、TestBudget_ReservationOverflow_Output、TestBudget_RetryNoRefresh、TestBudget_SettlementOverflow、TestBudget_TotalOnlyUsage、TestBudget_TransferKeepsAbsoluteDeadline、TestBudget_TransferToBackground、TestRegistry_ResolverCannotMutateKeyOptions、TestRegistry_ResolverCannotMutateProfile、TestRegistry_ResolverHoldsCopy。

## 结论
- **E1 race 正式闭合**：本环境经实际执行，包级 race 与 20 项关键并发用例十轮 race 均 EXIT 0，0 FAIL、0 DATA RACE，命中数=20（非 no-tests/RUN=0 冒充）。未把普通测试当 race 通过。
- 之前 `verification/stage2a-race-evidence-r2/race-package-r2.log`、`race-stress-r2.log` 记录的是旧 MinGW 环境 `0xc0000139` 运行异常，作为历史证据保留、未删除/覆盖。

## Stage 2A 验收缺口状态
- R1/R2 源码修复：独立复核判定闭合（本轮未改）。
- E1 race 证据：**已补齐并闭合**。
- 至此 Stage 2A 的既定验收门禁（源码 + 离线 + race + build/vet + 串行全量）均已满足；正式验收以独立复核实测结论为准。
- **不宣布 Stage 2 完成**；2B/2C/2D（默认工厂/业务装配、维度 guard、在线付费探针）仍属后续，未在本轮验证。A-4、真实 ES/Qdrant、SQL driver 取消等边界不变。
