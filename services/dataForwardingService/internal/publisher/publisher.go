package publisher

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/config"
	"data_forwarding_service/internal/utils"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/IBM/sarama"
)

var (
	KafkaProducer sarama.SyncProducer
	initOnce      sync.Once
)

// WaitForKafkaReady 等待 Kafka 就绪
func WaitForKafkaReady(broker string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	sugar := logger.Sugar()

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", broker, 3*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}

		sugar.Warnf("Kafka %s 未启动, 重试中...", broker)
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("Kafka 在 %s 时间内未能成功启动", timeout)
}

// InitKafkaProducer 初始化 Kafka 生产者
func InitKafkaProducer() error {
	var initErr error
	initOnce.Do(func() {
		sugar := logger.Sugar()

		broker := os.Getenv("KAFKA_BROKER")

		if broker == "" {
			broker = config.DefaultNsServer
		}

		sugar.Debugf("当前 Kafka Broker: %s", broker)

		saramaConfig := sarama.NewConfig()
		saramaConfig.Producer.Return.Successes = true
		saramaConfig.Producer.RequiredAcks = sarama.WaitForAll
		saramaConfig.Producer.Retry.Max = 5

		// 解析多个 Kafka broker 地址
		brokerList := utils.SplitBrokers(broker)

		for _, brokerAddr := range brokerList {
			brokerErr := WaitForKafkaReady(brokerAddr, 30*time.Second)
			if brokerErr != nil {
				sugar.Fatalf("Kafka 启动超时: %v", brokerErr)
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
	sugar.Debugf("Kafka 消息发布成功 - Partition: %d, Offset: %d", partition, offset)
	return nil
}
