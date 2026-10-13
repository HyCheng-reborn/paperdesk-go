package dao

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gogf/gf/v2/frame/g"
	database "github.com/wangle201210/go-rag/server/internal/dao/db"
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
	dbType, _ := g.Cfg().Get(ctx, "database.default.type")
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

// InitDBWithConfig 使用默认配置初始化数据库（生产入口）
func InitDBWithConfig() error {
	return InitDB(&defaultConfig{})
}

// InitDB 初始化数据库连接（不含迁移）。
// 必须在程序启动时显式调用，不在 init() 中自动执行，以支持离线测试隔离。
// 重复调用返回 error（幂等拒绝）。
// cfg 参数允许注入配置提供者，生产环境传 &defaultConfig{}，测试使用 fake。
func InitDB(cfg ConfigProvider) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	if dbInit {
		return fmt.Errorf("dao: InitDB already called")
	}

	// 使用注入的配置提供者获取配置
	var (
		dbCfg *database.Config
		err   error
	)
	if cfg != nil {
		dbCfg, err = cfg.GetDBConfig()
		if err != nil {
			return fmt.Errorf("dao: config error: %w", err)
		}
	} else {
		// 兼容旧调用：如果 cfg 为 nil，使用默认配置
		dbCfg, err = (&defaultConfig{}).GetDBConfig()
		if err != nil {
			return fmt.Errorf("dao: config error: %w", err)
		}
	}

	// 使用局部变量构建新实例，成功后才赋值全局
	var newDB database.Database
	if dbCfg.FilePath != "" {
		// SQLite
		newDB = database.NewSqlite(dbCfg)
	} else {
		// MySQL
		newDB = database.NewMysql(dbCfg)
	}

	if err = newDB.Connect(); err != nil {
		return fmt.Errorf("dao: connect failed: %w", err)
	}
	if err = newDB.Ping(); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("dao: ping failed: %w", err)
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

// InitWithMigrate 连接数据库并执行迁移（兼容入口，等价于 InitDB + Migrate）。
func InitWithMigrate() error {
	if err := InitDB(nil); err != nil {
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
func Reset() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if db != nil {
		_ = db.Close()
		db = nil
	}
	dbInit = false
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

// cfgGet 安全读取配置，不 panic。
func cfgGet(ctx context.Context, key string, args ...any) string {
	v, _ := g.Cfg().Get(ctx, key, args...)
	return v.String()
}

// cfgGetInt 安全读取配置整数，不 panic。
func cfgGetInt(ctx context.Context, key string, args ...any) int {
	v, _ := g.Cfg().Get(ctx, key, args...)
	return v.Int()
}

func GetSqliteConfig() (*database.Config, error) {
	ctx := context.Background()
	cfg := &database.Config{}

	filePath := cfgGet(ctx, "database.default.host")
	if filePath == "" {
		return nil, fmt.Errorf("database.default.host (sqlite file path) not configured")
	}
	cfg.FilePath = filePath

	if err := ensureSQLiteFileDir(cfg.FilePath); err != nil {
		return nil, fmt.Errorf("failed to create sqlite file directory: %v", err)
	}

	cfg.BusyTimeout = cfgGetInt(ctx, "database.default.busy_timeout")
	cfg.JournalMode = cfgGet(ctx, "database.default.journal_mode")
	cfg.Synchronous = cfgGet(ctx, "database.default.synchronous")
	cfg.CacheSize = cfgGetInt(ctx, "database.default.cache_size")
	cfg.MaxOpenConn = cfgGetInt(ctx, "database.default.max_open_conns", 1)
	cfg.MaxIdleConn = cfgGetInt(ctx, "database.default.max_idle_conns", 1)
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
