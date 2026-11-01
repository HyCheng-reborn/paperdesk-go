# init() 副作用追踪

## 有副作用的 init() 函数

### 1. internal/dao/dao.go:15

**操作**：读取配置 → `db.Connect()` → `db.Ping()` → `db.AutoMigrate()`
**副作用**：连接数据库 + 写数据库（AutoMigrate 建表/加列/加索引）
**AutoMigrate 表**：knowledge_base, knowledge_documents, knowledge_chunks（3 张）
**调用方**：logic/chat 直接 import dao；controller/rag 直接 import dao；logic/knowledge 直接 import dao

### 2. internal/logic/rag/retriever.go:15

**操作**：读取向量配置 → `vector.NewVectorStore()` → `core.New()` 创建 embedding 模型等
**副作用**：连接外部向量服务（ES/Qdrant）+ 调用 OpenAI API 创建 embedding 客户端
**调用方**：controller/rag 的多个文件 import logic/rag

### 3. internal/logic/chat/chat.go:30

**操作**：读取 chat 配置 → `openai.NewChatModel()` → `eino.NewEinoHistory(dao.GetDsn())`
**副作用**：调用 OpenAI API（创建 chat model）+ 连接数据库（chat-history 包独立 GORM 连接 + AutoMigrate 4 张表）
**AutoMigrate 表**：conversations, messages, attachments, message_attachments（4 张）
**调用方**：controller/rag/rag_v1_chat.go, rag_v1_chat_stream.go

## 无 init() 的目录

- `server/core/` — 无 init()
- `server/internal/cmd/` — 无 init()
- `server/internal/mcp/` — 无 init()

## 全局变量初始化

| 文件 | 变量 | 调用 | 副作用 |
|---|---|---|---|
| internal/mcp/mcp.go:5 | c | rag.NewV1() | 无（纯结构体） |
| internal/logic/rag/retriever.go:13 | ragSvr | &core.Rag{} | 无（纯字面量） |
| internal/dao/internal/*.go | *Columns | 结构体字面量 | 无 |
| internal/dao/knowledge_*.go | Knowledge* | internal.New*Dao() | 无（纯内存） |

## main.go 启动路径

```
main.go
  import _ "gf/contrib/drivers/mysql/v2"   // 驱动注册
  import _ "gf/contrib/drivers/sqlite/v2"  // 驱动注册
  import "internal/cmd"
    import "internal/controller/rag"
      import "internal/logic/rag"        → init(): 向量服务连接
      import "internal/logic/chat"       → init(): OpenAI + chat-history DB
        import "internal/dao"            → init(): 主数据库连接
```

Go init 执行顺序（依赖图拓扑排序）：
1. dao.init() — 主数据库
2. logic/rag init() — 向量存储
3. logic/chat init() — Chat 模型 + 聊天历史

## 显式初始化方案

移除 init() 副作用后，在 cmd.Main.Func 中显式调用：
1. `dao.InitDB()` — 主数据库
2. `rag.Init()` — 向量存储 + RAG 核心（需新增此函数）
3. `chat.Init()` — Chat 模型 + 聊天历史（需新增此函数）

依赖：dao 先于 chat（chat 依赖 dao.GetDsn()）。

## 测试影响

- `go test` 会触发 package init()，导致测试连接真实数据库
- 离线测试需 mock 或跳过这些初始化
- `go test -run` 不能阻止 package init
