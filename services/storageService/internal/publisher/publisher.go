package publisher

import (
	"Betterfly2/shared/logger"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"
)

// splitBrokers 解析多个 Kafka broker 地址
func splitBrokers(broker string) []string {
	// 将逗号分隔的 broker 地址拆分为数组
	return strings.Split(broker, ",")
}

var (
	KafkaProducer sarama.SyncProducer
	initOnce      sync.Once
)

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
	var initErr error
	initOnce.Do(func() {
		sugar := logger.Sugar()

		broker := os.Getenv("KAFKA_BROKER")

		if broker == "" {
			broker = "localhost:9092"
		}

		sugar.Infof("当前 Kafka Broker: %s", broker)

		saramaConfig := sarama.NewConfig()
		saramaConfig.Producer.Return.Successes = true
		saramaConfig.Producer.RequiredAcks = sarama.WaitForAll
		saramaConfig.Producer.Retry.Max = 5

		// 解析多个 Kafka broker 地址
		brokerList := splitBrokers(broker)

		// 增加等待时间到60秒，因为Kafka容器启动后需要时间完全就绪
		for _, brokerAddr := range brokerList {
			brokerErr := WaitForKafkaReady(brokerAddr, 60*time.Second)
			if brokerErr != nil {
				sugar.Errorf("Kafka %s 启动超时: %v", brokerAddr, brokerErr)
				initErr = brokerErr
				return
			}
		}

		// 使用多个 broker 地址初始化生产者
		producer, err := sarama.NewSyncProducer(brokerList, saramaConfig)
		if err != nil {
			initErr = fmt.Errorf("创建 Kafka 生产者失败: %v", err)
			return
		}
		KafkaProducer = producer
	})
	return initErr
}

// PublishMessage 发布消息到 Kafka
func PublishMessage(message string, targetTopic string) error {
	sugar := logger.Sugar()

	if KafkaProducer == nil {
		return fmt.Errorf("尚未初始化 Kafka Producer")
	}

	msg := &sarama.ProducerMessage{
		Topic: targetTopic,
		Value: sarama.ByteEncoder(message),
	}

	partition, offset, err := KafkaProducer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("向 Kafka 发布消息失败: %v", err)
	}
	sugar.Infof("Kafka 消息发布成功 - Partition: %d, Offset: %d", partition, offset)
	return nil
}
