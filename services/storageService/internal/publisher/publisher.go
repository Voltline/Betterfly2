package publisher

import (
	"Betterfly2/shared/logger"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/IBM/sarama"
)

var KafkaProducer sarama.SyncProducer

// WaitForKafkaReady 等待 Kafka 就绪
func WaitForKafkaReady(broker string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	sugar := logger.Sugar()
	retryCount := 0
	maxRetries := int(timeout.Seconds() / 2) // 每2秒重试一次

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", broker, 3*time.Second)
		if err == nil {
			conn.Close()
			sugar.Infof("Kafka %s 连接成功", broker)
			return nil
		}

		retryCount++
		if retryCount <= maxRetries {
			sugar.Warnf("Kafka %s 未启动, 重试中... (尝试 %d/%d)", broker, retryCount, maxRetries)
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("Kafka %s 在 %s 时间内未能成功启动", broker, timeout)
}

// InitKafkaProducer 初始化 Kafka 生产者
func InitKafkaProducer() error {
	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = "localhost:9092"
	}
	logger.Sugar().Infof("当前 Kafka Broker: %s", broker)

	saramaConfig := sarama.NewConfig()
	saramaConfig.Producer.Return.Successes = true
	saramaConfig.Producer.RequiredAcks = sarama.WaitForAll
	saramaConfig.Producer.Retry.Max = 5
	networkTimeout := kafkaNetworkTimeout()
	saramaConfig.Net.DialTimeout = networkTimeout
	saramaConfig.Net.ReadTimeout = networkTimeout
	saramaConfig.Net.WriteTimeout = networkTimeout
	saramaConfig.Producer.Timeout = networkTimeout

	brokerList := strings.Split(broker, ",")
	for _, brokerAddr := range brokerList {
		if err := WaitForKafkaReady(brokerAddr, 60*time.Second); err != nil {
			return err
		}
	}
	producer, err := sarama.NewSyncProducer(brokerList, saramaConfig)
	if err != nil {
		return fmt.Errorf("创建 Kafka 生产者失败: %v", err)
	}
	KafkaProducer = producer
	return nil
}

func kafkaNetworkTimeout() time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv("KAFKA_NETWORK_TIMEOUT")))
	if err != nil || value <= 0 {
		return 10 * time.Second
	}
	return value
}

// PublishMessage 发布消息到 Kafka
func PublishMessage(message string, targetTopic string) error {
	return PublishRawMessageContext(context.Background(), []byte(message), targetTopic, nil)
}

func PublishRawMessageContext(ctx context.Context, payload []byte, targetTopic string, headers []sarama.RecordHeader) error {
	sugar := logger.Sugar()

	if KafkaProducer == nil {
		return fmt.Errorf("尚未初始化 Kafka Producer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	msg := &sarama.ProducerMessage{
		Topic:   targetTopic,
		Value:   sarama.ByteEncoder(payload),
		Headers: headers,
	}

	partition, offset, err := KafkaProducer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("向 Kafka 发布消息失败: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sugar.Infof("Kafka 消息发布成功 - Partition: %d, Offset: %d", partition, offset)
	return nil
}
