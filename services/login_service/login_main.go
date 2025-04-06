package main

import (
	"fmt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"time"

	"Betterfly2/shared/db_op/db_config"
	"Betterfly2/shared/logger"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	sugar.Infoln("login服务启动中...")
	dsn := os.Getenv("PGSQL_DSN")
	if dsn == "" {
		sugar.Warnln("未获取到环境变量PGSQL_DSN，将使用默认dsn连接pgsql")
		dsn = db_config.DefaultDsn
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		sugar.Errorln("连接pgsql失败:", err)
	}

	// 获取底层连接池对象
	sqlDB, err := db.DB()
	if err != nil {
		sugar.Errorln("获取pgsql对象失败:", err)
	}

	// 设置连接池参数
	sqlDB.SetMaxOpenConns(50)                  // 最大打开连接数
	sqlDB.SetMaxIdleConns(10)                  // 最大空闲连接数
	sqlDB.SetConnMaxLifetime(time.Hour)        // 每个连接最长存活时间
	sqlDB.SetConnMaxIdleTime(10 * time.Minute) // 空闲连接最多保持多久

	fmt.Println("成功连接pgsql")
}
