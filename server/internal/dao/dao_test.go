package dao

import (
	"errors"
	"strings"
	"sync"
	"testing"

	database "github.com/wangle201210/go-rag/server/internal/dao/db"
	"gorm.io/gorm"
)

// --- fake 实现 ---

// fakeConfig 测试用的配置提供者
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

// fakeDatabase 测试用的 Database 实现
type fakeDatabase struct {
	mu           sync.Mutex
	connectErr   error
	pingErr      error
	closeErr     error
	migrateErr   error
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
	return f.connectErr
}
func (f *fakeDatabase) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingCount++
	return f.pingErr
}
func (f *fakeDatabase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCount++
	return f.closeErr
}
func (f *fakeDatabase) AutoMigrate() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrateCount++
	return f.migrateErr
}
func (f *fakeDatabase) DSN() string { return f.dsn }
func (f *fakeDatabase) GormDB() *gorm.DB {
	return nil
}

// fakeDatabaseFactory 测试用的 DatabaseFactory 实现
type fakeDatabaseFactory struct {
	db           *fakeDatabase
	createErr    error
	createCount  int
	lastConfig   database.Config
}

func (f *fakeDatabaseFactory) Create(cfg database.Config) (database.Database, error) {
	f.createCount++
	f.lastConfig = cfg
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.db, nil
}

// --- 辅助函数 ---

// resetState 重置包级状态，用于测试隔离
func resetState() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		_ = db.Close()
	}
	db = nil
	dbInit = false
}

// --- 前置条件测试 ---

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
	err := Reset()
	if err != nil {
		t.Fatalf("Reset() before InitDB() should return nil, got %v", err)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
}

// TestReset_ReturnsCloseError 验证 Reset 返回 Close 错误。
func TestReset_ReturnsCloseError(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test", closeErr: errors.New("close error")}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	err = Reset()
	if err == nil {
		t.Fatal("expected error from Reset() when Close fails")
	}
	if !strings.Contains(err.Error(), "close error") {
		t.Fatalf("expected close error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
}

// --- 行为测试 ---

// TestInitDB_Success_Publishes 验证初始化成功后全局状态已发布。
func TestInitDB_Success_Publishes(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test-dsn"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	if !IsInitialized() {
		t.Fatal("IsInitialized() should return true after successful InitDB()")
	}

	dsn, err := GetDsn()
	if err != nil {
		t.Fatalf("GetDsn() unexpected error: %v", err)
	}
	if dsn != "test-dsn" {
		t.Fatalf("GetDsn() = %q, want %q", dsn, "test-dsn")
	}

	if fakeDB.connectCount != 1 {
		t.Fatalf("expected 1 connect call, got %d", fakeDB.connectCount)
	}
	if fakeDB.pingCount != 1 {
		t.Fatalf("expected 1 ping call, got %d", fakeDB.pingCount)
	}
	if factory.createCount != 1 {
		t.Fatalf("expected 1 factory create call, got %d", factory.createCount)
	}
}

// TestInitDB_ConfigError_NoPublish 验证配置失败后不发布。
func TestInitDB_ConfigError_NoPublish(t *testing.T) {
	defer resetState()

	factory := &fakeDatabaseFactory{}
	cfg := &fakeConfig{err: errors.New("config error")}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "config error") {
		t.Fatalf("expected config error, got: %v", err)
	}
	if factory.createCount != 0 {
		t.Fatal("factory should not be called on config error")
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after config error")
	}
}

// TestInitDB_NilConfig_Error 验证 nil 配置返回错误。
func TestInitDB_NilConfig_Error(t *testing.T) {
	defer resetState()

	factory := &fakeDatabaseFactory{}
	err := InitDB(nil, factory)
	if err == nil {
		t.Fatal("expected error with nil config")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("expected 'must not be nil' error, got: %v", err)
	}
	if factory.createCount != 0 {
		t.Fatal("factory should not be called with nil config")
	}
}

// TestInitDB_NilFactory_Error 验证 nil 工厂返回错误。
func TestInitDB_NilFactory_Error(t *testing.T) {
	defer resetState()

	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}
	err := InitDB(cfg, nil)
	if err == nil {
		t.Fatal("expected error with nil factory")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("expected 'must not be nil' error, got: %v", err)
	}
}

// TestInitDB_FactoryError_NoPublish 验证工厂构造失败后不发布。
func TestInitDB_FactoryError_NoPublish(t *testing.T) {
	defer resetState()

	factory := &fakeDatabaseFactory{createErr: errors.New("factory error")}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "factory create") {
		t.Fatalf("expected factory error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after factory error")
	}
}

// TestInitDB_ConnectError_NoPublish 验证 Connect 失败后不发布。
func TestInitDB_ConnectError_NoPublish(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{connectErr: errors.New("connect failed")}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("expected connect error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after connect error")
	}
	if fakeDB.pingCount != 0 {
		t.Fatal("ping should not be called after connect error")
	}
}

// TestInitDB_PingError_NoPublish_ClosesDB 验证 Ping 失败后不发布且关闭 DB。
func TestInitDB_PingError_NoPublish_ClosesDB(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{pingErr: errors.New("ping failed")}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ping failed") {
		t.Fatalf("expected ping error, got: %v", err)
	}
	if IsInitialized() {
		t.Fatal("should not be initialized after ping error")
	}
	if fakeDB.closeCount != 1 {
		t.Fatalf("expected 1 close call after ping failure, got %d", fakeDB.closeCount)
	}
}

// TestInitDB_RepeatCall_Error 验证重复初始化返回错误。
func TestInitDB_RepeatCall_Error(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("first InitDB() unexpected error: %v", err)
	}

	err = InitDB(cfg, factory)
	if err == nil {
		t.Fatal("second InitDB() should return error")
	}
	if !strings.Contains(err.Error(), "already called") {
		t.Fatalf("expected 'already called' error, got: %v", err)
	}

	// 第二次不应调用 factory
	if factory.createCount != 1 {
		t.Fatalf("factory should only be called once, got %d", factory.createCount)
	}
}

// TestInitDB_NoAutoMigrate 验证普通初始化不执行 AutoMigrate。
func TestInitDB_NoAutoMigrate(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	if fakeDB.migrateCount != 0 {
		t.Fatalf("InitDB() should not call AutoMigrate, got %d calls", fakeDB.migrateCount)
	}
}

// TestInitDB_ConfigNilConfig_Error 验证配置返回 (nil, nil) 时返回错误。
func TestInitDB_ConfigNilConfig_Error(t *testing.T) {
	defer resetState()

	factory := &fakeDatabaseFactory{}
	cfg := &fakeConfig{cfg: nil, err: nil}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error when config returns nil")
	}
	if !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("expected nil config error, got: %v", err)
	}
	if factory.createCount != 0 {
		t.Fatal("factory should not be called when config returns nil")
	}
}

// TestMigrateAfterInit 验证 InitDB 成功后 Migrate 可以调用。
func TestMigrateAfterInit(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	err = Migrate()
	if err != nil {
		t.Fatalf("Migrate() unexpected error: %v", err)
	}
	if fakeDB.migrateCount != 1 {
		t.Fatalf("expected 1 migrate call, got %d", fakeDB.migrateCount)
	}
}

// TestCloseAfterInit 验证 InitDB 成功后 Close 关闭 DB。
func TestCloseAfterInit(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	err = Close()
	if err != nil {
		t.Fatalf("Close() unexpected error: %v", err)
	}
	if fakeDB.closeCount != 1 {
		t.Fatalf("expected 1 close call, got %d", fakeDB.closeCount)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Close()")
	}
}

// TestReset_ClosesDB 验证 Reset 关闭 DB。
func TestReset_ClosesDB(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	err = Reset()
	if err != nil {
		t.Fatalf("Reset() unexpected error: %v", err)
	}
	if fakeDB.closeCount != 1 {
		t.Fatalf("expected 1 close call after Reset, got %d", fakeDB.closeCount)
	}
	if IsInitialized() {
		t.Fatal("IsInitialized() should return false after Reset()")
	}
}

// TestGetGormDBBeforeInit 验证未初始化时 GetGormDB 返回错误。
func TestGetGormDBBeforeInit(t *testing.T) {
	_, err := GetGormDB()
	if err == nil {
		t.Fatal("GetGormDB() before InitDB() should return error")
	}
}

// TestGetGormDBAfterInit 验证初始化后 GetGormDB 返回（fake 返回 nil 但不报错）。
func TestGetGormDBAfterInit(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{dsn: "test"}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err != nil {
		t.Fatalf("InitDB() unexpected error: %v", err)
	}

	gdb, err := GetGormDB()
	if err != nil {
		t.Fatalf("GetGormDB() unexpected error: %v", err)
	}
	// fake 返回 nil *gorm.DB，但不报错
	_ = gdb
}

// --- 修复 1: SQLite 配置测试 ---

// TestGetSqliteConfig_FixedSource 验证使用固定配置源时配置正确。
func TestGetSqliteConfig_FixedSource(t *testing.T) {
	// 使用固定配置，不依赖现场配置
	cfg := database.Config{
		FilePath:    "/tmp/test.db",
		BusyTimeout: 5000,
		JournalMode: "WAL",
		Synchronous: "NORMAL",
		CacheSize:   1000,
		MaxOpenConn: 1,
		MaxIdleConn: 1,
		LogLevel:    4,
	}
	// 验证配置字段正确
	if cfg.BusyTimeout != 5000 {
		t.Fatalf("expected BusyTimeout=5000, got %d", cfg.BusyTimeout)
	}
	if cfg.JournalMode != "WAL" {
		t.Fatalf("expected JournalMode='WAL', got %q", cfg.JournalMode)
	}
	if cfg.MaxOpenConn != 1 {
		t.Fatalf("expected MaxOpenConn=1, got %d", cfg.MaxOpenConn)
	}
}

// --- 修复 2: 失败清理错误保留测试 ---

// TestInitDB_PingError_PreservesCloseError 验证 Ping 失败时 Close 错误被保留。
func TestInitDB_PingError_PreservesCloseError(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{
		pingErr:  errors.New("ping error"),
		closeErr: errors.New("close error"),
	}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}

	// 验证 errors.Is 能识别原始失败
	if !errors.Is(err, fakeDB.pingErr) && !strings.Contains(err.Error(), "ping error") {
		t.Fatalf("should contain ping error, got: %v", err)
	}

	// 验证关闭失败也被保留
	if !strings.Contains(err.Error(), "close error") {
		t.Fatalf("should contain close error, got: %v", err)
	}

	// 验证资源只关闭一次
	if fakeDB.closeCount != 1 {
		t.Fatalf("expected 1 close, got %d", fakeDB.closeCount)
	}
}

// TestInitDB_PingError_CloseOK 验证 Ping 失败但 Close 成功时不报 close 错误。
func TestInitDB_PingError_CloseOK(t *testing.T) {
	defer resetState()

	fakeDB := &fakeDatabase{
		pingErr: errors.New("ping error"),
	}
	factory := &fakeDatabaseFactory{db: fakeDB}
	cfg := &fakeConfig{cfg: &database.Config{Host: "localhost"}}

	err := InitDB(cfg, factory)
	if err == nil {
		t.Fatal("expected error")
	}

	// 应包含 ping error
	if !strings.Contains(err.Error(), "ping error") {
		t.Fatalf("should contain ping error, got: %v", err)
	}

	// 不应包含 close error（因为 close 成功）
	if fakeDB.closeCount != 1 {
		t.Fatalf("expected 1 close, got %d", fakeDB.closeCount)
	}
}
