package main

import (
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/config"
	"data_forwarding_service/internal/consumer"
	"data_forwarding_service/internal/publisher"
	"data_forwarding_service/internal/utils"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IBM/sarama"
)

// ConsumerRoutine 定义数据中转服务的消费者行为
func ConsumerRoutine() {
	sugar := logger.Sugar()

	topic := os.Getenv("HOSTNAME")
	if topic == "" {
		topic = "message-topic"
	}

	// 所有容器都需要监听踢出消息topic
	kickTopic := "user-kick-topic"
	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = config.DefaultNsServer
	}

	sugar.Debugf("启动 Kafka 消费者, broker: %s, topic: %s", broker, topic)

	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V2_1_0_0
	saramaConfig.Consumer.Return.Errors = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest

	groupID := "message-consumer-group"

	// 解析多个 Kafka broker 地址
	brokerList := utils.SplitBrokers(broker)

	// 等待 Kafka 启动并支持多个 broker
	for _, brokerAddr := range brokerList {
		brokerErr := publisher.WaitForKafkaReady(brokerAddr, 30*time.Second)
		if brokerErr != nil {
			sugar.Fatalf("Kafka 启动超时: %v", brokerErr)
		}
	}

	consumerGroup, err := sarama.NewConsumerGroup(brokerList, groupID, saramaConfig)
	if err != nil {
		sugar.Fatalf("创建 Kafka 消费组失败: %v", err)
	}
	defer consumerGroup.Close()

	ctx := context.Background()
	// 使用全局WebSocket处理器创建消费者处理器
	handler := consumer.NewKafkaConsumerGroupHandlerWithHandler(GetGlobalWebSocketHandler())

	go func() {
		for {
			err := consumerGroup.Consume(ctx, []string{topic, kickTopic}, handler)
			if err != nil {
				if errors.Is(err, sarama.ErrClosedConsumerGroup) {
					sugar.Warnf("Kafka 消费者组已关闭，退出消费循环")
					break
				}

				sugar.Errorf("Kafka 消费错误: %v", err)
				time.Sleep(time.Second)
			}
		}
	}()

	// 等待退出信号
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)
	<-sigterm
	sugar.Debug("Kafka 消费者退出")
}
