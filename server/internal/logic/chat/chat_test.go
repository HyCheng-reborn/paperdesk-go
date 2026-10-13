package chat

import (
	"testing"
)

// TestNoAutoInit 验证 package import 不会自动触发 Chat 服务初始化。
// 移除 init() 副作用后，chat 变量在显式调用 Init() 前应为 nil。
func TestNoAutoInit(t *testing.T) {
	if chat != nil {
		t.Fatal("chat should be nil before Init() is called; init() side effect was not removed")
	}
}

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

// TestInit_Signature 验证 Init 函数签名正确（返回 error）。
func TestInit_Signature(t *testing.T) {
	var _ func() error = Init
}
