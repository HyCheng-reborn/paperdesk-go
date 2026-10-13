package db

// defaultFactory 生产环境的 DatabaseFactory 实现
// 根据配置中的 FilePath 字段决定创建 MySQL 还是 SQLite 实例
type defaultFactory struct{}

// NewDefaultFactory 创建生产环境的 DatabaseFactory
func NewDefaultFactory() DatabaseFactory {
	return &defaultFactory{}
}

func (f *defaultFactory) Create(cfg Config) (Database, error) {
	if cfg.FilePath != "" {
		return NewSqlite(&cfg), nil
	}
	return NewMysql(&cfg), nil
}
