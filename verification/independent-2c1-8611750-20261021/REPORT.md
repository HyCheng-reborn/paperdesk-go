# Stage 2C-1 fix1 再次独立复核

日期：2026-10-21，Asia/Shanghai。

**结论：存在阻塞。原 P1（合法 YAML 整数被拒绝）已闭合；另有 P2 严格配置类型校验缺口，影响本批明确要求。** 未修改验收状态。

## 对象与证据真实性

- 增量基线 1850da1fcff2e66c1ed61594139e7e2e068fd775；目标 8611750bb89dbf0de268b04a212ca873d500cb6f；codex/stage2c1-dimensions 本地 ref 与目标一致。
- git merge-base --is-ancestor 对增量基线及原累计基线 8d2c21225d4627a2668cad788cc254f78f2e04e4 均 EXIT 0。完整对象、提交和累计统计见 git-objects.log。
- git worktree list 定位现有主仓、实现树及历史审查树后，另建干净 detached 树 C:/Zhy/code/go-rag/.review-stage2c1-8611750。未切换、清理或移动其他工作树，review/stage1a 仍在原 SHA。
- 目标 server 树 e891891c8040602374db3714ce49f731e770f099，与实现者实测代码提交 562bc884bdd1a2913a0a557455de765308668719:server 相同。8611750 的证据提交未改变代码树；不将其冒充实现者当时命令 SHA。
- 增量 server 仅 3 文件：生产 dimensions.go 增 20 行，另外两文件为必要回归测试；完整增量见 incremental-server.diff。所有交付测试均在 Git 对象内，无主仓未提交文件参与。
- 上轮报告保留于 verification/independent-2c1-1850da1-20261021/REPORT.md，本报告追加而非覆盖。原依据与生产路径审查仍适用；本轮读取修复 README、环境、脚本、树对象、测试、完整解析器及文档变更。目标中仍无 AGENTS.md。

## 原 P1 闭合

解析器现在在 reflect switch 前识别 json.Number，严格检查正十进制数字及平台 int 范围。普通字符串未转为整数；直接 json.Number 的小数、指数、符号、前导零、空白和溢出均拒绝，errors.Is 错误标识保留。

新增正式测试通过实际临时 YAML 文件、默认 GoFrame AdapterFile、g.Cfg、readRagConfig、normalizeDimensions；确认实际 json.Number，覆盖 legacy/provider 768、显式 1024、缺失、同值、冲突、非法值和 InitWith 新 profile 门禁。不是复制生产解析逻辑或只用 fake getter。

我逐字复制上轮 independent_test.go，通过独立 overlay 在新干净目标重跑；SHA256 对照见 original-repro-hash.log；2 顶层 PASS / 10 RUN，EXIT 0。原合法整数拒绝问题已消失。

## 新阻塞 B2 / P2：真实 YAML 小数和指数可被转换成整数后放行

位置：server/internal/logic/rag/retriever.go:719–734，尤其 728 行直接取 v.Val；server/core/config/dimensions.go:23–41 在 json.Number 上检查语法时，原始 YAML 标量类型/写法已经丢失。测试缺口：server/internal/logic/rag/dimensions_yaml_test.go:91 的非法值只选 1.5；server/core/config/dimensions_json_test.go 的 768.0/1e3 是直接 json.Number，未经过真实 YAML 转换。

触发与独立实测结果（legacy/provider 两入口相同）：

| YAML 原值 | GoFrame 实际值 | normalizeDimensions 结果 |
|---|---|---|
| 768.0 | json.Number("768") | 768，nil error |
| 1e3 | json.Number("1000") | 1000，nil error |
| 1.0e3 | json.Number("1000") | 1000，nil error |

1.5、普通字符串、0、负数和溢出仍拒绝。新 profile 即便类型被接受，后续 InitWith 门禁仍关闭，本问题不构成 guard 绕过。legacy 路径则把这些原要求拒绝的输入当作有效维度交给工厂。

原因经锁定依赖源码核实：GoFrame gjson_api_new_load_content.go:179–180 将 YAML 经 gyaml.ToJson 转为 JSON，之后 204 行 UseNumber；gyaml.Decode 使用 yaml.Unmarshal，ToJson 再 Marshal。整数值浮点在 JSON 重编码中丢失原始 float 类型/指数词法。仅检查最终 json.Number 的字符串不能履行 YAML 严格类型契约。

证据：numeric-forms.log / numeric-observed.log，均 EXIT 1；1 顶层测试、17 RUN（含 16 子项），6 个子项失败，分别为两个入口的三种写法。后一个日志增加实际类型和值及最终维度观察；没有修改生产代码。临时源码 numeric_forms_test.go、numeric_forms_observed_test.go 和对应 overlay 均保存。

影响及严重度：P2，属于配置契约/必要证据缺口，不是普通合法配置不可用，也不是身份门禁绕过。原审查明确要求类型错误不被掩盖，本轮又明确声称拒绝小数、指数；真实 YAML float 被接受，故本批暂不能以现有契约通过。不是风格或可选重构问题。

最小修复方向（仅建议，未实施）：在原始 YAML 类型/标量信息尚存在的配置输入边界验证这两个字段，或使用能保留类型证据的受控读取路径，再交给共享正整数/冲突解析；不要仅继续收紧最终 json.Number（此时 768 与 768.0 已不可区分），也不要用 .Int()。补真实 YAML 双入口的 768.0、1e3、1.0e3 失败测试，同时保留合法整数/缺失/冲突/门禁用例。若拟改为接受数学上为整数的 float，必须显式调整并获准变更原严格类型契约，不能把当前测试通过写成既定拒绝行为已实现。

## 独立离线执行

cwd C:/Zhy/code/go-rag/.review-stage2c1-8611750/server；Go 1.27.1 windows/amd64；使用现有 MSYS2 UCRT64 GCC，进程级 PATH/CC/CXX/CGO_ENABLED=1；GOFLAGS 空；GOPROXY=off、GOSUMDB=off。完整命令见 run.ps1 与每条日志，目标 SHA、环境见 environment.log。无 online 标签、真实模型、真实 ES/Qdrant/DB 或工具链变更。Go 缓存访问使用已有授权的执行权限。

P = ./core/config ./core/common ./core/vector ./core/indexer ./core/retriever ./core ./internal/logic/rag。

| 命令 | 结果 | 实际命中 |
|---|---|---|
| go test P -run ^TestDimensions_ -count=1 -v | EXIT 0 | 21 顶层 PASS / 76 RUN |
| go test P -count=1 -v | EXIT 0 | 226 顶层 PASS / 295 RUN |
| go test -race P -count=1 -v | EXIT 0 | 226 顶层 PASS / 295 RUN；0 DATA RACE |
| go test -p 1 ./... -count=1 | EXIT 0 | 全量实际执行；no test files 不计为用例 |
| go vet ./... | EXIT 0 | — |
| go build ./... | EXIT 0 | — |
| go test -overlay <本目录>/overlay.json ./internal/logic/rag -run ^TestIndependent -count=1 -v | EXIT 0 | 原独立复现 2 顶层 PASS / 10 RUN |
| go test -overlay <本目录>/numeric-overlay.json ./internal/logic/rag -run ^TestIndependentYAMLNumericForms -count=1 -v | EXIT 1 | B2 复现，6 子项失败 |
| 同上，numeric-observed-overlay.json | EXIT 1 | 记录实际值与最终维度，6 子项失败 |

目标自带测试和临时补充层分开；实现者 verification/stage2c1-fix1 的日志也不冒充本次独立执行。没有并发生产变更或 race 迹象，因此没有机械重复 race。测试前后工作树无变更，最终状态另存 clean-final.log。

## 累计生产维度流向及保留边界

本轮修复补上真实 YAML 合法整数 → ParseEmbeddingDimensions → RagConfig 的入口；后续 normalizeDimensions → vector.Config 与 core.Config/Copy → default factories/core.New → common.NewEmbedding → 同步/异步 ES/Qdrant indexer 和 content/QA retriever 的累计路径没有变。schema 的实际主入口 Config.CreateIndex、公开 vector.CreateIndex、deprecated common ES 仍共享 ESMapping/QdrantCollection，768 的真实请求构造/写入/查询回归在本轮重新执行通过。

集中默认 1024、Provider 声明优先/显式 legacy fallback/冲突拒绝、Config.Copy 的维度及私有 Provider 快照均保持。模型响应维度检查仍供文档/查询共用。新 profile 仍由 InitWith/core.New/common.NewEmbedding 拒绝；没有生产 GetEmbedder 新调用、门禁解除或既有索引身份“补写”。创建配置正确仍不等于已验证旧资源身份；本轮不要求 fingerprint 或旧索引迁移。

预算 owner/lease、取消、错误链、资源释放及 Stage 1 生命周期相关生产文件无增量修改；受影响包 race 与全量通过作为回归证据，不宣称全面重审或真实设施验证。

## 2C-2 文档评估（仅意见）

本轮新增条款已回应上轮意见：唯一且不可复用物理名、ES alias 解析并固定物理目标/UUID、歧义/过滤/路由拒绝、Qdrant 独占管理；manifest 稳定 store namespace/集群身份、CAS owner fencing、全部必要步骤及读回后 Ready、回执丢失/部分失败/崩溃窗口隔离；禁止凭同维度或当前配置补认旧身份；覆盖公开创建与 SDK.ensureCollection；DB 不可用和恢复不一致失败关闭。

推荐接受这些作为下一批验收约束，仍需决策 ES _meta/Qdrant 关系库 manifest 分流、新表/迁移与生命周期成本、独占管理实际可保证性、fingerprint 字段及 revision/漂移 alias 证明政策。文档明确现有入口尚无这些保证，不能据此解除门禁。受控创建和唯一物理名须由实际入口强制，manifest 不能单独识别外部同名替换。未保证时 Qdrant guard 继续关闭。

不需要为 2C-1 实施这些建议；本次唯一未闭合阻塞是 B2，未开启 2C-2/2C-3/2D。

## 停止

没有修改生产代码、提交、推送、合并或更新验收状态。保留本报告和全部日志后停止。
