# 项目进度记录

## 当前状态

**日期**：2026-10-13
**阶段**：Stage 1A 复核修复已完成

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
| 1-Pre | F06 File/URL nil 访问修复 | F06 | 无 | 待实施 |
| 1A | init 副作用清理 + 离线测试隔离 | F12 | 无 | **复核修复已完成** |
| 1B | Retriever/Indexer 并发 + Rerank HTTP | F01/F02/F05 | 1A | 待实施 |
| 1C | Chat context + 流式 EOF + 错误语义 | F03/F04 | 1A | 待实施 |
| 1D | Chunk 写入错误传播 + QA 并发 + goroutine 止损 | F07/F08/F09 | 1A, 1B | 待实施 |

### Stage 1A 完成记录

**日期**：2026-10-13
**状态**：✅ 复核修复已完成

**原始修改**：
- `server/internal/dao/dao.go` — 移除 init()，保留 InitDB() error
- `server/internal/logic/rag/retriever.go` — init() 提取为 Init() error
- `server/internal/logic/chat/chat.go` — init() 提取为 Init() error
- `server/internal/cmd/cmd.go` — 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init()

**复核修复**：
- `server/internal/dao/dao.go` — AutoMigrate 分离为 Migrate()；新增 Close()、InitWithMigrate()、IsInitialized()；原子初始化；重复调用保护
- `server/internal/dao/db/interface.go` — Database 接口新增 Close() 方法
- `server/internal/dao/db/base.go` — Base 实现 Close()
- `server/internal/logic/rag/retriever.go` — ragSvr 默认 nil；重复调用保护；GetRagSvr 未初始化返回 nil；IsInitialized()
- `server/internal/logic/chat/chat.go` — 重复调用保护；GetChat 未初始化返回 nil；IsInitialized()
- `server/internal/cmd/cmd.go` — Fatalf 改为返回 error；失败时 dao.Close() 清理
- `server/core/rag_test.go` — 添加 //go:build integration 标签
- `server/core/common/chat_model_test.go` — 添加 //go:build integration 标签
- `design/stage1-plan.md` — F 编号修正（1C→F03/F04，1D→F07/F08/F09）

**新增文件**：
- `server/internal/dao/dao_test.go` — 验证 db 在 InitDB() 前为 nil，GetDsn/Close/IsInitialized/Migrate 未初始化行为
- `server/internal/logic/rag/retriever_test.go` — 验证 ragSvr 在 Init() 前为 nil，GetRagSvr/IsInitialized 行为
- `server/internal/logic/chat/chat_test.go` — 验证 chat 在 Init() 前为 nil，GetChat/IsInitialized 行为

**验证结果**：
- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，14 个测试全部通过
- 离线测试不触发数据库/向量服务/OpenAI 连接
- 集成测试通过 //go:build integration 标签隔离
- `core/retriever` 和 `core/websearch` 的失败为 Windows 临时目录权限问题（已有问题，非本次引入）

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
