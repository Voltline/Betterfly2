package publisher

import (
	"data_forwarding_service/config"
	"data_forwarding_service/internal/logger_config"
	"fmt"
	"github.com/IBM/sarama"
	"go.uber.org/zap"
	"os"
	"sync"
)

var (
	KafkaProducer sarama.SyncProducer
	initOnce      sync.Once
)

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
func PublishMessage(message string) error {
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()

	topic := os.Getenv("HOSTNAME")
	if topic == "" {
		topic = "message-topic"
	}
	if KafkaProducer == nil {
		return fmt.Errorf("尚未初始化 Kafka Producer")
	}

	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(message),
	}

	partition, offset, err := KafkaProducer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("向 Kafka 发布消息失败: %v", err)
	}
	sugar.Infof("Kafka 消息发布成功 - Partition: %d, Offset: %d", partition, offset)
	return nil
}
