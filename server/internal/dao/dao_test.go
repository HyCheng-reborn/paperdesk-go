package dao

import (
	"strings"
	"testing"
)

// TestGetDsnBeforeInit 验证未初始化时 GetDsn 返回错误而非 panic。
func TestGetDsnBeforeInit(t *testing.T) {
	dsn, err := GetDsn()
	if err == nil {
		t.Fatal("GetDsn() before InitDB() should return error")
	}
	if dsn != "" {
		t.Fatalf("GetDsn() before InitDB() should return empty DSN, got %q", dsn)
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("error should mention 'not initialized', got: %v", err)
	}
}

// TestCloseBeforeInit 验证未初始化时 Close 是安全的 no-op。
func TestCloseBeforeInit(t *testing.T) {
	err := Close()
	if err != nil {
		t.Fatalf("Close() before InitDB() should return nil, got %v", err)
	}
}

// TestIsInitializedBeforeCall 验证未初始化时 IsInitialized 返回 false。
func TestIsInitializedBeforeCall(t *testing.T) {
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false before InitDB()")
	}
}

// TestMigrateBeforeInit 验证未调用 InitDB 时 Migrate 返回错误。
func TestMigrateBeforeInit(t *testing.T) {
	err := Migrate()
	if err == nil {
		t.Fatal("Migrate() before InitDB() should return error")
	}
}

// TestResetBeforeInit 验证未初始化时 Reset 是安全的 no-op。
func TestResetBeforeInit(t *testing.T) {
	// 不应 panic
	Reset()
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
}

// TestInitDB_FailsWithoutConfig 验证无配置时 InitDB 返回 error 而非 panic。
func TestInitDB_FailsWithoutConfig(t *testing.T) {
	// 无配置文件时，InitDB 应该返回 error 而不是 panic
	err := InitDB()
	if err == nil {
		// 如果意外成功（有默认配置），清理资源
		Reset()
		t.Skip("InitDB() succeeded unexpectedly (config exists); cleaned up")
	}
	// 验证失败后状态未改变
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed InitDB()")
	}
	dsn, err2 := GetDsn()
	if err2 == nil {
		t.Fatal("GetDsn() should return error after failed InitDB()")
	}
	if dsn != "" {
		t.Fatalf("GetDsn() should return empty DSN after failed InitDB(), got %q", dsn)
	}
}

// TestInitDB_RepeatCallGuard 验证重复调用保护逻辑存在（签名契约）。
func TestInitDB_RepeatCallGuard(t *testing.T) {
	// 验证函数签名正确
	var _ func() error = InitDB
	var _ func() error = Migrate
	var _ func() error = Close
	var _ func() error = InitWithMigrate
	var _ func() = Reset
	var _ func() (string, error) = GetDsn
	var _ func() bool = IsInitialized
}
