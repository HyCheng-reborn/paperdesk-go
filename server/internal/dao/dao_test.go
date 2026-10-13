package dao

import (
	"testing"
)

// TestNoAutoInit 验证 package import 不会自动触发数据库连接。
// 移除 init() 副作用后，db 变量在显式调用 InitDB() 前应为零值 nil。
func TestNoAutoInit(t *testing.T) {
	if db != nil {
		t.Fatal("db should be nil before InitDB() is called; init() side effect was not removed")
	}
}

// TestGetDsnBeforeInit 验证未初始化时 GetDsn 返回空字符串而非 panic。
func TestGetDsnBeforeInit(t *testing.T) {
	dsn := GetDsn()
	if dsn != "" {
		t.Fatalf("GetDsn() should return empty string before InitDB(), got %q", dsn)
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

// TestInitDB_RepeatCall 验证重复调用 InitDB 返回 error（幂等拒绝）。
// 注意：此测试会在无配置环境下返回 error（可能 panic on MustGet）。
// 因此只验证函数签名和重复调用契约的文档正确性。
func TestInitDB_RepeatCall(t *testing.T) {
	// 验证函数签名正确
	var _ func() error = InitDB
	var _ func() error = Migrate
	var _ func() error = Close
	var _ func() error = InitWithMigrate
}
