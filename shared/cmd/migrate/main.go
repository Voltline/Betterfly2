package main

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
)

func main() {
	database, err := db.Open()
	if err != nil {
		logger.Sugar().Fatalf("连接数据库失败: %v", err)
	}
	if err := db.RunMigrations(database); err != nil {
		logger.Sugar().Fatalf("数据库迁移失败: %v", err)
	}
	logger.Sugar().Infof("数据库迁移完成: schema_version=%d", db.CurrentSchemaVersion)
}
