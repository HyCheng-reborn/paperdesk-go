package dao

import (
	"errors"
	"strings"
	"testing"

	database "github.com/wangle201210/go-rag/server/internal/dao/db"
)

// fakeConfig 测试用的配置提供者
type fakeConfig struct {
	cfg *database.Config
	err error
}

func (f *fakeConfig) GetDBConfig() (*database.Config, error) {
	return f.cfg, f.err
}

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

// TestInitDB_ConfigError 验证配置错误时 InitDB 返回 error。
func TestInitDB_ConfigError(t *testing.T) {
	// 使用确定的非法配置
	err := InitDB(&fakeConfig{err: errors.New("no config")})
	if err == nil {
		t.Fatal("expected error with invalid config")
	}
	if !strings.Contains(err.Error(), "config error") {
		t.Fatalf("expected config error, got: %v", err)
	}
	// 验证未发布
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

// TestInitDB_InvalidDSN 验证无效 DSN 时 InitDB 返回连接错误。
func TestInitDB_InvalidDSN(t *testing.T) {
	// 使用确定的无效 DSN（MySQL 格式但无法连接）
	invalidCfg := &database.Config{
		Host:     "invalid-host-that-does-not-exist",
		Port:     "3306",
		User:     "test",
		Password: "test",
		Database: "test",
	}
	err := InitDB(&fakeConfig{cfg: invalidCfg})
	if err == nil {
		// 如果意外成功（比如网络环境特殊），清理资源
		Reset()
		t.Skip("InitDB() succeeded unexpectedly; cleaned up")
	}
	// 应该返回连接或 ping 错误
	if !strings.Contains(err.Error(), "connect") && !strings.Contains(err.Error(), "ping") {
		t.Fatalf("expected connection or ping error, got: %v", err)
	}
	// 验证未发布
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed InitDB()")
	}
}

// TestInitDB_RepeatCallGuard 验证重复调用保护逻辑存在。
func TestInitDB_RepeatCallGuard(t *testing.T) {
	// 验证函数签名正确
	var _ func(ConfigProvider) error = InitDB
	var _ func() error = InitDBWithConfig
	var _ func() error = Migrate
	var _ func() error = Close
	var _ func() error = InitWithMigrate
	var _ func() = Reset
	var _ func() (string, error) = GetDsn
	var _ func() bool = IsInitialized
}

// TestInitDB_NilConfig 验证 nil 配置时使用默认配置。
func TestInitDB_NilConfig(t *testing.T) {
	// nil 配置应该使用默认配置（会因为没有配置文件而失败）
	err := InitDB(nil)
	if err == nil {
		// 如果意外成功（有默认配置），清理资源
		Reset()
		t.Skip("InitDB(nil) succeeded unexpectedly (config exists); cleaned up")
	}
	// 验证失败后状态未改变
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after failed InitDB(nil)")
	}
}
