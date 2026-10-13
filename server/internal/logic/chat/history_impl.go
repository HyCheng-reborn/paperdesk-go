package chat

import (
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/chat-history/models"
	"github.com/wangle201210/chat-history/repositories"
	"gorm.io/gorm"
)

// historyWrapper 是 chat-history History 的包装器
// 它复用 dao 的数据库连接，而不是创建独立连接
type historyWrapper struct {
	db *gorm.DB
	mr *repositories.MessageRepository
	cr *repositories.ConversationRepository
}

// newHistoryWrapper 创建使用现有 GORM DB 的 History 包装器
// 注意：这不会执行 AutoMigrate，迁移需要通过 MigrateChatHistoryTables() 显式调用
func newHistoryWrapper(db *gorm.DB) *historyWrapper {
	return &historyWrapper{
		db: db,
		mr: repositories.NewMessageRepository(db),
		cr: repositories.NewConversationRepository(db),
	}
}

// SaveMessage 存储 message（实现与 chat-history History 相同的接口）
func (h *historyWrapper) SaveMessage(mess *schema.Message, convID string) error {
	return h.mr.Create(&models.Message{
		Role:           string(mess.Role),
		Content:        mess.Content,
		ConversationID: convID,
	})
}

// GetHistory 根据 convID 获取聊天历史
func (h *historyWrapper) GetHistory(convID string, limit int) (list []*schema.Message, err error) {
	if limit == 0 {
		limit = 100
	}
	// 如果 convID 数据不存在，则创建
	_, err = h.cr.FirstOrCreat(convID)
	if err != nil {
		return
	}
	// 最多取 100 条
	mess, err := h.mr.ListByConversation(convID, 0, limit)
	if err != nil {
		return
	}
	list = messageList2ChatHistory(mess)
	return
}

// messageList2ChatHistory 将消息列表转换为 schema.Message 列表
// 这个函数从 chat-history 包复制过来
func messageList2ChatHistory(mess []*models.Message) []*schema.Message {
	var list []*schema.Message
	for _, m := range mess {
		list = append(list, &schema.Message{
			Role:    schema.RoleType(m.Role),
			Content: m.Content,
		})
	}
	return list
}
