package publisher

import (
	"common/logger"
	"context"
	"fmt"
	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
)

var log = logger.NewLogger()

var RocketMQProducer rocketmq.Producer

// InitRocketMQProducer 初始化 RocketMQ 生产者
func InitRocketMQProducer() error {
	var err error
	RocketMQProducer, err = rocketmq.NewProducer(
		producer.WithGroupName("message-group"),
		producer.WithNameServer([]string{"127.0.0.1:9876"}),
	)
	if err != nil {
		return fmt.Errorf("创建RocketMQ生产者错误: %v", err)
	}

	err = RocketMQProducer.Start()
	if err != nil {
		return fmt.Errorf("启动RocketMQ生产者错误: %v", err)
	}
	return nil
}

// PublishMessage 发布消息
func PublishMessage(message string) error {
	msg := &primitive.Message{
		// 还没想好主题怎么设置
		Topic: "message-topic",
		Body:  []byte(message),
	}
	log.Warn.Println("消息内容: ", msg)

	sendResult, err := RocketMQProducer.SendSync(context.Background(), msg)
	if err != nil {
		return fmt.Errorf("向消息队列发布消息错误: %v", err)
	}
	log.Info.Printf("消息发布成功: %v\n", sendResult)
	return nil
}
