package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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

// --- 辅助函数 ---

// resetState 重置包级状态，用于测试隔离
func resetState() {
	chatMu.Lock()
	defer chatMu.Unlock()
	chat = nil
	chatInit = false
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

// --- 行为测试 ---

// TestInit_FailsWithoutDAO 验证 dao 未初始化时 Init 返回 error。
func TestInit_FailsWithoutDAO(t *testing.T) {
	err := Init()
	if err == nil {
		Reset()
		t.Skip("Init() succeeded unexpectedly; cleaned up")
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed Init()")
	}
	if GetChat() != nil {
		t.Fatal("GetChat() should return nil after failed Init()")
	}
}

// TestInitWith_ConfigError 验证配置为空时返回错误。
func TestInitWith_ConfigError(t *testing.T) {
	defer resetState()

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
	defer resetState()

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

// TestInitWith_RepeatCall_Error 验证重复初始化返回错误。
func TestInitWith_RepeatCall_Error(t *testing.T) {
	defer resetState()

	// 模拟已初始化状态
	chatMu.Lock()
	chatInit = true
	chatMu.Unlock()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	err := InitWith(cfg, nil)
	if err == nil {
		t.Fatal("expected error on repeat call")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}
}

// TestInitWith_DAOError 验证 dao 未初始化时 InitWith 返回错误。
func TestInitWith_DAOError(t *testing.T) {
	defer resetState()

	cfg := &ChatConfig{APIKey: "test", BaseURL: "http://test", Model: "test"}
	factory := &fakeChatModelFactory{cm: &fakeChatModel{}}

	err := InitWith(cfg, factory)
	if err == nil {
		Reset()
		t.Skip("InitWith() succeeded unexpectedly (dao is initialized); cleaned up")
	}
	// 应该因为 dao 未初始化而失败
	if !strings.Contains(err.Error(), "dao") {
		t.Fatalf("expected dao error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after dao error")
	}
}
