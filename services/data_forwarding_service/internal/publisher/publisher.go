package publisher

import (
	"data_forwarding_service/config"
	"data_forwarding_service/internal/logger_config"
	"fmt"
	"github.com/IBM/sarama"
	"go.uber.org/zap"
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
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()

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
		log := zap.New(logger_config.CoreConfig, zap.AddCaller())
		defer log.Sync()
		sugar := log.Sugar()

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

		error := WaitForKafkaReady(broker, 30*time.Second)
		if error != nil {
			initErr = fmt.Errorf("Kafka 启动超时: %v", error)
			return
		}

		producer, err := sarama.NewSyncProducer([]string{broker}, sarama_config)
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
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()

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
