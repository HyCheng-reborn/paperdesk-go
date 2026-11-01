# Stage 2B 阻塞修复 — 完整验证汇总 (stage2b-fix2)

- 目标 HEAD: `be28e918f4962e88906919e6f45ce39707cb9a50`（分支 `review/stage1a`）
- 工作区状态: 相对 HEAD 有未提交漂移（112 项变更，`server/` 下 20 文件 +1174/-193）。验证按**当前工作区实际内容**执行，非纯净 HEAD。详见 `environment-and-scope.txt`。
- 环境: Go 1.27.1 windows/amd64；race 工具链 MSYS2 UCRT64 gcc 16.2.0（进程级 PATH/CC/CGO_ENABLED=1）；系统默认 gcc 仍为 MinGW-W64 8.1.0（仅 race 步骤显式切 UCRT64）。
- 执行日期: 2026-10-20

## 退出码汇总

| 步骤 | 命令 | 日志 | EXIT | 结果 |
|------|------|------|------|------|
| 1 gofmt | `gofmt -l core/providers/ core/common/ core/config/ internal/logic/rag/ internal/logic/chat/ core/` | gofmt.log | 0 | 无未格式化文件 |
| 2 build | `go build ./...` | build.log | 0 | 无输出，通过 |
| 3 vet | `go vet ./...` | vet.log | 0 | 无输出，通过 |
| 4 providers 定点 | `go test ./core/providers/... -count=1 -v` | test-providers.log | 0 | 顶层 93 PASS（含 14 子测试合计 107），0 FAIL / 0 SKIP / 0 panic / 0 DATA RACE；providers + bailian 两包 ok |
| 5 关键 race 压测 | `go test -race ./core/providers/... -count=10 -v -run "..."` | race-key-stress.log / -repro.log（修复前 EXIT=1）；**race-key-stress-pass.log（修复后 EXIT=0）** | **0（修复后）** | 修复前：249 PASS / 1 FAIL（`TestStream_EarlyCloseFinalizes` flake），两次复现。修复后：250 PASS / 0 FAIL / 0 DATA RACE，该用例 10/10 PASS |
| 6 受影响包 race | `go test -race ./core/providers/... ./core/common/... ./core/rerank/... ./internal/logic/rag/... ./internal/logic/chat/... -count=1 -timeout=300s` | race-affected.log | 0 | 6 包全部 ok，0 DATA RACE（flake 未在 count=1 触发） |
| 7 全项目 | `go test -p 1 ./... -count=1` | test-full.log | 0 | 15 包 ok，15 无测试文件，0 FAIL |

## Step 5 失败与修复：`TestStream_EarlyCloseFinalizes`（flaky，非 DATA RACE）— 已修复

- 修复前命中: 10 次迭代中 RUN=10 / PASS=9 / **FAIL=1**；race detector 报 **0 DATA RACE**。
- 失败断言: `integration_test.go:329: expected lease to be finalized after Close`。
- 隔离复现: 单测 `-count=50` → 50/50 PASS；完整 stress 命令 `-count=10` 连续两次 → 每次 1 FAIL。属调度相关的时序 flake。

### 根因（测试同步缺陷，非生产缺陷）
`wrapLeaseStreamReader`（`core/providers/bailian/chat.go`）返回的是 `schema.Pipe` 的裸 `reader`；lease 的终结发生在**后台 goroutine**：当下游 Close 使 `writer.Send` 返回 false 时，goroutine 才调用 `lsw.Close()` → `finalize()` → `lease.Finish()`。测试在 `wrapped.Close()` 后**立即**断言 `lease.Finalized()`，两者之间无同步，goroutine 未必已被调度执行 → 偶发失败。生产语义为"最终一致终结"，本身正确。

### 已采用修复（方向 1，测试侧最小改动，leader 批准）
在 `TestStream_EarlyCloseFinalizes` 中，`wrapped.Close()` 后以带 2s 超时的轮询等待 `lease.Finalized()`，替代原同步断言；仅改测试，不动生产异步契约。同目录 `TestStream_CancelFinalizes`/`ErrorFinalizes`/`ConcurrentCloseFinalizes` 经核查均相对于测试可观测点同步终结或用 WaitGroup 等待，无同类 flake，未改动。

### 修复后验证证据
- gofmt: 干净（exit 0）。
- 隔离 `-race -count=200`（仅该用例）: 200/200 PASS，0 DATA RACE。
- 完整 Step5 命令 `-race -count=10`: **EXIT=0**，250 PASS / 0 FAIL / 0 DATA RACE，EarlyClose 10/10（race-key-stress-pass.log）。
- 扩展 `-race -count=30`（TestStream_/Budget_/Embedding_/Rerank_/Builder_）: 两包均 ok，EXIT=0。

## 结论
Step 1/2/3/4/6/7 全部 EXIT=0。Step 5 修复前 EXIT=1（唯一失败为 `TestStream_EarlyCloseFinalizes` 时序 flake，0 DATA RACE），已按方向 1 于测试侧修复并重跑转绿（EXIT=0，累计 240 次迭代无 FAIL/无 DATA RACE）。**全部 7 步现为绿**，可放行 #100（文档更新与提交推送）。
