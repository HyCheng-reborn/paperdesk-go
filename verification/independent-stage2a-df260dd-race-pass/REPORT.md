# Stage 2A E1 独立实测最终复核

**结论：通过（Stage 2A 既定源码与离线验收范围）。E1 已闭合，未发现已知剩余阻塞。**

固定目标：`df260ddfb12694e7064d74fb63cc1c228f8ebbfe`。
日期：2026-10-19。

## 独立执行与保护

本轮先只读核对实现者 verification/stage2a-race-evidence-r2/ 新增通过日志及工具链记录，再在 `C:/Zhy/code/go-rag-review-1d3/server` 独立执行原冻结两条命令。

审查目录始终为目标的 linked worktree，未 checkout/fetch/clone 或操作原仓库分支。执行前把两份审查方未跟踪复现文件保存到本目录 preserved/，记录哈希，确认 HEAD 精确匹配且 git status --short 为空。两条命令均在未加入补充测试的干净目标中运行。结束后恢复原复现文件，内容与保存副本一致，仍未跟踪；历史失败日志和报告保持原处。

仅本次测试进程临时设置 PATH、CC、CXX、CGO_ENABLED、GOFLAGS、GOPROXY、GOSUMDB，不修改系统环境或 Go/Git 配置。使用用户已经安装的工具链，没有安装、升级或覆盖旧 GCC。

## 工具链

- Go 1.27.1 windows/amd64，CGO_ENABLED=1，GOFLAGS 空。
- CC：`C:/msys64/ucrt64/bin/gcc.exe`；CXX：同目录 g++.exe。
- GCC：MSYS2 UCRT64 16.2.0；GNU ld：2.47.20260726。
- GOPROXY=off、GOSUMDB=off；无 online/integration 标签。

完整环境见 environment.log。使用默认离线用例和 httptest，未观察到真实 DB/ES/Qdrant/模型调用。

## 实测结果

| 完整命令 | 退出码 | 结果 |
|---|---:|---|
| go test -race ./core/providers/... -count=1 -timeout=180s | 0 | 包 ok；race-package.log |
| go test -race ./core/providers/... -run 下列冻结正则 -count=10 -timeout=180s -v | 0 | 20 个顶层各10轮，200顶层PASS、250含子项PASS；race-stress.log |

```
^(TestBudget_(ReservationOverflow_(Input|Output|CrossScope)|NegativeUsageConservative|TotalOnlyUsage|SettlementOverflow|NegativeProcessLimit|OverflowSlotRelease|OwnerCancelPropagates|DeadlineNotRefreshedByRetry|TransferKeepsAbsoluteDeadline|ConcurrentReserve_MaxRequests|DualScope_PartialFailure|RetryNoRefresh|TransferToBackground|IdempotentFinalize|ConcurrentFinalize)|TestRegistry_Resolver(CannotMutateProfile|CannotMutateKeyOptions|HoldsCopy))$
```

独立统计：顶层 RUN=200、PASS=200，20 个不同用例均恰好10次；含子项 RUN/PASS=250；FAIL=0、DATA RACE=0、no tests to run=0。实际命中名称见 actual-test-names.txt，计数见 counts.json。两份原始日志含 SHA、CWD、完整CMD、时间、真实EXIT。

这是真实 race 执行结果，不是普通重复、空命中或实现者摘要转述。旧环境 0xc0000139 继续作为历史运行异常证据保留；本次成功说明在该新工具链环境可以完成验证，不进一步断言旧异常的确切根因。

## 综合验收判断

- R1 预留阶段溢出、R2 resolver Profile 暴露：此前独立源码及复现已判闭合。
- 原 B1/B2/B3：前两轮原始复现和相邻回归通过，边界保持。
- 普通 providers、独立复现、串行全量14包、build、vet：同一完整目标已在 verification/independent-stage2a-df260dd/REPORT.md 及其日志中独立实测通过；本轮仅更换测试进程编译器以补E1，不无关重复全量审计。
- E1：本轮干净固定目标包级及冻结关键十轮 race 均实际通过，正式闭合。

因此，Stage 2A 的既定验收门禁已满足，可验收该完整目标；无需继续安排本批业务修复。

## 保留边界

不代表 Stage 2 整体完成，也不代表全业务已有预算保护。默认工厂/业务装配、真实 HTTP 尝试/重试及 body/stream 终结绑定由2B承接；新 Embedding profile 启用必须等待维度/模型身份 guard；2C索引/fingerprint和2D获授权在线探针仍待完成。A-4、真实ES/Qdrant、SQL driver取消等未验证边界不变。

实现者新日志支持相同结论。旧日志及原报告的“E1未完成”是当时状态，本报告是对固定目标的最新独立补证结论，不覆盖或抹去历史证据。

本轮未修改生产代码、测试语义、方案或项目验收状态文档；未提交、推送或合并。完成报告后停止。
