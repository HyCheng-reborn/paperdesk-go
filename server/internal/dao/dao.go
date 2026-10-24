package dao

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gogf/gf/v2/frame/g"
	database "github.com/wangle201210/go-rag/server/internal/dao/db"
	gormmodel "github.com/wangle201210/go-rag/server/internal/model/gorm"
	"gorm.io/gorm"
)

var (
	db     database.Database
	dbMu   sync.RWMutex
	dbInit bool
)

// ConfigProvider 可注入的配置提供者接口
// 生产环境使用 defaultConfig，测试使用 fakeConfig
type ConfigProvider interface {
	GetDBConfig() (*database.Config, error)
}

// defaultConfig 生产环境的配置实现
type defaultConfig struct{}

func (c *defaultConfig) GetDBConfig() (*database.Config, error) {
	ctx := context.Background()
	dbType, err := g.Cfg().Get(ctx, "database.default.type")
	if err != nil {
		return nil, fmt.Errorf("failed to read database.default.type: %w", err)
	}
	dbTypeStr := dbType.String()
	if dbTypeStr == "" {
		dbTypeStr = "mysql"
	}

	switch dbTypeStr {
	case "mysql":
		return GetMysqlConfig()
	case "sqlite":
		return GetSqliteConfig()
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbTypeStr)
	}
}

// InitDBWithConfig 使用默认配置和默认工厂初始化数据库（生产入口）
func InitDBWithConfig() error {
	return InitDB(&defaultConfig{}, database.NewDefaultFactory())
}

// InitDB 初始化数据库连接（不含迁移）。
// 必须在程序启动时显式调用，不在 init() 中自动执行，以支持离线测试隔离。
// 重复调用返回 error（幂等拒绝）。
// cfg: 注入的配置提供者；factory: 注入的数据库工厂。
func InitDB(cfg ConfigProvider, factory database.DatabaseFactory) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	if dbInit {
		return fmt.Errorf("dao: InitDB already called")
	}

	if cfg == nil {
		return fmt.Errorf("dao: ConfigProvider must not be nil")
	}
	if factory == nil {
		return fmt.Errorf("dao: DatabaseFactory must not be nil")
	}

	// 使用注入的配置提供者获取配置
	dbCfg, err := cfg.GetDBConfig()
	if err != nil {
		return fmt.Errorf("dao: config error: %w", err)
	}
	if dbCfg == nil {
		return fmt.Errorf("dao: config returned nil config")
	}

	// 使用注入的工厂创建 Database 实例
	newDB, err := factory.Create(*dbCfg)
	if err != nil {
		return fmt.Errorf("dao: factory create failed: %w", err)
	}

	if err = newDB.Connect(); err != nil {
		return fmt.Errorf("dao: connect failed: %w", err)
	}
	if err = newDB.Ping(); err != nil {
		closeErr := newDB.Close()
		return errors.Join(
			fmt.Errorf("dao: ping failed: %w", err),
			closeErr,
		)
	}

	// 成功后才赋值全局变量（原子发布）
	db = newDB
	dbInit = true
	return nil
}

// Migrate 执行数据库迁移（建表/加列/加索引）。
// 必须在 InitDB 成功后调用。
func Migrate() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if !dbInit || db == nil {
		return fmt.Errorf("dao: InitDB must be called before Migrate")
	}
	return db.AutoMigrate()
}

// MigrateFingerprintTable explicitly migrates only the index_fingerprints table.
// This is NOT called during normal startup (design requirement).
// Must be called after InitDB succeeds.
func MigrateFingerprintTable() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if !dbInit || db == nil {
		return fmt.Errorf("dao: InitDB must be called before MigrateFingerprintTable")
	}
	gormDB := db.GormDB()
	if gormDB == nil {
		return fmt.Errorf("dao: GORM DB not available")
	}
	return gormDB.AutoMigrate(&gormmodel.IndexFingerprint{})
}

// InitWithMigrate 连接数据库并执行迁移（兼容入口，等价于 InitDB + Migrate）。
func InitWithMigrate() error {
	if err := InitDB(&defaultConfig{}, database.NewDefaultFactory()); err != nil {
		return err
	}
	return Migrate()
}

// Close 关闭数据库连接。
func Close() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db == nil {
		return nil
	}
	err := db.Close()
	db = nil
	dbInit = false
	return err
}

// Reset 重置 DAO 状态，清理全局变量。用于初始化失败时的级联清理。
// 返回 Close 错误（如果有）。
func Reset() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	var err error
	if db != nil {
		err = db.Close()
		db = nil
	}
	dbInit = false
	return err
}

// GetDsn 返回当前数据库 DSN。未初始化时返回错误。
func GetDsn() (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", fmt.Errorf("dao: database not initialized")
	}
	return db.DSN(), nil
}

// IsInitialized 返回数据库是否已初始化。
func IsInitialized() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return dbInit
}

// GetGormDB 返回底层的 *gorm.DB 实例，供需要 GORM 的模块使用。
// 未初始化时返回错误。
func GetGormDB() (*gorm.DB, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return nil, fmt.Errorf("dao: database not initialized")
	}
	return db.GormDB(), nil
}

// ensureSQLiteFileDir 确保 SQLite 文件的父目录存在
func ensureSQLiteFileDir(filePath string) error {
	dir := filepath.Dir(filePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
		g.Log().Infof(context.Background(), "Created directory for SQLite database: %s", dir)
	}
	return nil
}

// cfgGet 安全读取配置，传播错误而非忽略。
func cfgGet(ctx context.Context, key string, args ...any) (string, error) {
	v, err := g.Cfg().Get(ctx, key, args...)
	if err != nil {
		return "", fmt.Errorf("failed to read config %q: %w", key, err)
	}
	return v.String(), nil
}

// cfgGetInt 安全读取配置整数，传播错误而非忽略。
func cfgGetInt(ctx context.Context, key string, args ...any) (int, error) {
	v, err := g.Cfg().Get(ctx, key, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to read config %q: %w", key, err)
	}
	return v.Int(), nil
}

func GetSqliteConfig() (*database.Config, error) {
	ctx := context.Background()
	cfg := &database.Config{}

	filePath, err := cfgGet(ctx, "database.default.host")
	if err != nil {
		return nil, err
	}
	if filePath == "" {
		return nil, fmt.Errorf("database.default.host (sqlite file path) not configured")
	}
	cfg.FilePath = filePath

	if err := ensureSQLiteFileDir(cfg.FilePath); err != nil {
		return nil, fmt.Errorf("failed to create sqlite file directory: %v", err)
	}

	// BusyTimeout
	busyTimeout, err := cfgGetInt(ctx, "database.default.busy_timeout")
	if err != nil {
		return nil, fmt.Errorf("failed to read busyTimeout: %w", err)
	}
	if busyTimeout == 0 {
		busyTimeout = 5000 // 默认值
	}
	cfg.BusyTimeout = busyTimeout

	// JournalMode
	journalMode, err := cfgGet(ctx, "database.default.journal_mode")
	if err != nil {
		return nil, fmt.Errorf("failed to read journalMode: %w", err)
	}
	if journalMode == "" {
		journalMode = "WAL"
	}
	cfg.JournalMode = journalMode

	// Synchronous
	synchronous, err := cfgGet(ctx, "database.default.synchronous")
	if err != nil {
		return nil, fmt.Errorf("failed to read synchronous: %w", err)
	}
	cfg.Synchronous = synchronous

	// CacheSize
	cacheSize, err := cfgGetInt(ctx, "database.default.cache_size")
	if err != nil {
		return nil, fmt.Errorf("failed to read cacheSize: %w", err)
	}
	cfg.CacheSize = cacheSize

	// MaxOpenConn
	maxOpenConn, err := cfgGetInt(ctx, "database.default.max_open_conns", 1)
	if err != nil {
		return nil, fmt.Errorf("failed to read maxOpenConn: %w", err)
	}
	cfg.MaxOpenConn = maxOpenConn

	// MaxIdleConn
	maxIdleConn, err := cfgGetInt(ctx, "database.default.max_idle_conns", 1)
	if err != nil {
		return nil, fmt.Errorf("failed to read maxIdleConn: %w", err)
	}
	cfg.MaxIdleConn = maxIdleConn

	cfg.LogLevel = 4
	return cfg, nil
}

func GetMysqlConfig() (cfg *database.Config, err error) {
	// g.DB() 会在无配置文件时 panic，使用 recover 捕获
	defer func() {
		if r := recover(); r != nil {
			cfg = nil
			err = fmt.Errorf("database config not found: %v", r)
		}
	}()
	goCfg := g.DB().GetConfig()
	c := &database.Config{
		Host:        goCfg.Host,
		Port:        goCfg.Port,
		User:        goCfg.User,
		Password:    goCfg.Pass,
		Database:    goCfg.Name,
		Charset:     goCfg.Charset,
		MaxOpenConn: goCfg.MaxOpenConnCount,
		MaxIdleConn: goCfg.MaxIdleConnCount,
		LogLevel:    4,
	}
	if c.MaxIdleConn == 0 {
		c.MaxIdleConn = 10
	}
	if c.MaxOpenConn == 0 {
		c.MaxOpenConn = 100
	}
	return c, nil
}
