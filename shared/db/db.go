package db

import (
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"sync"
	"time"

	"Betterfly2/shared/db/config"
	"Betterfly2/shared/logger"
	gologger "gorm.io/gorm/logger"
)

var DB = func() func() *gorm.DB {
	var (
		db   *gorm.DB
		once sync.Once
	)

	var initModels = func() {
		db.AutoMigrate(&User{})
	}

	return func() *gorm.DB {
		once.Do(func() {
			sugar := logger.Sugar()
			sugar.Infoln("开始连接数据库")
			dsn := os.Getenv("PGSQL_DSN")
			if dsn == "" {
				sugar.Warnln("未获取到环境变量PGSQL_DSN，将使用默认dsn连接pgsql")
				dsn = config.DefaultDsn
			}
			var err error
			db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
				Logger: gologger.Default.LogMode(gologger.Silent),
			})
			if err != nil {
				sugar.Fatalln("连接pgsql失败:", err)
			}

			// 获取底层连接池对象
			sqlDB, err := db.DB()
			if err != nil {
				sugar.Fatalln("获取pgsql对象失败:", err)
			}

			// 设置连接池参数
			sqlDB.SetMaxOpenConns(50)                  // 最大打开连接数
			sqlDB.SetMaxIdleConns(10)                  // 最大空闲连接数
			sqlDB.SetConnMaxLifetime(time.Hour)        // 每个连接最长存活时间
			sqlDB.SetConnMaxIdleTime(10 * time.Minute) // 空闲连接最多保持多久

			sugar.Infoln("自动更新/创建表")
			initModels()
			sugar.Infoln("数据库连接完成")
		})
		return db
	}
}()
