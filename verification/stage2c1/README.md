# Stage 2C-1 实施交付与离线证据

状态：**实施完成，待独立复核**。2C-2 仅设计建议待确认，2C/Stage 2 均未整体完成。

交接基线：`8d2c21225d4627a2668cad788cc254f78f2e04e4`。
干净代码/测试提交（以下验证对象）：`5bb15e8eb7c17070eaa29203efcf58fb008c0b53`。
实际分支：`codex/stage2c1-dimensions`。
实际实现 worktree：`C:/Users/zhy/.codex/worktrees/stage2c1-dimensions/go-rag`。
验证后只追加本目录证据及方案文字核正，server 树没有变；server-tree.txt 记录被验证代码/测试树对象，可与最终交付 tip 的 server 树核对。不把后续证据提交的 SHA 冒充当时命令的代码 SHA。

## 生产流向与范围

唯一解析边界 `core/config/dimensions.go`：
- 旧模式读取 embedding.dimensions；缺失默认 1024，仅此一个默认常量。
- 新 Provider Embedding 优先 provider.embedding_dimensions；若缺失，允许显式 legacy 值作为 fallback；二者冲突拒绝，两者均缺失拒绝。只有 Chat 等其他角色的 Provider 不改变旧 Embedding 来源；孤立的 provider 维度字段报错。
- 配置读取使用共享严格解析：显式 0、负数、浮点、字符串、布尔、不可表示整数拒绝。Go 结构零表示未指定。ErrEmbeddingDimensions / ErrEmbeddingDimensionConflict 均可 errors.Is 识别。
- RagConfig → normalizeDimensions（在任何工厂调用前）→ vector.Config 与 core.Config；Copy 保留，Provider 用私有快照；VectorCfg 的显式冲突不覆盖。
- defaultRagFactory → buildRegistry（新 Profile 声明使用同一维度，无散落默认）/ core.New。core.New 先校验配置及新 profile 门禁，再 IndexExists/CreateIndex。
- 实际创建主路径 Config.CreateIndex 与 vector 公共路径、common deprecated ES 路径共用 config.ESMapping / QdrantCollection。schema builder 只接受解析好的正值，不选择默认。
- 同步/异步 Qdrant indexer VectorDim、ES/Qdrant 内容与 QA query Embedding 共用同一配置。SDK 的 Qdrant ensureCollection 路径也收到该值。
- legacy 支持可选尺寸的模型按既有前缀规则发送解析值；其余模型保留原协议，不擅自增加参数。所有 legacy query/document 响应校验数量和实际维度，防止固定尺寸模型与配置不一致后继续写入/搜索。

没有迁移或读取真实用户索引，没有实现 fingerprint 持久化/比较，没有改 Provider/预算/流架构或依赖。

## 验证

MSYS2 UCRT64 仅进程级 PATH/CC/CXX/CGO_ENABLED；Go 1.27.1 windows/amd64，GCC 16.2.0，GOFLAGS 空，GOPROXY/GOSUMDB=off。完整信息见 environment.log。
所有命令实际 cwd 为实现 worktree 的 server/，完整命令、代码 SHA、时间与退出码见对应日志和 run.ps1。

| 项目 | 日志 | 退出码 | 实际覆盖 |
|---|---|---|---|
| 定点 ^TestDimensions_ -count=1 -v | focused.log | 0 | 18 顶层 PASS，41 RUN（含子项） |
| 7 个受影响包普通 -count=1 | affected.log | 0 | config/common/vector/indexer/retriever/core/logic rag |
| 同上受影响包 -race -count=1 -v | race-affected.log | 0 | 223 顶层 PASS，260 RUN，无 DATA RACE |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | 0 | 串行全量实际执行 |
| go vet ./... | vet.log | 0 | — |
| go build ./... | build.log | 0 | — |

TEST-NAMES.md 列出真实命中。没有生产并发编排修改，因此不为本批额外重复历史十轮压测；不将其他历史包 race 状态自动改为通过。

定点证据：
- 768 维真实 ES CreateIndex 请求覆盖 Config 与 vector 两条路径；deprecated common builder 也由真实本机 HTTP 验证。
- 768 维 Qdrant CreateCollection/FieldIndex 经内存 bufconn gRPC fake，覆盖 Config 与 vector 两条路径，两个 named vector 的 schema 均检查。
- 同步/异步 ES SDK Store 的实际 bulk 向量为 768；同步/异步 Qdrant SDK 构造与自定义 Store 的 VectorDim/实际 upsert 为 768。
- 实际 ES script_score 查询、Qdrant 内容/QA 查询向量为 768，Embedding 经过真实锁定 SDK + 本机 HTTP；没有测试复制生产解析或 schema。
- 默认工厂实际装配、Config.Copy、InitWith 工厂传递/不修改调用者快照、默认与非法/冲突配置、新 profile 门禁及失败前不调用工厂均覆盖。
- 无真实模型/DB/ES/Qdrant 连接或在线探针。Qdrant fixture 使用 127.0.0.1 resolver + 自定义 bufconn dialer，所有 RPC 由内存 fake 处理。

开发期曾因内存 gRPC fixture 使用非地址 host `offline` 导致 resolver 无地址，未连接到真实 Qdrant；修正为 loopback resolver 后，全部正式验证通过。首次编码脚本的字符编码错误在正式提交前已修正。不会将这些开发失败当作最终通过证据，也没有为此调整系统或工具链。

## 保留门禁与未验证项

common.NewEmbedding 的 ErrEmbeddingGuardNotReady 保留；InitWith/core.New 提前 fail closed，维度贯通不等于模型身份已验证。未自动重建、删改或给未知旧索引补身份。真实 ES/Qdrant 映射往返、真实模型维度/协议/usage、A-4 和 SQL driver 取消等仍在既有边界外。

2C-2 推荐与待确认决策见 design/stage2-plan.md §5.8：ES mapping _meta；锁定 Qdrant 1.15.2 下关系库 manifest，绑定受控唯一物理名/generation，明确外部同名重建的未解决约束。若部署不能保证独占管理与不复用物理名，则 Qdrant guard 继续关闭，需另行确认原生 metadata/升级方案。未实施此建议，不自动授权后续批次或在线 2D。

## 工作区保护与交付策略

原仓 HEAD 为交接基线，存在他人未提交源文件及历史独立证据。实现从该基线新建 managed worktree，不使用独立审查 worktree，不复制原脏目录。原 retriever_test.go、config_test.go 等他人修改未纳入；必要测试另建 dimensions_test.go。没有 checkout/reset/clean/stash 主仓，没有更改 Git 配置、远端、行尾设置或合并 main。

review/stage1a 仍由带他人修改的主工作区持有，故选择推送独立 codex/stage2c1-dimensions 分支，不强行移动主分支或覆盖文件。历史 independent-* 证据不改动。本目录是本批实现证据，不冒充独立验收报告。
