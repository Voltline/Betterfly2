package main

import (
	_ "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"storageService/internal/cache"
	_ "storageService/internal/cache"
	_ "storageService/internal/consumer"
	"time"
)

func main() {
	logger.Sugar().Infoln("存储服务启动")
	key := "测试key"
	val := "这是测试1"
	key2 := "测试key2"

	cache.L1Set(key, val, 1*time.Second)
	time.Sleep(2 * time.Second)
	val1, valid := cache.L1Get(key)
	if valid {
		logger.Sugar().Infoln(val1)
	}
	cache.L1Set(key2, val, 5*time.Minute)
	time.Sleep(200 * time.Millisecond)
	val1, valid = cache.L1Get(key2)
	if valid {
		logger.Sugar().Infoln(val1)
	}
	cache.L1Close()
}
