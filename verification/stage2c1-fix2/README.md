# Stage 2C-1 P2 原始 YAML 类型修复

状态：修复完成，待再次独立复核；原 P1 保持修复。2C/Stage 2 未整体完成。

增量基线：`8611750bb89dbf0de268b04a212ca873d500cb6f`。
本轮实测的干净代码/测试提交：`62aeb772e38e56dbda8c70d2c47070e663df28e4`。
分支 `codex/stage2c1-dimensions`；实现目录 `C:/Users/zhy/.codex/worktrees/stage2c1-dimensions/go-rag`。
最终追加证据提交不改 server 树，树对象见 server-tree.txt。所有命令 cwd 是该实现目录的 server/。

## 原因与修复范围

GoFrame 的 YAML→JSON 转换会将 768.0/1e3/1.0e3 归一化为整数 json.Number，仅检查该字符串不能证明 YAML 原始标量是整数。本轮没有继续收紧最终数字猜测来源，没有将契约改为“数学上为整数的浮点也接受”。

新增 dimensions_source.go；readEmbeddingDimensions 在共享严格解析后，对生产 defaultConfigGetter 的默认 AdapterFile 另核查原始来源：
1. 通过 GetFileName/GetContent/GetFilePath/gres 公开 API，遵循 SetContent→已解析资源路径→磁盘的源优先级，不用 unsafe 或读取 SDK 私有字段。
2. 使用锁定 gyaml.Decode 保留原始 YAML float 类型，不做 YAML→JSON 重编码；JSON 正整数兼容，TOML 用锁定 gtoml.Decode 保留类型。没有增加或升级依赖。
3. 只检查 embedding.dimensions、provider.embedding_dimensions。float 类型（含整数值小数、指数）进入共享解析器后拒绝，错误可 errors.Is(ErrEmbeddingDimensions) 识别；原始值与 GoFrame 有效值不同返回 ErrEmbeddingDimensionConflict，不静默采用缓存/AdapterFile.Set 维度覆盖。
4. 缺失默认、合法整数、显式冲突、原有程序化配置保持。schema、索引/检索、Config.Copy、Profile、预算 owner/lease、流及取消实现均无本轮修改。

边界：非文件适配器只暴露已类型化数据，没有可恢复的原始 YAML 信息，继续共享 typed-value 解析契约；若自定义适配器自行转换原始文本，应由其输入边界保留/验证类型。本批没有新增非文件 YAML 生产入口，也不声称可从丢失类型的任意 opaque adapter 恢复原文本。实际仓内默认文件入口已覆盖。内嵌资源读取沿公开 API 实现，本轮没有另行构建嵌入资源二进制做往返集成。

common.NewEmbedding、InitWith/core.New 门禁保持；没有开始 fingerprint/迁移、真实设施或在线 2D。

## 提交内回归

在原实际 YAML 文件链测试中加入 legacy/provider 各自的 768.0、1e3、1.0e3。另覆盖 SetContent 两入口、YAML alias/merge 的合法整数与相同浮点边界、JSON/TOML 正整数兼容、源值/有效值不一致拒绝。

测试经过默认 AdapterFile → g.Cfg → readRagConfig → normalizeDimensions/原有门禁，不复制生产解析。原 P1 合法 YAML 与原有维度请求/写入/查询全部回归。

开发期新增 SetContent fixture 最初使用框架不能自动识别的 flow-only YAML；已改为其支持的块式 YAML，再验证真实类型拒绝。最终测试断言检查 ErrEmbeddingDimensions，不将上游格式识别错误算作正确拒绝。

## 实测证据分层

环境见 environment.log：Go 1.27.1 windows/amd64、现有 MSYS2 UCRT64 GCC 16.2.0，仅进程 PATH/CC/CXX/CGO_ENABLED=1；GOFLAGS 空、GOPROXY/GOSUMDB=off。完整 SHA、命令、时间、退出码见对应日志及 run.ps1；实际测试名见 TEST-NAMES.md。

受影响包 P：./core/config ./core/common ./core/vector ./core/indexer ./core/retriever ./core ./internal/logic/rag。

| 层级/命令 | 日志 | 实际结果 |
|---|---|---|
| 基线 8611750 + 新 YAML 三种边界（修复生产代码前） | repro-before.log | EXIT 1，双入口共六个子项失败 |
| 修复提交 go test P -run ^TestDimensions_ -count=1 -timeout=180s -v | focused.log | EXIT 0，25 顶层 PASS / 108 RUN |
| 修复提交 go test P -count=1 -timeout=180s | affected.log | EXIT 0 |
| 修复提交 go test -race P -count=1 -timeout=180s -v | race-affected.log | EXIT 0，230 顶层 PASS / 327 RUN，0 DATA RACE |
| 修复提交＋独立原 P1/P2 两份源文件 overlay，^TestIndependent -count=1 -timeout=120s -v | independent-original.log | EXIT 0，3 顶层 PASS / 27 RUN |
| go test -p 1 ./... -count=1 -timeout=180s | full.log | EXIT 0 |
| go vet ./... | vet.log | EXIT 0 |
| go build ./... | build.log | EXIT 0 |

没有新增生产并发编排或 DATA RACE，不重复无关历史十轮压测。非 verbose 命令不伪造用例数量；no tests to run 不作覆盖证明。全部离线，没有调用模型 API 或连接真实 DB/ES/Qdrant。

独立补充来源为原报告目录 verification/independent-2c1-8611750-20261021 的 independent_test.go 与 numeric_forms_test.go；原文件未改，逐字复制保存为本目录两个 *.go.txt，哈希见 repro-hashes.txt。overlay 为补充层，不算目标自带测试。正式 YAML 回归已进入代码提交，不依赖原仓未跟踪文件。runner 每次按当前 worktree 重建 overlay 绝对路径。

## 交付与停止边界

仅在原独立实现 worktree/分支追加本批生产修复、必要测试、状态说明及证据。主仓 review/stage1a、他人改动、历史审查树和报告未操作；不强推、不合并 main。2C-2 的存储/运维决策仍待确认，未新增 manifest 表或迁移/恢复代码，不解除 guard、不宣布本批独立复核通过。完成后停止等待复核。
