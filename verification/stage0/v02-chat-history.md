# V02 chat-history 包接口清单

## 包信息

- 模块：`github.com/wangle201210/chat-history v0.0.1`
- 路径：`C:\Users\zhy\go\pkg\mod\github.com\wangle201210\chat-history@v0.0.1\`

## 文件结构

| 文件 | 说明 |
|---|---|
| eino/eino.go | History 结构体 + NewEinoHistory + SaveMessage + GetHistory |
| eino/helper.go | 消息转换（仅映射 Role + Content） |
| models/models.go | 4 个数据模型 |
| models/const.go | ConvKey 常量 |
| repositories/db.go | InitDB + AutoMigrate |
| repositories/conversation.go | ConversationRepository CRUD |
| repositories/message.go | MessageRepository CRUD |
| repositories/attachment.go | AttachmentRepository CRUD |
| repositories/message_attachment.go | MessageAttachmentRepository CRUD |

## 导出 API

| 函数/方法 | 签名 |
|---|---|
| NewEinoHistory | `func NewEinoHistory(dsn string) *History` |
| SaveMessage | `func (x *History) SaveMessage(mess *schema.Message, convID string) error` |
| GetHistory | `func (x *History) GetHistory(convID string, limit int) ([]*schema.Message, error)` |

## 数据模型

| 表名 | 关键字段 |
|---|---|
| conversations | conv_id(unique), title, settings(json), is_archived, is_pinned |
| messages | msg_id(unique), conversation_id, parent_id, role, content(text), status, metadata(json) |
| attachments | attach_id(unique), message_id, attachment_type, file_name, storage_*, vectorized |
| message_attachments | message_id, attachment_id |

## 初始化路径

1. `NewEinoHistory(dsn)` → `repositories.InitDB(dsn)`
2. DSN 格式判断：含 `@tcp(` → MySQL，否则 → SQLite
3. `gorm.Open()` 建立连接
4. 连接池设置（MySQL: MaxIdle=10, MaxOpen=100; SQLite: MaxIdle=1, MaxOpen=1）
5. `db.AutoMigrate(Conversation, Message, Attachment, MessageAttachment)` — 4 张表

## 能力差距

| 能力需求 | 支持情况 | 证据 |
|---|---|---|
| Conversation 与 Topic 绑定 | 不支持 | Conversation 无 topic_id 字段 |
| 消息 Scope 及来源版本 | 不支持 | Message 无 scope_snapshot 字段 |
| evidence_map | 不支持 | 无专用结构 |
| 终态（generating/failed/canceled） | 部分支持 | Status varchar(20)，无枚举约束 |
| 分页/恢复/过滤 | 部分支持 | offset/limit 分页，无状态过滤 |
| 数据访问 | 有风险 | 独立全局 *gorm.DB，与 GoFrame DAO 并行 |

## 补充发现

- `helper.go` 中 `message2MessagesTemplate` 仅映射 Role + Content，丢弃 Status/Metadata/ParentID
- `SaveMessage` 仅写入 Role、Content、ConversationID
- 包使用独立全局 `*gorm.DB`（非注入），与项目 GoFrame DAO 连接管理完全独立
