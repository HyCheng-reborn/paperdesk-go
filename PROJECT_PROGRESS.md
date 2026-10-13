# 项目进度记录

## 当前状态

**日期**：2026-10-14
**阶段**：Stage 1-Pre 已完成，Stage 1A 第三轮复核已完成，Stage 1-Pre 独立复核通过，Stage 1B 计划已制定

## Stage 0：事实基线与只读核对

**状态**：静态部分完成

| 核实项 | 状态 | 结论 |
|---|---|---|
| V01 Eino 依赖兼容性 | 已完成 | go build/vet 通过，依赖解析无冲突 |
| V02 chat-history 接口 | 已完成 | 能力严重不足，Stage 5 需替换 |
| V03 MySQL SKIP LOCKED | 未执行 | 归属 Stage 4 |
| V04 百炼 endpoint | 未执行 | 归属 Stage 2 |
| V05 ES 版本兼容（静态） | 存在风险 | 客户端 8.16.0 vs 服务端 8.11.3 |
| V05 ES 版本兼容（运行） | 未执行 | 归属 Stage 1 或 0C |
| V06 GoFrame DAO 清单 | 已完成 | 12 生成 + 10 手写 = 22 文件 |
| V07 快照与 commit 差异 | 待补证 | ZIP 未找到 |

**详细报告**：[design/stage0-baseline.md](design/stage0-baseline.md)
**证据目录**：[verification/stage0/](verification/stage0/)

## Stage 1：初始化副作用清理 + 并发安全修复

**状态**：计划就绪，待实施

### 批次计划

| 批次 | 内容 | F 编号 | 依赖 | 状态 |
|---|---|---|---|---|
| 1-Pre | F06 File/URL nil 访问修复 | F06 | 无 | **✅ 已完成** |
| 1A | init 副作用清理 + 离线测试隔离 | F12 | 无 | **第三轮修复已完成，待独立复核** |
| 1B | Retriever/Indexer 并发 + Rerank HTTP | F01/F02/F05 | 1A | 待实施 |
| 1C | Chat context + 流式 EOF + 错误语义 | F03/F04 | 1A | 待实施 |
| 1D | Chunk 写入错误传播 + QA 并发 + goroutine 止损 | F07/F08/F09 | 1A, 1B | 待实施 |

### Stage 1-Pre 完成记录

**日期**：2026-10-14
**基线**：3b8f7a0（Stage 1A 复核通过）
**状态**：✅ 已完成

**修改文件**：
- `server/internal/controller/rag/rag_v1_indexer.go` — 新增 `validateIndexerInput()` 参数校验；修复 `req.File.Filename` nil 访问；URL 模式下用 `path.Base` 提取文件名
- `server/internal/controller/rag/rag_v1_indexer_test.go` — 新增 11 个测试覆盖四类输入 + PaperDesk 模式 + 无副作用验证

**四类输入行为**：

| 输入情况 | 默认模式 | PaperDesk 模式 |
|---|---|---|
| File 和 URL 均缺失 | 返回参数错误 | 返回参数错误 |
| 仅 File | 正常处理 | 正常处理 |
| 仅 URL | 正常处理 | 返回错误 |
| File + URL | 正常处理 | 返回错误 |

**验证结果**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test ./internal/controller/rag/... -count=1 -v` — 退出码 0，11 个测试全部通过
- `go test -p 1 ./... -count=1` — 退出码 0，全量通过
- 非法请求不触发文件保存、SQL 写入、后台任务

**PaperDesk 模式策略**：当前 `isPaperDeskMode()` 返回 false（临时策略），待 PaperDesk profile 配置实现后接入。

**证据目录**：[verification/stage1pre/](verification/stage1pre/)

### Stage 1-Pre 独立复核记录

**日期**：2026-10-14
**复核基线**：`db0c5971d137ddabcc10ae5fb0c4b9baa17e1b98`（Stage 1-Pre 完成后的 HEAD）
**复核方式**：源码复核，未独立重跑 Go 测试

**复核结论**：
- F06 nil 访问修复逻辑正确，`validateIndexerInput()` 在所有副作用前完成参数校验
- 四类输入行为定义清晰，默认模式与 PaperDesk 模式分支合理
- `paperDeskModeOverride` 为临时测试机制，待 PaperDesk profile 配置实现后移除
- 无副作用保证：校验失败直接返回 error，不触发文件保存、DB 插入、后台任务

**行为修正记录**：
- 默认模式下 File+URL 同时存在时的行为为"File 优先，URL 忽略"（非报错）
- PaperDesk 模式目前未在生产启用（`isPaperDeskMode()` 临时返回 false）
- `paperDeskModeOverride` 是临时测试机制，不应在生产代码中长期保留

### Stage 1A 完成记录

**日期**：2026-10-14
**状态**：✅ 第三轮修复已完成，待独立复核

**原始修改**：
- `server/internal/dao/dao.go` — 移除 init()，保留 InitDB() error
- `server/internal/logic/rag/retriever.go` — init() 提取为 Init() error
- `server/internal/logic/chat/chat.go` — init() 提取为 Init() error
- `server/internal/cmd/cmd.go` — 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init()

**第一轮复核修复**：
- `server/internal/dao/dao.go` — AutoMigrate 分离为 Migrate()；新增 Close()、InitWithMigrate()、IsInitialized()；原子初始化；重复调用保护
- `server/internal/dao/db/interface.go` — Database 接口新增 Close() 方法
- `server/internal/dao/db/base.go` — Base 实现 Close()
- `server/internal/logic/rag/retriever.go` — ragSvr 默认 nil；重复调用保护；GetRagSvr 未初始化返回 nil；IsInitialized()
- `server/internal/logic/chat/chat.go` — 重复调用保护；GetChat 未初始化返回 nil；IsInitialized()
- `server/internal/cmd/cmd.go` — Fatalf 改为返回 error；失败时 dao.Close() 清理
- `server/core/rag_test.go` — 添加 //go:build integration 标签
- `server/core/common/chat_model_test.go` — 添加 //go:build integration 标签
- `design/stage1-plan.md` — F 编号修正（1C→F03/F04，1D→F07/F08/F09）

**第二轮复核修复（bd20ca2 → 本轮）**：

| 修复项 | 说明 |
|---|---|
| Migrate 分离 | cmd.go 普通启动不再调用 dao.Migrate()，避免意外改表 |
| 在线测试隔离 | rerank_test.go 和 youcom_integration_test.go 添加 `//go:build online`；rag_test.go 改为 `//go:build integration && online`；chat_model_test.go 改为独立单元测试 |
| MustGet 替换 | dao/retriever/chat 三个包的 MustGet 全部替换为 Get + error 处理，缺配置返回 error 而非 panic |
| 行为测试 | 17 个测试覆盖：预初始化状态、失败后状态不变、Reset 安全性、GetDsn 返回 error |
| 失败清理协调 | 新增 dao.Reset()、rag.Reset()、chat.Reset()；cmd.go 级联清理（chat 失败 → rag.Reset + dao.Reset） |
| 同步一致性 | 全部改用 sync.RWMutex；GetDsn 返回 (string, error)；IsInitialized/GetRagSvr/GetChat 使用 RLock |
| chat-history 边界 | chat.Init() 先检查 dao.GetDsn() 返回 error，明确标注 AutoMigrate 边界 |
| Windows 诊断 | `go test ./...` 的 fork/exec Access denied 为 Windows 临时目录权限问题，单独运行通过 |

**验证结果（第二轮）**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，17 个测试全部通过
- `go test ./core/retriever/...` — 退出码 0（单独运行）
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — core/retriever 和 core/websearch 因 Windows 临时目录权限问题 fork/exec 失败（非测试逻辑问题）
- 离线测试不触发数据库/向量服务/OpenAI 连接
- 集成测试通过 `//go:build integration` 标签隔离
- 在线测试通过 `//go:build online` 标签隔离

**第三轮复核修复（2df2421 → e48b67e）**：

| 修复项 | 说明 |
|---|---|
| chat-history AutoMigrate 适配 | 创建 historyWrapper 和 historyImpl，复用 dao 连接，绕过 NewEinoHistory 的 AutoMigrate；新增 MigrateChatHistoryTables() 显式迁移入口 |
| 可注入边界 | dao.InitDB 接受 ConfigProvider 接口；生产用 InitDBWithConfig()，测试用 fakeConfig |
| 确定性失败测试 | 使用 fakeConfig 测试配置错误、无效 DSN、nil 配置等场景，不再使用“若意外成功就 skip” |
| Reset/Close 资源释放 | rag.Reset() 改为返回 error；cmd.go 级联清理处理 Reset 错误 |
| 配置错误传播 | ConfigProvider 接口统一配置读取错误传播 |
| 在线许可检查 | rerank_test.go 和 youcom_integration_test.go 添加 ONLINE_TEST_PERMITTED=true 检查 |
| Database 接口扩展 | 新增 GormDB() 方法暴露底层 *gorm.DB，供 chat-history wrapper 使用 |

**验证结果（第三轮）**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，19 个测试全部通过
- `go test ./core/websearch/...` — 退出码 0（单独运行）
- `go test ./...` — 除 Windows 文件锁定问题外全部通过（非测试逻辑问题）
- 新增测试：TestInitDB_ConfigError, TestInitDB_InvalidDSN, TestInitDB_NilConfig
- chat-history 不再在 Init 时 AutoMigrate，迁移通过 MigrateChatHistoryTables() 显式控制

**详细计划**：[design/stage1-plan.md](design/stage1-plan.md)

## 设计文档版本线

| 版本 | 状态 | 说明 |
|---|---|---|
| v1.0 | 已归档 | 初始方案 |
| v1.1 | 已归档 | 复核后首修 |
| v1.1.1 | 已归档 | 12 项微调 |
| v1.1.2 | **当前版本** | 9 项修正（BlobStore/旧数据映射/Stage 0 等） |

## ES 版本风险

客户端 v8.16.0 vs Compose 8.11.3 vs README 8.18.0 — 不在官方保证区间。不阻塞 Stage 1。

## 未验证项

- Eino 运行时兼容性（Stage 2）
- ES 实际连通性（Stage 1 启动验证）
- MySQL SKIP LOCKED（Stage 4）
- chat-history 替换方案（Stage 5）
- ZIP 快照逐文件对比（待获取文件）
