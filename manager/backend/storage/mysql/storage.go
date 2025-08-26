package mysql

import (
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Storage MySQL存储实现
type Storage struct {
	DB     *gorm.DB
	config *Config
}

// NewStorage 创建MySQL存储实例
func NewStorage(config *Config) (*Storage, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	dsn := config.DSN()

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL: %w", err)
	}

	s := &Storage{
		DB:     db,
		config: config,
	}

	s.configureConnectionPool()

	return s, nil
}

// Connect 连接数据库
func (s *Storage) Connect() error {
	dsn := s.config.DSN()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	s.DB = db
	s.configureConnectionPool()
	return nil
}

// configureConnectionPool 配置连接池
func (s *Storage) configureConnectionPool() {
	if s.DB == nil {
		return
	}
	
	sqlDB, err := s.DB.DB()
	if err != nil {
		return
	}
	
	sqlDB.SetMaxIdleConns(s.config.MaxIdleConns)
	sqlDB.SetMaxOpenConns(s.config.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(s.config.ConnMaxLifetime) * time.Second)
}