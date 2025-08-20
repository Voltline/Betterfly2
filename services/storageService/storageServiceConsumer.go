package main

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"os"
	"os/signal"
	"storageService/internal/consumer"
	"storageService/internal/publisher"
	"strings"
	"syscall"
	"time"

	"github.com/IBM/sarama"
)

// splitBrokers 解析多个 Kafka broker 地址
func splitBrokers(broker string) []string {
	// 将逗号分隔的 broker 地址拆分为数组
	return strings.Split(broker, ",")
}

// ConsumerRoutine 定义存储服务的消费者行为
func ConsumerRoutine() {
	sugar := logger.Sugar()

	topic := "storage-service"
	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = "localhost:9092"
	}

	sugar.Infof("存储服务启动 Kafka 消费者, broker: %s, topic: %s", broker, topic)

	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V2_1_0_0
	saramaConfig.Consumer.Return.Errors = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest

	groupID := "storage-consumer-group"

	// 解析多个 Kafka broker 地址
	brokerList := splitBrokers(broker)

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
	handler := &consumer.KafkaConsumerGroupHandler{}

	go func() {
		for {
			err := consumerGroup.Consume(ctx, []string{topic}, handler)
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
