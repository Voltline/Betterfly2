package main

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/publisher"
	"data_forwarding_service/internal/redis"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()

	sugar.Infoln("Betterfly2服务器启动中")

	// 初始化 Kafka 生产者
	err := publisher.InitKafkaProducer()
	if err != nil {
		sugar.Fatalln(err)
	}
	defer publisher.KafkaProducer.Close()

	// 初始化 Redis 客户端
	err = redisClient.InitRedis()
	if err != nil {
		sugar.Fatalln(err)
	}
	defer redisClient.Rdb.Close()

	go ConsumerRoutine()

	sugar.Infoln("Betterfly2服务器启动完成")
	err = handlers.StartWebSocketServer()
	if err != nil {
		sugar.Fatalln("启动 WebSocket 服务器失败: ", err)
	}
}
