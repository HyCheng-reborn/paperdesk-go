# Stage 2C-3 增量复核修复（P1 版本身份 / P2 完整 fingerprint）— 验证证据

- 固定起点（baseline，本提交父提交）：`2c952ce5e4afe3096945596c9b03be1b5cf1458b`
- 该起点的父提交（上一轮 P1-1…P2-3 修复）：`c4111e031521eb372198d6b428759637cf21fee8`
- 分支：`codex/stage2c2-fingerprint`
- 独立报告：`verification/independent-2c3-2c952ce-20261028/REPORT.md`
- 运行时：go1.27.1 windows/amd64，`CGO_ENABLED=1`，CC=`C:\msys64\ucrt64\bin\gcc.exe`，`GOTMPDIR=C:\gotmp`
- 离线约束：不调用真实模型 / ES / Qdrant / MySQL；只使用内存 transport、`httptest` 与本机 fake。

> 日志里的 `CODE_SHA` 是**提交前**工作区的固定起点（`2c952ce…`）。修复提交的 SHA 以仓库
> `git log -1` 为准；提交内容与这些日志来自同一工作区。

## 复现脚本

`run.ps1`（工作目录仓库根）依次执行并写入日志：

| 日志 | 命令 |
| --- | --- |
| `focused.log` | `go test -p 1 ./core/providers ./core/providers/bailian ./core/vector ./core/common ./core ./internal/logic/rag -run '^(TestRequestModelIDRejectsVersionedIdentity\|TestP1_\|TestP2_\|TestP12_\|TestR2_\|TestNewProviderEmbedding_\|TestComputeFingerprintMeta)' -count=1 -timeout=300s -v` |
| `affected.log` | 同上包，`-p 1 -count=1 -timeout=900s` |
| `race-affected.log` | 同上包，`-p 1 -race -count=1 -timeout=1200s` |
| `race-repeat-identity.log` | `go test -race ./core/providers ./core/providers/bailian ./core/common ./core -run '<focused 正则>' -count=3 -timeout=600s -v` |
| `race-repeat-factory.log` | `go test -race ./internal/logic/rag -run '^(TestP1_\|TestP2_\|TestP13_)' -count=3 -timeout=600s -v` |
| `full.log` | `go test -p 1 ./... -count=1 -timeout=600s` |
| `vet.log` | `go vet ./...` |
| `build.log` | `go build ./...` |

## 结果（本轮全部 EXIT 0，均为 ATTEMPT 1 一次通过）

- `focused`：27 个 `--- PASS`（含子用例），0 FAIL，6 个包 `ok`。
- `affected`：6 个包全部 `ok`（无 FAIL）。
- `race-affected`：6 个包 `-race` 全部 `ok`，无 DATA RACE。
- `race-repeat-identity`：72 个 `--- PASS`（身份/指纹用例 × 3 次），4 个包 `ok`，无 DATA RACE。
- `race-repeat-factory`：12 个 `--- PASS`（4 个装配用例 × 3 次），无 DATA RACE。
- `full`：串行全量 17 个测试包 `ok`，14 个无测试文件包 `[no test files]`。
- `vet`、`build`：EXIT 0，无输出。

关键用例的重复命中次数（`-race -count=3`）：

| 用例 | 重复通过次数 |
| --- | --- |
| `TestRequestModelIDRejectsVersionedIdentity` | 3 |
| `TestP1_VersionedProfileFailsClosedForAllBuilders` | 3（另见 `focused`/`race-affected`） |
| `TestP1_WireModelEqualsBoundIdentity` | 3 |
| `TestP1_BailianBuildersFailClosedForVersionedProfile` | 3 |
| `TestP1_VersionedProviderConfigFailsClosed` | 3 |
| `TestP1_VersionedProviderEmbeddingConfigFailsClosed` | 3 |
| `TestP1_FactoryFailsClosedBeforeTouchingES` | 3 |
| `TestComputeFingerprintMetaRejectsVersionedProviderConfig` | 3 |
| `TestP2_ExpectedFingerprintUsesSharedConstructor` | 3 |
| `TestP2_DirectEntryComparesFullFingerprint` | 3 |
| `TestP2_DefaultFactoryRejectsNonCosineTarget` | 3 |
| `TestP13_FactoryPathBindsTargetForDirectEntry`（正常匹配路径） | 3 |
| `TestNewProviderEmbedding_VersionedConfigFailsClosed` / `TestR2_VersionMismatch` / `TestR2_VersionMatch` | 各 3 |

## 环境限制记录（不影响结论）

1. 首次执行时 `%TEMP%` 中的测试二进制被外部进程（杀毒/索引器）短时占用，`go` 在
   `fork/exec` 或 `unlinkat` 阶段报 `Access is denied`（此时用例本身全 PASS）。原始日志保留为
   `focused-transient-exec-denied.log`、`focused-transient-tempfile-locked.log`、
   `affected-transient-tempfile-locked.log`、`full-transient-tempfile-locked.log`。
2. 规避措施：设置 `GOTMPDIR=C:\gotmp`、对多包命令加 `-p 1` 串行，并仅在「日志中没有任何
   `--- FAIL` / `FAIL <pkg>` / `panic:`」时做最多 3 次重试（逐次记录 `ATTEMPT`）。最终
   `run.ps1` 全流程一次通过（每个日志均为 `ATTEMPT 1 EXIT: 0`）。
3. `gofmt -l` 对 `server/core/vector/fingerprint.go`、`server/core/providers/adapter_test.go`
   会报告差异，但该差异是**基线既有**的字段对齐（`allowedSimilarities` 与
   `TestBuildEmbedRole_SupportsDimensionsRequest` 的 struct 列对齐），在 `2c952ce…` 上同样存在；
   本轮新增/修改的代码块经 LF 归一化后 `gofmt` 无差异。仓库工作区为 CRLF 检出，直接对工作区
   运行 `gofmt -l` 会误报行尾差异。
4. `server/go.mod` 仅被 go 工具链规范化了 `google.golang.org/grpc` 的 `// indirect` 标注
   （该包已被 `internal/testutil` 直接导入），无版本变化、无新增依赖。

## 用例说明

见 `TEST-NAMES.md`（逐用例映射到问题编号与验证点）。
