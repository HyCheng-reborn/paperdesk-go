# 变更记录

## 2026-10-13 Stage 1A 复核修复

### 修复内容

| 修复项 | 说明 |
|---|---|
| AutoMigrate 分离 | InitDB() 仅连接，新增 Migrate() 建表；新增 InitWithMigrate() 兼容入口 |
| 错误传播与清理 | cmd.go 中 Fatalf 改为返回带阶段信息的 error，失败时 dao.Close() 清理 |
| 原子初始化 | 局部变量构建完整实例后才赋值全局变量；失败时全局变量保持零值 |
| 重复初始化保护 | 重复调用 Init() 返回 error；各包新增 IsInitialized() |
| 未初始化访问保护 | GetDsn() 返回空字符串；GetRagSvr()/GetChat() 返回 nil |
| Close 函数 | dao 包新增 Close()；Database 接口新增 Close() 方法 |
| 测试隔离 | core/rag_test.go 和 chat_model_test.go 添加 //go:build integration |
| 测试增强 | 新增 GetDsn/Close/IsInitialized/Migrate 未初始化行为测试 |
| F 编号修正 | 1C 对应 F03/F04；1D 对应 F07/F08/F09 |

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | AutoMigrate 分离、原子初始化、Close/IsInitialized/Migrate/InitWithMigrate |
| server/internal/dao/db/interface.go | Database 接口新增 Close() |
| server/internal/dao/db/base.go | Base 实现 Close() |
| server/internal/logic/rag/retriever.go | ragSvr 默认 nil、重复保护、GetRagSvr nil 安全、IsInitialized |
| server/internal/logic/chat/chat.go | 重复保护、GetChat nil 安全、IsInitialized、chat-history AutoMigrate 边界标注 |
| server/internal/cmd/cmd.go | Fatalf → return error，失败时 dao.Close() |
| server/core/rag_test.go | 添加 //go:build integration 标签 |
| server/core/common/chat_model_test.go | 添加 //go:build integration 标签 |
| design/stage1-plan.md | F 编号修正、1A 复核修复记录 |
| PROJECT_PROGRESS.md | 更新进度 |
| .gitignore | 添加 design/verification/PROJECT_PROGRESS.md/CHANGES.md 例外 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0，14 个测试全部通过
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

整批协调回滚：恢复 init() + 移除显式 Init()/Migrate()/Close() 调用。避免恢复 init 后仍保留显式调用造成重复初始化。

---
## 2026-10-13 Stage 1A：初始化副作用清理 + 离线测试隔离

### 修改文件

| 文件 | 变更 |
|---|---|
| server/internal/dao/dao.go | 移除 init()，保留 InitDB() error，添加文档注释 |
| server/internal/logic/rag/retriever.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/logic/chat/chat.go | init() 提取为 Init() error，Fatalf 改为 return error |
| server/internal/cmd/cmd.go | 在 s.Run() 前显式调用 dao.InitDB() → rag.Init() → chat.Init() |

### 新增文件

| 文件 | 说明 |
|---|---|
| server/internal/dao/dao_test.go | 验证 db 在 InitDB() 前为 nil |
| server/internal/logic/rag/retriever_test.go | 验证 ragSvr 在 Init() 前为零值，Init 签名正确 |
| server/internal/logic/chat/chat_test.go | 验证 chat 在 Init() 前为 nil，Init 签名正确 |

### 验证结果

- `go build ./...` — 退出码 0
- `go vet ./...` — 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` — 退出码 0
- 离线测试不触发数据库/向量服务/OpenAI 连接

### 回滚说明

恢复 init() 中的调用即可。每个文件独立可回滚。

---
## 2026-10-13 Stage 0 补证 + Stage 1 计划

### 新增文件

| 文件 | 说明 |
|---|---|
| design/stage0-baseline.md | Stage 0 基线核对报告 |
| design/stage1-plan.md | Stage 1 分批实施计划 |
| verification/stage0/v01-build-evidence.md | V01 构建证据 |
| verification/stage0/v02-chat-history.md | V02 chat-history 接口清单 |
| verification/stage0/v06-goframe-dao.md | V06 GoFrame DAO 文件清单 |
| verification/stage0/init-side-effects.md | init() 副作用追踪 |
| verification/stage0/es-version-risk.md | ES 版本风险记录 |
| PROJECT_PROGRESS.md | 项目进度记录 |
| CHANGES.md | 本文件 |

### Stage 0 结论

- V01：依赖解析和编译通过（go build/vet 退出码 0）
- V02：chat-history 能力严重不足（6 项需求中 4 项不支持）
- V05：ES 版本不在官方保证区间（客户端 8.16.0 vs 服务端 8.11.3）
- V06：12 生成 + 10 手写文件已分类
- V07：待补证（ZIP 未找到）
- 3 个 init() 副作用已追踪，显式初始化方案已制定

### Stage 1 计划

- 5 个批次：1-Pre → 1A → 1B → 1C → 1D
- 推荐首批：1-Pre（F06 nil 修复）和 1A（init 副作用清理）可并行
- 覆盖 F01-F09 + F12

### 方案文档版本线

- v1.0 → v1.1 → v1.1.1 → v1.1.2（当前）
- 详见 docs/go-rag_PaperDesk_改进方案_v1.1.2.md
