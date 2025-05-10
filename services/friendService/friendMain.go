package main

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"os"
)

func main() {
	defer logger.Sync()
	logger.Sugar().Infoln("friend服务启动中...")
	db.DB(&db.Friend{})
	port := os.Getenv("PORT")
	if port == "" {
		port = "54401"
	}

	logger.Sugar().Infoln("friend服务启动成功！端口:", port)
}
