package publisher

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/config"
	"data_forwarding_service/internal/utils"
	"fmt"
	"github.com/IBM/sarama"
	"net"
	"os"
	"sync"
	"time"
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

		topic := os.Getenv("HOSTNAME")
		broker := os.Getenv("KAFKA_BROKER")
		if topic == "" {
			topic = "message-topic"
		}

		if broker == "" {
			broker = config.DefaultNsServer
		}

		sugar.Infof("当前 Kafka Broker: %s, topic: %s", broker, topic)

		sarama_config := sarama.NewConfig()
		sarama_config.Producer.Return.Successes = true
		sarama_config.Producer.RequiredAcks = sarama.WaitForAll
		sarama_config.Producer.Retry.Max = 5

		// 解析多个 Kafka broker 地址
		brokerList := utils.SplitBrokers(broker)

		for _, brokerAddr := range brokerList {
			error := WaitForKafkaReady(brokerAddr, 30*time.Second)
			if error != nil {
				sugar.Fatalf("Kafka 启动超时: %v", error)
			}
		}

		// 使用多个 broker 地址初始化生产者
		producer, err := sarama.NewSyncProducer(brokerList, sarama_config)
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
