package main

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/grpcClient"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 全局WebSocket处理器实例
var globalWebSocketHandler *handlers.WebSocketHandler

// GetGlobalWebSocketHandler 获取全局WebSocket处理器
func GetGlobalWebSocketHandler() *handlers.WebSocketHandler {
	return globalWebSocketHandler
}

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

	// 创建并设置全局WebSocket处理器
	globalWebSocketHandler = handlers.NewWebSocketHandler()
	// 设置handlers包中的全局实例
	handlers.SetGlobalWebSocketHandler(globalWebSocketHandler)

	go ConsumerRoutine()

	// 启动metrics HTTP服务器
	go func() {
		metricsPort := "9090"
		sugar.Infof("启动metrics HTTP服务器，端口: %s", metricsPort)
		http.Handle("/metrics", promhttp.Handler())
		err := http.ListenAndServe(":"+metricsPort, nil)
		if err != nil {
			sugar.Errorf("metrics HTTP服务器启动失败: %v", err)
		}
	}()

	// 初始化 gRPC 客户端
	_, err = grpcClient.GetAuthClient()
	if err != nil {
		sugar.Fatalln(err)
	}
	defer grpcClient.CloseConn()

	sugar.Infoln("Betterfly2服务器启动完成")

	// 使用全局WebSocket处理器
	err = globalWebSocketHandler.StartWebSocketServer()
	if err != nil {
		sugar.Fatalln("启动 WebSocket 服务器失败: ", err)
	}
}
