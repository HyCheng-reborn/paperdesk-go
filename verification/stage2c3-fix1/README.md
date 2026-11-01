# Stage 2C-3 增量复核修复（P1-1 … P2-3）— 验证证据

- 固定起点（baseline / 本提交父提交）：`c4111e031521eb372198d6b428759637cf21fee8`
- 上一提交（父提交）：`6ab2027ecf8e5cf6664ac751f3a40305772d9ffd`
- 分支：`codex/stage2c2-fingerprint`
- 独立报告：`verification/independent-2c3-c4111e0-20261028/REPORT.md`
- 运行时：go1.27.1 windows/amd64，`CGO_ENABLED=1`，CC=`C:\msys64\ucrt64\bin\gcc.exe`
- 离线约束：不调用真实模型 / ES / Qdrant / MySQL；只使用 `httptest` 与本机 fake。

## 复现脚本

`run.ps1`（工作目录仓库根）依次执行并写入日志：

| 日志 | 命令 |
| --- | --- |
| `focused.log` | `go test ./core/config ./core/providers ./core/vector ./core/common ./core ./internal/logic/rag -run '^(TestP11_\|TestP12_\|TestP13_\|TestP21_\|TestP22_\|TestP23_\|TestProviderNameNormalization\|TestStrictBool_)' -count=1 -timeout=300s -v` |
| `affected.log` | 同上包，`-count=1 -timeout=300s` |
| `race-affected.log` | 同上包，`-race -count=1 -timeout=600s` |
| `race-repeat-budget.log` | `go test -race ./core/common -run '^(TestP11_\|TestP12_\|TestP13_\|TestP22_\|TestProviderNameNormalization)' -count=3 -v` |
| `race-repeat-yaml.log` | `go test -race ./internal/logic/rag -run '^(TestP21_\|TestP23_\|TestP13_FactoryPath)' -count=3 -v` |
| `race-repeat-strictbool.log` | `go test -race ./core/config -run '^TestStrictBool_' -count=5 -v` |
| `full.log` | `go test -p 1 ./... -count=1 -timeout=600s` |
| `vet.log` | `go vet ./...` |
| `build.log` | `go build ./...` |

## 结果（全部 EXIT 0）

- `focused`：P1-1/P1-2/P1-3/P2-1/P2-2/P2-3 + 名称规范化用例全部 PASS。
- `affected`：config / providers / vector / common / core / internal-logic-rag 全部 `ok`。
- `race-affected`：6 个包 `-race` 全部 `ok`，无 DATA RACE。
- `race-repeat-budget`：27 个 `--- PASS`（9 个用例 × 3 次），无 DATA RACE。
- `race-repeat-yaml`：12 个 `--- PASS`（4 个用例 × 3 次），无 DATA RACE。
- `race-repeat-strictbool`：45 个 `--- PASS`（9 个用例 × 5 次），无 DATA RACE。
- `full`：串行全量 17 个测试包 `ok`，其余无测试文件包 `[no test files]`。
- `vet`、`build`：EXIT 0，无输出。

### 环境限制记录

首次执行 `-race` 时 `core/config`、`core/providers`、`core/common` 出现
`exit status 0xc0000139`（Windows 下 cgo 测试二进制找不到 UCRT64 运行库 DLL），
属于本机工具链 PATH 问题，不是测试失败。原始日志保留为
`race-affected-cgo-dll-failed.log`。把 `C:\msys64\ucrt64\bin` 加入 `PATH` 并显式指定
`CC/CXX` 后重跑，上述三包 `-race` 全部通过（见 `race-affected.log`）。

## 用例说明

见 `TEST-NAMES.md`（逐用例映射到问题编号与验证点）。
