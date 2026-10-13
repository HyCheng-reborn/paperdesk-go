package chat

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/chat-history/models"
	"github.com/wangle201210/chat-history/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTestDB 创建内存 SQLite 测试数据库并迁移表
func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	// 迁移 chat-history 表
	err = db.AutoMigrate(
		&models.Conversation{},
		&models.Message{},
		&models.Attachment{},
		&models.MessageAttachment{},
	)
	if err != nil {
		t.Fatalf("failed to migrate test db: %v", err)
	}
	return db
}

// TestHistory_NoAutoMigrate 验证 historyWrapper 构造不执行迁移。
// 使用一个没有迁移任何表的 gorm.DB，构造 wrapper 后验证表不存在。
func TestHistory_NoAutoMigrate(t *testing.T) {
	// 创建空的内存 DB（不迁移任何表）
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	// 构造 wrapper — 不应该执行迁移
	// 注意：NewMessageRepository/NewConversationRepository 只保存 db 引用，不迁移
	h := newHistoryWrapper(db)
	if h == nil {
		t.Fatal("newHistoryWrapper should return non-nil")
	}

	// 验证表确实不存在（wrapper 构造不应创建表）
	if db.Migrator().HasTable("messages") {
		t.Fatal("historyWrapper constructor should not auto-migrate messages table")
	}
	if db.Migrator().HasTable("conversations") {
		t.Fatal("historyWrapper constructor should not auto-migrate conversations table")
	}
}

// TestHistory_SaveAndGet 验证 SaveMessage 和 GetHistory 的基本功能。
func TestHistory_SaveAndGet(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	// SaveMessage
	err := h.SaveMessage(&schema.Message{Role: "user", Content: "hello"}, "conv1")
	if err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	// GetHistory
	msgs, err := h.GetHistory("conv1", 10)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Fatalf("expected role 'user', got %q", msgs[0].Role)
	}
	if msgs[0].Content != "hello" {
		t.Fatalf("expected content 'hello', got %q", msgs[0].Content)
	}
}

// TestHistory_MultipleMessages 验证消息顺序和多条消息。
func TestHistory_MultipleMessages(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	// 保存多条消息
	h.SaveMessage(&schema.Message{Role: "user", Content: "msg1"}, "conv1")
	h.SaveMessage(&schema.Message{Role: "assistant", Content: "msg2"}, "conv1")
	h.SaveMessage(&schema.Message{Role: "user", Content: "msg3"}, "conv1")

	msgs, err := h.GetHistory("conv1", 10)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// 验证顺序
	if msgs[0].Content != "msg1" {
		t.Fatalf("expected first message 'msg1', got %q", msgs[0].Content)
	}
	if msgs[1].Content != "msg2" {
		t.Fatalf("expected second message 'msg2', got %q", msgs[1].Content)
	}
	if msgs[2].Content != "msg3" {
		t.Fatalf("expected third message 'msg3', got %q", msgs[2].Content)
	}
}

// TestHistory_Limit 验证 limit 参数生效。
func TestHistory_Limit(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	// 保存 5 条消息
	for i := 0; i < 5; i++ {
		h.SaveMessage(&schema.Message{Role: "user", Content: "msg"}, "conv1")
	}

	// 限制取 3 条
	msgs, err := h.GetHistory("conv1", 3)
	if err != nil {
		t.Fatalf("GetHistory failed: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages with limit=3, got %d", len(msgs))
	}
}

// TestHistory_ConvIDIsolation 验证不同 convID 的消息隔离。
func TestHistory_ConvIDIsolation(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	h.SaveMessage(&schema.Message{Role: "user", Content: "conv1-msg"}, "conv1")
	h.SaveMessage(&schema.Message{Role: "user", Content: "conv2-msg"}, "conv2")

	msgs1, _ := h.GetHistory("conv1", 10)
	msgs2, _ := h.GetHistory("conv2", 10)

	if len(msgs1) != 1 || msgs1[0].Content != "conv1-msg" {
		t.Fatalf("conv1 isolation failed: got %d messages", len(msgs1))
	}
	if len(msgs2) != 1 || msgs2[0].Content != "conv2-msg" {
		t.Fatalf("conv2 isolation failed: got %d messages", len(msgs2))
	}
}

// TestHistory_RoleContentMapping 验证 Role/Content 映射正确。
func TestHistory_RoleContentMapping(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	roles := []schema.RoleType{schema.User, schema.Assistant, schema.System}
	for _, role := range roles {
		h.SaveMessage(&schema.Message{Role: role, Content: "content-" + string(role)}, "conv1")
	}

	msgs, _ := h.GetHistory("conv1", 10)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	for i, role := range roles {
		if msgs[i].Role != role {
			t.Fatalf("message %d: expected role %q, got %q", i, role, msgs[i].Role)
		}
		if msgs[i].Content != "content-"+string(role) {
			t.Fatalf("message %d: expected content %q, got %q", i, "content-"+string(role), msgs[i].Content)
		}
	}
}

// TestHistory_Close_NoOp 验证 Close 不关闭借用的 DAO 连接。
func TestHistory_Close_NoOp(t *testing.T) {
	db := setupTestDB(t)
	mr := repositories.NewMessageRepository(db)
	cr := repositories.NewConversationRepository(db)
	h := &historyWrapper{db: db, mr: mr, cr: cr}

	err := h.Close()
	if err != nil {
		t.Fatalf("Close() should return nil, got: %v", err)
	}

	// DB 仍然可用（因为 Close 不应该关闭它）
	var count int64
	db.Model(&models.Message{}).Count(&count)
	// 如果能执行查询，说明 DB 没有被关闭
}
