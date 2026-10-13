package rag

import (
	"testing"
)

// TestGetRagSvrBeforeInit 验证未初始化时 GetRagSvr 返回 nil。
func TestGetRagSvrBeforeInit(t *testing.T) {
	svr := GetRagSvr()
	if svr != nil {
		t.Fatal("GetRagSvr() should return nil before Init()")
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
	if GetRagSvr() != nil {
		t.Fatal("GetRagSvr() should return nil after Reset()")
	}
}

// TestInit_FailsWithoutConfig 验证无配置时 Init 返回 error 而非 panic。
func TestInit_FailsWithoutConfig(t *testing.T) {
	err := Init()
	if err == nil {
		// 如果意外成功（有默认配置），清理资源
		Reset()
		t.Skip("Init() succeeded unexpectedly (config exists); cleaned up")
	}
	// 验证失败后状态未改变
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed Init()")
	}
	if GetRagSvr() != nil {
		t.Fatal("GetRagSvr() should return nil after failed Init()")
	}
}

// TestInit_Signature 验证 Init/Reset 函数签名正确。
func TestInit_Signature(t *testing.T) {
	var _ func() error = Init
	var _ func() error = Reset
	var _ func() bool = IsInitialized
}
