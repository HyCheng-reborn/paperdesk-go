# Stage 2C-1 fix2 独立复核报告

日期：2026-10-21，Asia/Shanghai。

**结论：通过（限 Stage 2C-1 维度配置单源贯通的既定离线范围）。** 原 P1 和 P2 均闭合，本轮未发现剩余本批阻塞。没有修改验收状态，不表示 2C 或整个 Stage 2 完成。

## Git 对象及工作树

- 增量基线：8611750bb89dbf0de268b04a212ca873d500cb6f。
- 被复核完整目标：2dc0b825497ae5116827561861474f40720503a4；codex/stage2c1-dimensions 本地 ref 指向同一对象。
- 原累计基线：8d2c21225d4627a2668cad788cc254f78f2e04e4。增量基线、原累计基线对目标的 git merge-base --is-ancestor 均 EXIT 0。
- 目标 server 树：7567eb7a7a7e2d0b3297623827944bf436a1a48b，与实现者实测代码提交 62aeb772e38e56dbda8c70d2c47070e663df28e4:server 相同。不将最后证据提交冒充实现者命令当时的 SHA。
- 先用 git worktree list --porcelain 定位所有现有工作树，再新建独立 detached 干净树 C:/Zhy/code/go-rag/.review-stage2c1-2dc0b82；主仓仍为 review/stage1a 原 SHA，未 checkout/reset/clean/stash 或移动实现树/历史审查树。
- git-objects.log 记录完整对象、祖先关系、累计 diff 统计和增量提交。incremental-server.diff 保存本轮源码/测试 diff。生产增量仅 dimensions_source.go 与 retriever.go 的接线，其余 server 变动为已提交 YAML 回归测试。不采用主仓未提交内容。
- 目标中仍未发现 AGENTS.md；已沿用此前适用祖先检查。读取修复 README、脚本/环境/树证据、PROJECT_PROGRESS/CHANGES 变更、stage2-plan §5.7/完整 §5.8，以及新增生产逻辑和测试。此前累计生产路径审查见独立 1850da1、8611750 报告；本轮对累计路径的提交内实测重新执行。

## 问题闭合及源码判断

原 P1：合法 YAML 整数经 GoFrame 变为 json.Number 的情况仍由共享严格解析器正确支持；原独立复现通过，没有回归合法 768/显式 1024/缺失默认。

原 P2：defaultConfigGetter 在共享 typed-value 解析后调用 verifyFileDimensionSource。对于生产默认 AdapterFile，使用 GetFileName/GetContent/GetFilePath/gres 公开 API 获取原始来源；与锁定 GoFrame getJson 的 SetContent → 资源 → 磁盘优先级相符。gyaml.Decode 保留原始 float 类型，进入共享解析器后拒绝 768.0/1e3/1.0e3；不再把 JSON 重编码后整数形式当成原文类型证明。JSON/TOML 正整数兼容回归通过。

来源/有效值不一致返回可 errors.Is 识别的 ErrEmbeddingDimensionConflict；非法类型、无法读取或解码来源返回 ErrEmbeddingDimensions；不以默认值掩盖。校验位于 readRagConfig 内，早于 InitWith 工厂及设施副作用。

已检查提交内测试调用实际临时文件/SetContent、默认 AdapterFile、g.Cfg、readRagConfig 和 normalizeDimensions/门禁，没有复制生产解析实现。alias/merge 使用同一锁定 YAML 解码语义。原两份独立复现逐字复制（哈希见 repro-hashes.log）并通过 overlay 重跑：3 顶层 PASS / 27 RUN，EXIT 0。

独立补充 source_precedence_test.go 还验证：磁盘为 512、SetContent 为 768 时正确采用后者；直接标量 alias 的整数通过、浮点拒绝；merge 后显式整数覆盖浮点通过，显式浮点覆盖整数拒绝；省略扩展名的文件查找仍可读取合法维度。5 子项全部通过。临时源码与 overlay 仅保存在本报告目录，未写入目标树。

未发现其他本批阻塞，无需列出最小修复方向；没有风格或可选重构阻塞项。

## 独立离线实测

实际 cwd：C:/Zhy/code/go-rag/.review-stage2c1-2dc0b82/server。
Go 1.27.1 windows/amd64；CGO_ENABLED=1，现有 C:/msys64/ucrt64/bin/gcc.exe、g++.exe，仅进程级 PATH/CC/CXX；GOFLAGS 空，GOPROXY=off、GOSUMDB=off。未修改系统工具链。

P = ./core/config ./core/common ./core/vector ./core/indexer ./core/retriever ./core ./internal/logic/rag。

| 完整命令形式 | 日志 | 结果/命中 |
|---|---|---|
| go test P -run ^TestDimensions_ -count=1 -v | focused.log | EXIT 0；25 顶层 PASS / 108 RUN |
| go test P -count=1 -v | affected.log | EXIT 0；230 顶层 PASS / 327 RUN |
| go test -race P -count=1 -v | race.log | EXIT 0；230 顶层 PASS / 327 RUN，0 DATA RACE |
| go test -p 1 ./... -count=1 | full.log | EXIT 0；全量实际执行，no-test 包不计作测试用例 |
| go vet ./... | vet.log | EXIT 0 |
| go build ./... | build.log | EXIT 0 |
| go test -overlay <本目录>/overlay.json ./internal/logic/rag -run ^TestIndependent -count=1 -v | original-repros.log | EXIT 0；原 P1/P2，3 顶层 PASS / 27 RUN |
| go test -overlay <本目录>/source-overlay.json ./internal/logic/rag -run ^TestIndependentSource -count=1 -v | source-precedence.log | EXIT 0；1 顶层 PASS / 6 RUN |

run.ps1 展开 P 并保存精确命令；各日志含目标 SHA、GOFLAGS、命令和退出码；environment.log 记录实际树和 cwd，counts.log 汇总命中。完整 run 直接在干净目标执行；overlay 是另列的补充层，不当作目标自带测试。实现者 verification/stage2c1-fix2 日志仅用于溯源，不冒充独立 PASS。

全部离线，不运行 online/integration 标签，不调用真实模型、不连接真实 ES/Qdrant/DB。已有单测自身本地 fixture 范围不变。无新增生产并发编排、无 DATA RACE，因此未机械重复全部 race。最终 git status --porcelain 空，记录 clean-final.log；空管道可能不生成 runner 的 clean-before/after.log，不能把不存在的日志作为证据。

## 累计生产维度流向

实际链：默认配置文件 → readRagConfig/readEmbeddingDimensions（共享严格解析 + 原始文件类型核查）→ normalizeDimensions → RagConfig 与 vector.Config/core.Config → Config.Copy/Provider 私有快照 → default factories/core.New → 同步和异步 Indexer、content/QA Retriever → Embedding 请求与响应维度验证、ES/Qdrant schema。

- 旧模式仅缺失时在集中 ResolveEmbeddingDimensions 默认 1024。Provider Embedding 声明优先、仅显式 legacy fallback、冲突拒绝、新模型两处缺失拒绝；不同实例不覆盖共享 Provider 输入。
- 实际建索引主入口仍是 core.New → Config.IndexExists/CreateIndex；vector 公开 CreateIndex 及 deprecated common ES 兼容入口共用 ESMapping/QdrantCollection。common 兼容入口无仓内生产调用者；不漏计公开 builder，也不把所有 1024 机械视为硬编码问题。
- 同步/异步 Qdrant VectorDim 进入锁定 SDK.ensureCollection；实际 StoreWithNamedVectors 以及 ES bulk 与查询均使用同一配置来源的 embedder。支持可选尺寸的旧模型前缀发送解析尺寸；其他旧模型保留协议，以返回长度验证阻断不一致的写入/查询。提交内 768 真实请求/写入/查询测试本轮重新通过。
- common.NewEmbedding 的新 profile 门禁保留，InitWith/core.New 提前 fail-closed；业务 indexer/retriever 仍必经该门禁，无新增 GetEmbedder 生产绕行。Profile 维度声明不等于允许启用新模型。
- Config.Copy、schema/indexer/retriever、预算 owner/lease、取消和 Stage 1 生命周期无本轮生产改动；受影响包 race 和全量回归通过。不借此宣称全面重审 2A/2B 或真实设施取消验证。

## 明确的通过边界

1. 本批仅“维度配置单源贯通”；没有 fingerprint 持久化、旧资源身份验证、索引迁移或在线探针。创建参数正确不等于已证明既有索引身份；同维度不等于同模型，没有自动删除/重建/修改旧索引或补身份。
2. 默认文件生产入口覆盖原始 YAML 类型；非文件适配器只承诺共享 typed-value 契约。本仓未新增非文件 YAML 生产入口；自定义 adapter 如果先丢失原始类型，不能指望最终数字恢复词法证明。该边界在方案和实现证据中明确，不把任意 opaque adapter 说成已验证。
3. 内嵌资源取源路径按公开 API 与锁定 SDK 源码核查，本轮未构建嵌入资源二进制做集成往返；真实 ES/Qdrant mapping/模型响应/SQL driver 取消仍未验证。上述既有边界不阻塞本批离线验收。

## 2C-2 建议（只评估）

完整 §5.8 的方向可接受为下一批设计约束：ES mapping _meta 与资源一起创建/读取但可变，应限制覆盖权限；Qdrant 在锁定 SDK 下选关系库 manifest 是有条件方案，代价包括新表、迁移、双写、崩溃恢复和备份一致性，不能当成跨设施事务。

当前文档已要求：唯一且不可复用物理名、固定 alias 解析后的物理目标/UUID、歧义/过滤/路由拒绝、受控所有创建入口（包括 SDK.ensureCollection）；owner fencing/CAS、全部 schema/payload 必需步骤完成后 Ready；超时/回执丢失/部分失败/身份缺失/恢复不一致失败关闭，禁止按当前配置认领旧资源。建议接受这些规则。

仍需明确决策：是否接受 ES/Qdrant 存储分流和 manifest 新表/生命周期成本；部署能否保证独占管理、不复用物理名与一致恢复；fingerprint v1 字段、immutable revision/漂移 alias 与私有 deployment namespace 的证明规则。manifest 本身无法识别外部同名删除重建，无法保证约束时 Qdrant guard 继续关闭。现有入口尚未实现这些保证，下一批须由实际代码和独立测试证明，不能凭文档解除门禁。

未实施这些建议不阻塞正确的 2C-1；本报告不授权 2C-2/2C-3/2D、真实迁移或付费探针。详细方案取舍和上轮依据保留在前两轮独立报告，不覆盖。

## 停止

本轮没有修改生产代码、提交、推送、合并、更新 PROJECT_PROGRESS/CHANGES 或验收状态。保存本报告及新日志后停止。2C/Stage 2 未整体完成。
