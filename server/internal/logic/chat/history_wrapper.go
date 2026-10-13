package chat

import (
	"fmt"

	"github.com/wangle201210/chat-history/models"
	"github.com/wangle201210/go-rag/server/internal/dao"
	"gorm.io/gorm"
)

// chatHistoryTables 定义 chat-history 包需要迁移的表
// 这些表由第三方包 chat-history 定义，但我们将迁移控制权分离出来
var chatHistoryTables = []interface{}{
	&models.Conversation{},
	&models.Message{},
	&models.Attachment{},
	&models.MessageAttachment{},
}

// MigrateChatHistoryTables 显式执行 chat-history 表的迁移
// 这是一个独立的迁移入口，不会在 Init 时自动调用
// 必须在 dao.InitDB() 成功后调用
func MigrateChatHistoryTables() error {
	if !dao.IsInitialized() {
		return fmt.Errorf("chat: dao must be initialized before migrating chat-history tables")
	}

	// 获取 dao 的 GORM DB 实例
	gormDB, err := dao.GetGormDB()
	if err != nil {
		return fmt.Errorf("chat: failed to get GORM DB: %w", err)
	}

	// 执行迁移
	if err := gormDB.AutoMigrate(chatHistoryTables...); err != nil {
		return fmt.Errorf("chat: failed to migrate chat-history tables: %w", err)
	}

	return nil
}

// getGormDBFromDAO 从 dao 获取 GORM DB 实例用于 History
// 这样 chat-history 复用主数据库连接，而不是创建独立连接
func getGormDBFromDAO() (*gorm.DB, error) {
	return dao.GetGormDB()
}
