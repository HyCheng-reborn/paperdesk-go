# Stage 1 分批实施计划

## 概述

Stage 1 目标：修复初始化副作用、优先 nil 访问、并发安全和错误处理问题，使项目具备离线可测试的基础。

**不实现**：完整持久化任务引擎、重启恢复能力。
**不修改**：依赖版本、Compose 配置、数据库 schema。
**不启动**：服务、真实模型调用。

## init 副作用追踪结论

3 个有副作用的 init()：

| # | 文件 | 副作用 | 触发顺序 |
|---|---|---|---|
| 1 | internal/dao/dao.go:15 | DB 连接 + AutoMigrate（3 表） | 1 |
| 2 | internal/logic/rag/retriever.go:15 | 向量存储连接 + embedding 客户端 | 2 |
| 3 | internal/logic/chat/chat.go:30 | OpenAI chat model + chat-history DB + AutoMigrate（4 表） | 3 |

显式初始化方案：`dao.InitDB()` → `rag.Init()` → `chat.Init()`，在 `cmd.Main.Func` 中调用。

## 批次划分

### 批次 1-Pre：F06 File/URL 参数校验 ✅ 已完成

**对应 F 编号**：F06

**触发条件**：通过 URL 提交文档时 `req.File` 为 nil，第 29 行 `req.File.Filename` 触发 panic。

**修复内容**：
- 文件：`server/internal/controller/rag/rag_v1_indexer.go`
- 将第 29 行 `req.File.Filename` 移入 `if req.File != nil` 保护块内
- 当 File 为 nil 且 URL 也为空时，返回参数错误
- 新增 nil 检查测试

**实施顺序**：无前置依赖，可独立执行

**影响范围**：
- 配置：无
- 接口：无变化（仅修复 panic）
- 启动：无影响
- 兼容性：无

**测试**：
- 新增单元测试：验证 File=nil + URL 为空时返回错误而非 panic
- 新增单元测试：验证 File=nil + URL 非空时正常处理
- race 覆盖包：无需（单文件修改）

**验证命令**：
```
go test ./internal/controller/rag/... -run TestIndexer -v
```

**回滚边界**：单文件修改，回滚简单。

**交付物**：diff、测试日志、退出码。

---

### 批次 1A：初始化副作用清理 + 离线测试隔离 ✅ 复核修复已完成

**对应 F 编号**：F12（init 副作用）

**触发条件**：`go test` 触发 package init()，导致测试连接真实数据库/向量服务。

**修复内容**：

1. **dao/dao.go**：移除 `init()` 中的 `InitDB()` 调用，保留 `InitDB()` 函数供显式调用
2. **logic/rag/retriever.go**：将 init() 中的向量存储连接逻辑提取为 `Init()` 函数
3. **logic/chat/chat.go**：将 init() 中的 chat model 和 chat-history 初始化提取为 `Init()` 函数
4. **cmd/cmd.go**：在 `Main.Func` 的 `s.Run()` 之前显式调用：
   ```go
   dao.InitDB()
   rag.Init()
   chat.Init()
   ```
5. **测试隔离**：确保 `go test ./...` 在无数据库环境下不触发连接

**实施顺序**：
1. 先提取 Init() 函数（不删除 init）
2. 在 cmd.go 中添加显式调用
3. 删除 init() 中的副作用
4. 验证 `go build ./...` 通过
5. 验证 `go test ./...` 在无 DB 环境下不 panic（可能仍需要 mock 或 skip）

**前置条件**：无

**影响范围**：
- 配置：无变化
- 接口：无变化
- 启动：启动流程从隐式 init 变为显式调用，行为等价
- 兼容性：API 入口不变

**测试**：
- `go build ./...` 编译通过
- `go vet ./...` 无警告
- 离线环境下 `go test ./core/... ./internal/dao/...` 不触发 DB 连接

**验证命令**：
```
go build ./...
go vet ./...
go test ./core/... -count=1 -short
```

**回滚边界**：恢复 init() 中的调用即可。

**交付物**：diff、编译日志、测试日志、退出码。

**实际完成记录**：
- 修改文件：`dao/dao.go`、`logic/rag/retriever.go`、`logic/chat/chat.go`、`cmd/cmd.go`
- 新增测试：`dao/dao_test.go`、`logic/rag/retriever_test.go`、`logic/chat/chat_test.go`
- `go build ./...` 退出码 0
- `go vet ./...` 退出码 0
- `go test -race ./internal/dao/... ./internal/logic/rag/... ./internal/logic/chat/...` 退出码 0
- 离线测试不触发数据库/向量服务/OpenAI 连接

**注意**：`go test -run` 不能阻止 package init。不得用跳过关键测试、吞掉错误或依赖机器上恰好存在的服务来制造离线通过。

**复核修复记录**：

1. **AutoMigrate 分离**：`InitDB()` 仅负责连接，新增 `Migrate() error` 负责建表；新增 `InitWithMigrate()` 兼容入口
2. **错误传播与清理**：cmd.go 中 `Fatalf` 改为返回带阶段信息的 error，失败时调用 `dao.Close()` 清理
3. **原子初始化**：使用局部变量构建完整实例后才赋值全局变量；失败时全局变量保持零值
4. **重复初始化保护**：重复调用 `Init()` 返回 error；各包新增 `IsInitialized()` 查询函数
5. **未初始化访问保护**：`GetDsn()` 未初始化返回空字符串；`GetRagSvr()`/`GetChat()` 未初始化返回 nil
6. **Close 函数**：dao 包新增 `Close() error`，Database 接口新增 `Close()` 方法
7. **测试隔离**：`core/rag_test.go` 和 `core/common/chat_model_test.go` 添加 `//go:build integration` 标签
8. **测试增强**：新增 GetDsn/Close/IsInitialized/Migrate 未初始化行为测试
9. **F 编号修正**：1C 对应 F03/F04；1D 对应 F07/F08/F09

---

### 批次 1B：Retriever/Indexer 并发与 Rerank HTTP 错误处理

**对应 F 编号**：F01、F02、F05

**触发条件**：
- F01：Retriever.Retrieve 循环修改 req.optQuery（多 goroutine 共享）+ err 命名返回值竞争
- F02：Indexer.Index 异步闭包写外部命名返回值 err
- F05：Rerank HTTP 请求使用 context.Background()，失败后继续处理

**修复内容**：

1. **F01 — server/core/retriever.go**：
   - 第 76 行：将 req.optQuery 的修改移到循环前，或使用局部变量
   - 第 81 行：将 err 改为局部变量，不使用命名返回值
   - 文件：`server/core/retriever.go`

2. **F02 — server/core/indexer.go**：
   - 第 63 行：异步闭包不写外部命名返回值 err
   - 改为在闭包内使用局部错误变量，通过 channel 或返回值传递
   - 文件：`server/core/indexer.go`

3. **F05 — server/core/rerank/rerank.go**：
   - 第 109 行：将 context.Background() 改为传入的 ctx
   - 第 109 行后：HTTP 请求失败时返回错误，不继续处理
   - 文件：`server/core/rerank/rerank.go`

**实施顺序**：三个修复相互独立，可并行。建议按 F01 → F02 → F05 顺序。

**前置条件**：1A 完成（确保测试不触发真实连接）

**影响范围**：
- 配置：无
- 接口：无变化
- 启动：无影响
- 兼容性：无

**测试**：
- F01：并发测试（多个 goroutine 同时 Retrieve），验证无 data race
- F02：异步 Index 测试，验证错误正确传播
- F05：mock HTTP 失败，验证错误返回

**验证命令**：
```
go test -race ./core ./core/rerank/... -count=1 -v
```

**回滚边界**：每个修复独立，可单独回滚。

**交付物**：diff、race 测试日志、退出码。

---

### 批次 1C：Chat context + 流式 EOF/取消/错误

**对应 F 编号**：F03、F04

**触发条件**：
- F03：Chat 未正确处理 context 取消
- F04：流式读取失败后仍发送 [DONE] 并返回 nil

**修复内容**：

1. **F03 — server/internal/logic/chat/chat.go**：
   - 检查 context 取消，返回适当错误
   - 文件：`server/internal/logic/chat/chat.go`

2. **F04 — server/core/common/stream.go**：
   - 第 51 行读流失败后，不发送 [DONE]，返回错误
   - 文件：`server/core/common/stream.go`

**实施顺序**：F04 优先（流式基础），然后 F03。

**前置条件**：1A 完成

**影响范围**：
- 配置：无
- 接口：流式响应在错误时行为变化（不再发送 [DONE]）
- 启动：无影响
- 兼容性：客户端需处理不收到 [DONE] 的情况

**测试**：
- F03：context 取消测试
- F04：mock 流读取失败，验证不发送 [DONE]

**验证命令**：
```
go test -race ./core/common/... ./internal/logic/chat/... -count=1 -v
```

**回滚边界**：每个修复独立。

**交付物**：diff、测试日志、退出码。

---

### 批次 1D：Chunk 写入错误传播 + QA 有限并发和失败状态 + goroutine 入库止损

**对应 F 编号**：F07、F08、F09

**触发条件**：
- F07：`core/indexer.go` 依赖后台 goroutine 和 sleep 后读取 ES，重启时没有持久化任务
- F08：`core/indexer/qa.go` 为每个 Chunk 启协程；失败仅记日志
- F09：`internal/logic/knowledge/chunks.go` 部分写入失败仍返回 nil

**修复内容**：

1. **F07 — server/core/indexer.go（止损措施）**：
   - QA 生成增加并发上限（errgroup.SetLimit）
   - 失败时标记明确 failed 状态而非伪造成功
   - QA 可通过配置关闭
   - 注意：此时尚未具备持久化任务恢复能力，完整任务引擎在 Stage 4 实现
   - 文件：`server/core/indexer.go`、`server/core/indexer/qa.go`

2. **F08 — server/core/indexer/qa.go**：
   - 添加有限并发控制（errgroup.SetLimit，限制为合理数量）
   - 失败时标记 failed 状态
   - 文件：`server/core/indexer/qa.go`

3. **F09 — server/internal/logic/knowledge/chunks.go**（或相关写入路径）：
   - 错误正确传播，不吞掉
   - 写入失败就是 failed，不使用 PartialActive 等新状态掩盖
   - 文件：待确认具体位置

**实施顺序**：F09 → F08 → F07

**前置条件**：1A 完成，1B 部分完成（Indexer 并发基础）

**影响范围**：
- 配置：可能需要新增并发限制配置项
- 接口：无变化
- 启动：无影响
- 兼容性：无

**测试**：
- F07：验证 QA 并发限制生效
- F08：并发 QA 生成测试 + race 检测
- F09：mock 写入失败，验证错误传播

**验证命令**：
```
go test -race ./core/indexer/... ./internal/logic/knowledge/... -count=1 -v
```

**回滚边界**：每个修复独立。

**交付物**：diff、race 测试日志、退出码。

---

## 批次依赖关系

```
1-Pre (F06) ──┐
              ├── 可并行
1A (F12) ─────┘
              │
              ▼
1B (F01/F02/F05) ──┐
                    ├── 可部分并行
1C (F03/F04) ────┘
              │
              ▼
1D (F07/F08/F09)
```

推荐顺序：1-Pre 和 1A 并行 → 1B → 1C → 1D

**当前进度**：1-Pre 已完成，1A 复核修复已完成（2026-10-14）

## 全局验证命令

每个批次完成后运行：
```
go build ./...
go vet ./...
go test -race ./core ./core/retriever/... ./core/indexer/... ./core/rerank/... ./core/common/... -count=1
```

Stage 5 完成后追加：
```
go test -race ./internal/logic/chat/... ./internal/logic/knowledge/... -count=1
```

## 未验证项

- 运行时 Eino 框架兼容性（需 Stage 2 在线验证）
- ES 客户端/服务端版本兼容性（需启动验证）
- MySQL SKIP LOCKED 支持（需 Stage 4 真实 DB）
- chat-history 包替换/适配（Stage 5 设计）
- 完整持久化任务引擎（不在 Stage 1 范围）
