package main

import (
	"common/logger"
	"context"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/publisher"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/rlog"
	"time"
)

var log = logger.NewLogger()

func consumerRoutine() {
	rlog.SetLogLevel("warn")
	pushConsumer, err := consumer.NewPushConsumer(
		consumer.WithGroupName("message-consumer-group"),
		consumer.WithNameServer([]string{"127.0.0.1:9876"}),
	)
	if err != nil {
		log.Fatal.Fatalf("创建PushConsumer失败: %v", err)
	}

	// 订阅 topic 和消息处理函数
	err = pushConsumer.Subscribe("message-topic", consumer.MessageSelector{}, messageHandler)
	if err != nil {
		log.Fatal.Fatalf("订阅失败: %v", err)
	}

	// 启动消费者
	err = pushConsumer.Start()
	if err != nil {
		log.Fatal.Fatalf("启动PushConsumer失败: %v", err)
	}

	// 保持运行
	select {}
}

func main() {
	log.Info.Println("Hello World")
	// 初始化 RocketMQ 生产者
	err := publisher.InitRocketMQProducer()
	if err != nil {
		log.Fatal.Fatal("初始化RocketMQ生产者失败: ", err)
	}
	defer publisher.RocketMQProducer.Shutdown()

	go consumerRoutine()

	err = handlers.StartWebSocketServer()
	if err != nil {
		log.Fatal.Fatal("启动 WebSocket 服务器失败: ", err)
	}
}

// 消息处理
func messageHandler(context context.Context, msg ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, m := range msg {
		log.Info.Println("消息队列收到消息: ", string(m.Body))
	}
	// TODO: 处理消息
	time.Sleep(1 * time.Second)
	return consumer.ConsumeSuccess, nil
}
