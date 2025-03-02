package main

import (
	"context"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/logger"
	"data_forwarding_service/internal/publisher"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/rlog"
	"os"
	"time"
)

var log = logger.NewLogger()

func consumerRoutine() {
	rlog.SetLogLevel("warn")
	topic := os.Getenv("HOSTNAME")
	nsServer := os.Getenv("NAMESERVER")
	if topic == "" {
		nsServer = "127.0.0.1:9876"
		topic = "message-topic"
	}
	log.Info.Printf("当前nsServer: %s, topic: %s\n", nsServer, topic)
	pushConsumer, err := consumer.NewPushConsumer(
		consumer.WithGroupName("message-consumer-group"),
		consumer.WithNameServer([]string{nsServer}),
	)
	if err != nil {
		log.Fatal.Fatalf("创建PushConsumer失败: %v", err)
	}

	// 订阅 topic 和消息处理函数
	err = pushConsumer.Subscribe(topic, consumer.MessageSelector{}, messageHandler)
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
	log.Info.Println("Betterfly2服务器启动中")
	// 初始化 RocketMQ 生产者
	err := publisher.InitRocketMQProducer()
	if err != nil {
		log.Fatal.Fatal("初始化RocketMQ生产者失败: ", err)
	}
	defer publisher.RocketMQProducer.Shutdown()

	go consumerRoutine()

	log.Info.Println("Betterfly2服务器启动完成")
	err = handlers.StartWebSocketServer()
	if err != nil {
		log.Fatal.Fatal("启动 WebSocket 服务器失败: ", err)
	}
}

// 消息处理
func messageHandler(context context.Context, msg ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	for _, m := range msg {
		log.Info.Println("消息队列收到消息:", string(m.Body))
		// 未来需要从m.Body解析报文再发回
		err := handlers.SendMessage(string(m.Body), "你好，这是来自服务器的回应!")
		if err != nil {
			continue
		}
		log.Info.Printf("向%v发送回显消息\n", string(m.Body))
	}
	// TODO: 处理消息
	time.Sleep(1 * time.Second)
	return consumer.ConsumeSuccess, nil
}
