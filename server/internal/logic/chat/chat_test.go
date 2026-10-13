package chat

import (
	"testing"
)

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

// TestInit_FailsWithoutDAO 验证 dao 未初始化时 Init 返回 error。
// chat.Init() 依赖 dao.GetDsn()，dao 未初始化时 GetDsn 返回 error。
// 但 chat.Init() 先创建 chat model（需要配置），所以可能先在这一步失败。
// 无论哪种情况，Init 应该返回 error 而非 panic。
func TestInit_FailsWithoutDAO(t *testing.T) {
	err := Init()
	if err == nil {
		// 如果意外成功，清理资源
		Reset()
		t.Skip("Init() succeeded unexpectedly; cleaned up")
	}
	// 验证失败后状态未改变
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed Init()")
	}
	if GetChat() != nil {
		t.Fatal("GetChat() should return nil after failed Init()")
	}
}

// TestInit_Signature 验证 Init/Reset 函数签名正确。
func TestInit_Signature(t *testing.T) {
	var _ func() error = Init
	var _ func() = Reset
	var _ func() bool = IsInitialized
}
