package dao

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gogf/gf/v2/frame/g"
	database "github.com/wangle201210/go-rag/server/internal/dao/db"
)

var (
	db     database.Database
	dbMu   sync.Mutex
	dbInit bool
)

// InitDB 初始化数据库连接（不含迁移）。
// 必须在程序启动时显式调用，不在 init() 中自动执行，以支持离线测试隔离。
// 重复调用返回 error（幂等拒绝）。
func InitDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()

	if dbInit {
		return fmt.Errorf("dao: InitDB already called")
	}

	ctx := context.Background()
	dbType := g.Cfg().MustGet(ctx, "database.default.type", "mysql").String()
	var (
		cfg *database.Config
		err error
	)
	switch dbType {
	case "mysql", "":
		cfg, _ = GetMysqlConfig()
	case "sqlite":
		cfg, err = GetSqliteConfig()
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	// 使用局部变量构建新实例，成功后才赋值全局
	var newDB database.Database
	switch dbType {
	case "sqlite":
		newDB = database.NewSqlite(cfg)
	default:
		newDB = database.NewMysql(cfg)
	}

	if err = newDB.Connect(); err != nil {
		return fmt.Errorf("dao: connect failed: %w", err)
	}
	if err = newDB.Ping(); err != nil {
		// 连接失败时尝试关闭底层资源
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
	if err := InitDB(); err != nil {
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

// GetDsn 返回当前数据库 DSN。未初始化时返回空字符串。
func GetDsn() string {
	if db == nil {
		return ""
	}
	return db.DSN()
}

// IsInitialized 返回数据库是否已初始化。
func IsInitialized() bool {
	dbMu.Lock()
	defer dbMu.Unlock()
	return dbInit
}

// ensureSQLiteFileDir 确保 SQLite 文件的父目录存在
func ensureSQLiteFileDir(filePath string) error {
	// 获取文件的目录路径
	dir := filepath.Dir(filePath)

	// 检查目录是否存在
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// 目录不存在，创建目录（包括所有必要的父目录）
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
		g.Log().Infof(context.Background(), "Created directory for SQLite database: %s", dir)
	}

	return nil
}

func GetSqliteConfig() (*database.Config, error) {
	ctx := context.Background()
	cfg := &database.Config{}
	cfg.FilePath = g.Cfg().MustGet(ctx, "database.default.host").String()
	if cfg.FilePath == "" {
		return nil, fmt.Errorf("sqlite file path is required when using sqlite")
	}
	if err := ensureSQLiteFileDir(cfg.FilePath); err != nil {
		return nil, fmt.Errorf("failed to create sqlite file directory: %v", err)
	}
	cfg.BusyTimeout = g.Cfg().MustGet(ctx, "database.default.busy_timeout").Int()
	cfg.JournalMode = g.Cfg().MustGet(ctx, "database.default.journal_mode").String()
	cfg.Synchronous = g.Cfg().MustGet(ctx, "database.default.synchronous").String()
	cfg.CacheSize = g.Cfg().MustGet(ctx, "database.default.cache_size").Int()
	cfg.MaxOpenConn = g.Cfg().MustGet(ctx, "database.default.max_open_conns", 1).Int()
	cfg.MaxIdleConn = g.Cfg().MustGet(ctx, "database.default.max_idle_conns", 1).Int()
	cfg.LogLevel = 4
	return cfg, nil
}

func GetMysqlConfig() (*database.Config, error) {
	cfg := g.DB().GetConfig()
	c := &database.Config{
		Host:        cfg.Host,
		Port:        cfg.Port,
		User:        cfg.User,
		Password:    cfg.Pass,
		Database:    cfg.Name,
		Charset:     cfg.Charset,
		MaxOpenConn: cfg.MaxOpenConnCount,
		MaxIdleConn: cfg.MaxIdleConnCount,
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
