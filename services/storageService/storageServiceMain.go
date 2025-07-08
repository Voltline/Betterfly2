package main

import (
	_ "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	_ "storageService/internal/cache"
	_ "storageService/internal/consumer"
	_ "storageService/internal/redis"
)

func main() {
	logger.Sugar().Infoln("存储服务启动")
}
