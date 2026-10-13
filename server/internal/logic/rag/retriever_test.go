package rag

import (
	"testing"
)

// TestNoAutoInit 验证 package import 不会自动触发向量存储连接。
// 移除 init() 副作用后，ragSvr 应保持为 nil（不再初始化为 &core.Rag{}）。
func TestNoAutoInit(t *testing.T) {
	if ragSvr != nil {
		t.Fatal("ragSvr should be nil before Init() is called; init() side effect was not removed")
	}
}

// TestGetRagSvrBeforeInit 验证未初始化时 GetRagSvr 返回 nil 而非零值实例。
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

// TestInit_Signature 验证 Init 函数签名正确（返回 error）。
func TestInit_Signature(t *testing.T) {
	var _ func() error = Init
}
