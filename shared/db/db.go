package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"Betterfly2/shared/db/config"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gologger "gorm.io/gorm/logger"
)

const CurrentSchemaVersion = 4

type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

var DB = func() func(dst ...interface{}) *gorm.DB {
	var (
		database *gorm.DB
		once     sync.Once
	)
	return func(_ ...interface{}) *gorm.DB {
		once.Do(func() {
			var err error
			database, err = Open()
			if err != nil {
				logger.Sugar().Fatalln("连接pgsql失败:", err)
			}
			if envBool("DB_AUTO_MIGRATE", false) {
				logger.Sugar().Warn("DB_AUTO_MIGRATE已启用，仅建议开发环境使用")
				err = RunMigrations(database)
			} else if envBool("DB_SCHEMA_CHECK", true) {
				err = CheckSchemaVersion(database)
			}
			if err != nil {
				logger.Sugar().Fatalln("数据库schema不可用:", err)
			}
		})
		return database
	}
}()

func Open() (*gorm.DB, error) {
	dsn := strings.TrimSpace(os.Getenv("PGSQL_DSN"))
	if dsn == "" {
		logger.Sugar().Warnln("未获取到环境变量PGSQL_DSN，将使用默认dsn连接pgsql")
		dsn = config.DefaultDsn
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gologger.Default.LogMode(gologger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := database.DB()
	if err != nil {
		return nil, err
	}
	pool, err := LoadPoolConfig()
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	configurePool(sqlDB, pool)
	metrics.RegisterDBStats(sqlDB)
	logger.Sugar().Infow(
		"数据库连接池已配置",
		"max_open_conns", pool.MaxOpenConns,
		"max_idle_conns", pool.MaxIdleConns,
		"conn_max_lifetime", pool.ConnMaxLifetime,
		"conn_max_idle_time", pool.ConnMaxIdleTime,
	)
	return database, nil
}

func LoadPoolConfig() (PoolConfig, error) {
	config := PoolConfig{
		MaxOpenConns:    envInt("DB_MAX_OPEN_CONNS", 50),
		MaxIdleConns:    envInt("DB_MAX_IDLE_CONNS", 10),
		ConnMaxLifetime: envDuration("DB_CONN_MAX_LIFETIME", time.Hour),
		ConnMaxIdleTime: envDuration("DB_CONN_MAX_IDLE_TIME", 10*time.Minute),
	}
	if config.MaxOpenConns <= 0 {
		return PoolConfig{}, errors.New("DB_MAX_OPEN_CONNS must be positive")
	}
	if config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns {
		return PoolConfig{}, errors.New("DB_MAX_IDLE_CONNS must be between 0 and DB_MAX_OPEN_CONNS")
	}
	if config.ConnMaxLifetime <= 0 || config.ConnMaxIdleTime <= 0 {
		return PoolConfig{}, errors.New("database connection lifetimes must be positive")
	}
	if config.ConnMaxIdleTime > config.ConnMaxLifetime {
		return PoolConfig{}, errors.New("DB_CONN_MAX_IDLE_TIME must not exceed DB_CONN_MAX_LIFETIME")
	}
	return config, nil
}

func configurePool(database *sql.DB, config PoolConfig) {
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	database.SetConnMaxLifetime(config.ConnMaxLifetime)
	database.SetConnMaxIdleTime(config.ConnMaxIdleTime)
}

func CheckSchemaVersion(database *gorm.DB) error {
	if !database.Migrator().HasTable(&SchemaMigration{}) {
		return errors.New("schema_migrations table is missing; run the migrate command")
	}
	var version int
	if err := database.Model(&SchemaMigration{}).Select("COALESCE(MAX(version), 0)").Scan(&version).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version < CurrentSchemaVersion {
		return fmt.Errorf("database schema version %d is older than required version %d", version, CurrentSchemaVersion)
	}
	return nil
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return -1
	}
	return value
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
