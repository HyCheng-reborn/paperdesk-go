# Stage 2C-1 P1 修复交付

状态：修复完成，待再次独立复核。2C/Stage 2 未整体完成，2C-2 仍仅为设计建议。

修复基线：`1850da1fcff2e66c1ed61594139e7e2e068fd775`。
被验证的干净代码提交：`562bc884bdd1a2913a0a557455de765308668719`。
实现分支：`codex/stage2c1-dimensions`；工作目录：`C:/Users/zhy/.codex/worktrees/stage2c1-dimensions/go-rag/server`。
最终交付会追加本目录证据；server 树与被验证提交完全相同，树对象见 server-tree.txt。不把后续证据提交 SHA 冒充命令执行时的代码 SHA。

## 生产修复

仅修改 `server/core/config/dimensions.go` 的解析入口：显式识别 `encoding/json.Number`；只接受不带符号/空白/前导零的正十进制整数，并按 `strconv.IntSize` 检查平台 int 范围。普通字符串仍拒绝；json.Number 小数/指数、零/负数、空值、溢出同样拒绝。没有 `.Int()` 宽松转换，没有改优先级、默认或其他生产装配代码。

以前的测试使用 Go int mock，没有覆盖 GoFrame 文件适配器返回 json.Number 的真实表示。本轮新增正式提交内测试：
- `core/config/dimensions_json_test.go`：合法 JSONNumber/平台最大 int；空、符号、前导零、空白、小数、指数、NaN/Inf、零/负数、边界溢出；普通字符串保持拒绝。
- `internal/logic/rag/dimensions_yaml_test.go`：写实际临时 YAML 文件，经锁定 GoFrame 默认 AdapterFile → g.Cfg → readRagConfig → normalizeDimensions，明确断言实际输入是 json.Number，验证 legacy/provider 768、显式 1024、缺失默认、同值、冲突、双入口非法值。Provider 合法配置继续由 InitWith 门禁阻断且不调用设施工厂。测试不并行，结束恢复原 adapter，不替换生产解析函数。

common.NewEmbedding、InitWith/core.New 门禁均保留，未启用新 Embedding profile。没有改 schema/indexer/retriever、预算 owner/lease、流/取消架构或依赖，没有实施 fingerprint/迁移/真实索引操作。

## 证据层与结果

环境见 environment.log：Go 1.27.1 windows/amd64，现有 MSYS2 UCRT64 GCC 16.2.0，进程级 PATH/CC/CXX/CGO_ENABLED=1，GOFLAGS 空、GOPROXY/GOSUMDB=off。命令完整 cwd、代码 SHA、时间、退出码见各日志；run.ps1 可重跑，TEST-NAMES.md 为实际命中。

| 层级/命令 | 日志 | 退出码与命中 |
|---|---|---|
| 基线 1850da1 + 新回归测试（修改解析器前） | repro-before.log | EXIT 1；合法整数/冲突因 json.Number 误拒绝 |
| 修复提交：7 个受影响包 ^TestDimensions_ -count=1 -v | focused.log | EXIT 0；21 顶层 PASS / 76 RUN |
| 修复提交：7 个受影响包普通 -count=1 | affected.log | EXIT 0 |
| 修复提交：同范围 -race -count=1 -v | race-affected.log | EXIT 0；226 顶层 PASS / 295 RUN，0 DATA RACE |
| 修复提交 + 独立原复现 overlay，^TestIndependent -count=1 -v | independent-original.log | EXIT 0；2 顶层 PASS / 10 RUN |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | EXIT 0 |
| go vet ./... | vet.log | EXIT 0 |
| go build ./... | build.log | EXIT 0 |

受影响包为 core/config、common、vector、indexer、retriever、core、internal/logic/rag。本轮无生产并发改动或 DATA RACE，不机械重复历史十轮。命令非 verbose 时不编造测试数量；定点个别无命中包不冒充实际命中。

独立原复现来源：`C:/Zhy/code/go-rag/verification/independent-2c1-1850da1-20261021/independent_test.go`，原文件未改，逐字复制为本目录 independent-original_test.go.txt，哈希见 original-repro-hash.txt。overlay 仅补充层，不算目标自带测试；本批正式 YAML 回归已提交，干净测试不依赖原仓未跟踪文件。runner 会按实际 worktree 重建 overlay 的绝对路径。

全部离线；只使用现有本机测试 fixture、临时文件和既有测试数据库，无真实 DB/ES/Qdrant 或模型 API/在线探针，无系统工具链改动。

## 2C-2 文档边界

方案 §5.8 仅明确后续验收约束：manifest 双写/回执丢失/崩溃恢复、owner fencing、全部必要步骤后 Ready、未知资源隔离不补身份；唯一物理名、alias 解析后固定物理目标、过滤/路由或多目标歧义拒绝；数据库不可用/备份恢复不一致失败关闭。现有入口尚未保证这些条件，2C-2 须控制所有创建点和 SDK ensureCollection，不能凭文档宣称已实现。未新增 manifest 表、迁移或恢复代码。

## 工作区与交付

复用独立实现 worktree/分支。主仓 review/stage1a 和他人 retriever_test.go 等改动、两个独立审查树、历史日志全部保持。没有清理/覆盖主仓或审查文件，没有强推或合并 main。正常追加提交并推送同一独立实现分支；仅交付本批修复、必要测试、文档和新证据，等待独立复核。
