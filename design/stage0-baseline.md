# Stage 0 基线核对报告

## 仓库状态

| 项目 | 值 |
|---|---|
| 项目路径 | `c:\Zhy\code\go-rag` |
| HEAD | `0a1b52ad1a221f157b5228b40f445891d1a87544` |
| 分支 | `main` |
| 已跟踪文件数 | 187 |
| 工作区状态 | 干净（无已跟踪文件修改），3 个未跟踪文件（docs/v1.1.md、v1.1.1.md、v1.1.2.md） |
| 预期 HEAD | `0a1b52a`（与 v1.0 文档记录一致） |

## 核实项状态

| 编号 | 核实项 | 状态 | 结论 |
|---|---|---|---|
| V01 | Eino v0.6.0 依赖兼容性 | **已完成** | 所有 eino-ext 要求 <= v0.6.0，`go build ./...` 和 `go vet ./...` 均通过（退出码 0）。依赖解析和编译通过，运行时兼容仍待后续验证。 |
| V02 | chat-history 包接口 | **已完成** | 能力严重不足：6 项需求中 4 项不支持、2 项部分支持。Stage 5 需设计业务自有模型。详见 `verification/stage0/v02-chat-history.md`。 |
| V03 | MySQL SKIP LOCKED | **未执行** | 需真实数据库，归属 Stage 4。 |
| V04 | 百炼 endpoint 在线探针 | **未执行** | 需授权和预算，归属 Stage 2。 |
| V05 | ES 版本兼容（静态） | **存在风险** | 客户端 v8.16.0 vs Compose 服务端 8.11.3，不在官方保证区间。README 示例 8.18.0 又不同。需统一版本或 Stage 1 启动验证。 |
| V05 | ES 版本兼容（运行） | **未执行** | 归属 0C 启动验证或 Stage 1。 |
| V06 | GoFrame DAO 文件清单 | **已完成** | 生成文件 12 个 + 手写文件 10 个，共 22 个。详见 `verification/stage0/v06-goframe-dao.md`。 |
| V07 | 快照与 commit 差异 | **待补证** | ZIP 文件 `go-rag-main.zip` 未找到（已搜索 5 个路径），无法执行逐文件 SHA-256 对比。HEAD 与 v1.0 文档记录一致，已跟踪文件无修改。 |

## 初始化副作用追踪

详见 `verification/stage0/init-side-effects.md`。

### 有副作用的 init() 函数（3 个）

| 文件 | 副作用 | 触发顺序 |
|---|---|---|
| `internal/dao/dao.go:15` | 数据库连接 + AutoMigrate（3 张表） | 1 |
| `internal/logic/rag/retriever.go:15` | 向量存储连接 + embedding 客户端创建 | 2 |
| `internal/logic/chat/chat.go:30` | OpenAI chat model + chat-history 独立 DB 连接 + AutoMigrate（4 张表） | 3 |

### 显式初始化方案

移除 init() 副作用后，在 `cmd.Main.Func` 中显式调用：
1. `dao.InitDB()` — 主数据库
2. `rag.Init()` — 向量存储 + RAG 核心（需新增此函数）
3. `chat.Init()` — Chat 模型 + 聊天历史（需新增此函数）

依赖关系：dao 先于 chat（chat 依赖 `dao.GetDsn()`）。

## ES 版本风险

| 来源 | 版本 |
|---|---|
| go-elasticsearch 客户端 | v8.16.0 |
| docker-compose ES 镜像 | 8.11.3 |
| README 示例 | 8.18.0 |

客户端 v8.16.0 官方保证兼容服务端 >= 8.16，但 Compose 中为 8.11.3。不在官方保证区间。
本轮不升级 ES，制定独立基础设施验证计划，不阻塞 Stage 1 离线逻辑修复。

## Stage 0 状态总结

- **已完成（静态）**：V01、V02、V05（静态）、V06、V07（部分——HEAD 确认，ZIP 对比待补证）
- **待补证**：V07（需获取 ZIP 文件）
- **未执行（归属后续 Stage）**：V03（Stage 4）、V04（Stage 2）、V05 运行验证（Stage 1 或 0C）
- **启动验证（0C）**：未执行，需单独安排
