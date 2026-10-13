package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/wangle201210/go-rag/server/internal/dao"
	database "github.com/wangle201210/go-rag/server/internal/dao/db"
	"gorm.io/gorm"
)

// --- fake 实现 ---

// fakeChatModel 测试用的 ChatModel 实现
type fakeChatModel struct{}

func (f *fakeChatModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: "assistant", Content: "test"}, nil
}
func (f *fakeChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}

// fakeChatModelFactory 测试用的 ChatModelFactory 实现
type fakeChatModelFactory struct {
	cm          model.BaseChatModel
	createErr   error
	createCount int
}

func (f *fakeChatModelFactory) Create(apiKey, baseURL, modelName string) (model.BaseChatModel, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.cm, nil
}

// fakeDatabase 测试用的 Database 实现（用于初始化 dao）
type fakeDatabase struct {
	mu           sync.Mutex
	connectCount int
	pingCount    int
	closeCount   int
	migrateCount int
	dsn          string
}

func (f *fakeDatabase) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCount++
	return nil
}
func (f *fakeDatabase) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCount++
	return nil
}
func (f *fakeDatabase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCount++
	return nil
}
func (f *fakeDatabase) AutoMigrate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrateCount++
	return nil
}
func (f *fakeDatabase) DSN() string { return f.dsn }
func (f *fakeDatabase) GormDB() *gorm.DB {
	return nil
}

// fakeDatabaseFactory 测试用的 DatabaseFactory 实现
type fakeDatabaseFactory struct {
	db          *fakeDatabase
	createErr   error
	createCount int
}

func (f *fakeDatabaseFactory) Create(cfg database.Config) (database.Database, error) {
	f.createCount++
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.db, nil
}

// fakeConfig 测试用的 ConfigProvider 实现
type fakeConfig struct {
	cfg *database.Config
	err error
}

func (f *fakeConfig) GetDBConfig() (*database.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.cfg, nil
}

// --- 辅助函数 ---

// resetChatState 重置 chat 包级状态，用于测试隔离
func resetChatState() {
	chatMu.Lock()
	defer chatMu.Unlock()
	chat = nil
	chatInit = false
}

// resetDAOState 重置 dao 包级状态，用于测试隔离
func resetDAOState() {
	_ = dao.Close()
}

// initDAOForTest 使用 fake 初始化 dao（用于需要 dao 已初始化的测试）
func initDAOForTest(t *testing.T) func() {
	t.Helper()
	fakeDB := &fakeDatabase{dsn: "test-dsn"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	if err := dao.InitDB(cfg, factory); err != nil {
		t.Fatalf("failed to init DAO for test: %v", err)
	}
	return func() {
		resetDAOState()
	}
}

// --- 前置条件测试 ---

// TestGetChatBeforeInit 验证未初始化时 GetChat 返回 nil。
func TestGetChatBeforeInit(t *testing.T) {
	c := GetChat()
	if c != nil {
		t.Fatal("GetChat() should return nil before Init()")
	}
}

// TestIsInitializedBeforeCall 验证未初始化时 IsInitialized 返回 false。
func TestIsInitializedBeforeCall(t *testing.T) {
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false before Init()")
	}
}

// TestResetBeforeInit 验证未初始化时 Reset 是安全的 no-op。
func TestResetBeforeInit(t *testing.T) {
	Reset()
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
	if GetChat() != nil {
		t.Fatal("GetChat() should return nil after Reset()")
	}
}

// --- 行为测试（全部使用 fake，不调用现场 Init） ---

// TestInit_FailsWithoutDAO 验证 dao 未初始化时 InitWith 返回 error（在工厂调用之前）。
func TestInit_FailsWithoutDAO(t *testing.T) {
	defer resetChatState()
	// 确保 dao 未初始化
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("expected dao error, got: %v", err)
	}
	// 工厂不应被调用（DAO 检查在工厂之前）
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when dao not initialized")
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after dao error")
	}
}

// TestInitWith_ConfigError 验证配置为空时返回错误。
func TestInitWith_ConfigError(t *testing.T) {
	defer resetChatState()

	cfg := &ChatConfig{APIKey: "", BaseURL: "", Model: ""}
	err := InitWith(cfg, nil)
	if err == nil {
		t.Fatal("expected error with empty config")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected config error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after config error")
	}
}

// TestInitWith_ChatModelError 验证 ChatModel 创建失败时不发布。
func TestInitWith_ChatModelError(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	// 需要先初始化 dao（因为 DAO 检查在工厂之前）
	fakeDB := &fakeDatabase{dsn: "test"}
	daoFactory := &fakeDatabaseFactory{db: fakeDB}
	daoCfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}
	if err := dao.InitDB(daoCfg, daoFactory); err != nil {
		t.Fatalf("failed to init DAO: %v", err)
	}

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{createErr: errors.New("model error")}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "chat model failed") {
		t.Fatalf("expected chat model error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after chat model error")
	}
}

// TestInitWith_RepeatCall_Rejected 验证重复初始化被拒绝（通过真实成功，非预设状态）。
func TestInitWith_RepeatCall_Rejected(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	// 初始化 dao
	cleanupDAO := initDAOForTest(t)
	defer cleanupDAO()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	// 第一次成功
	err := InitWith(cfg, factory)
	if err != nil {
		t.Fatalf("first InitWith() unexpected error: %v", err)
	}

	// 第二次返回 error
	err = InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error on repeat call")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}
	// 工厂只调用一次
	if factory.createCount != 1 {
		t.Fatalf("factory should only be called once, got %d", factory.createCount)
	}
}

// TestInitWith_DAOError_BeforeFactory 验证 DAO 错误在工厂调用之前被检测。
func TestInitWith_DAOError_BeforeFactory(t *testing.T) {
	defer resetChatState()
	// 确保 dao 未初始化
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	// 工厂不应被调用
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("expected dao error, got: %v", err)
	}
}

// --- 协调测试 ---

// TestInit_Order_DAOFirst 验证 chat 在 DAO 未初始化时失败。
func TestInit_Order_DAOFirst(t *testing.T) {
	defer resetChatState()
	resetDAOState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		t.Fatal("expected error when dao not initialized")
	}
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("error should mention dao, got: %v", err)
	}
}

// TestInit_Success_WithDAO 验证 DAO 已初始化时 chat 可以成功。
func TestInit_Success_WithDAO(t *testing.T) {
	defer resetChatState()
	defer resetDAOState()

	cleanupDAO := initDAOForTest(t)
	defer cleanupDAO()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err != nil {
		t.Fatalf("InitWith() unexpected error: %v", err)
	}
	if !IsInitialized() {
		t.Fatal("should be initialized after successful InitWith()")
	}
	if GetChat() == nil {
		t.Fatal("GetChat() should return non-nil after successful init")
	}
	if factory.createCount != 1 {
		t.Fatalf("expected 1 factory call, got %d", factory.createCount)
	}
}
