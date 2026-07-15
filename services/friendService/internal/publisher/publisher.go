package publisher

import (
	"Betterfly2/shared/logger"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IBM/sarama"
)

var (
	KafkaProducer sarama.SyncProducer
	initOnce      sync.Once
)

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

	return fmt.Errorf("Kafka %s 在 %s 时间内未能成功启动", broker, timeout)
}

func InitKafkaProducer() error {
	var initErr error
	initOnce.Do(func() {
		broker := os.Getenv("KAFKA_BROKER")
		if broker == "" {
			broker = "localhost:9092"
		}

		cfg := sarama.NewConfig()
		cfg.Producer.Return.Successes = true
		cfg.Producer.RequiredAcks = sarama.WaitForAll
		cfg.Producer.Retry.Max = 5
		networkTimeout := kafkaNetworkTimeout()
		cfg.Net.DialTimeout = networkTimeout
		cfg.Net.ReadTimeout = networkTimeout
		cfg.Net.WriteTimeout = networkTimeout
		cfg.Producer.Timeout = networkTimeout

		brokerList := strings.Split(broker, ",")
		for _, brokerAddr := range brokerList {
			if err := WaitForKafkaReady(brokerAddr, 60*time.Second); err != nil {
				initErr = err
				return
			}
		}

		producer, err := sarama.NewSyncProducer(brokerList, cfg)
		if err != nil {
			initErr = fmt.Errorf("创建 Kafka 生产者失败: %v", err)
			return
		}
		KafkaProducer = producer
	})
	return initErr
}

func kafkaNetworkTimeout() time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv("KAFKA_NETWORK_TIMEOUT")))
	if err != nil || value <= 0 {
		return 10 * time.Second
	}
	return value
}

func PublishMessage(message string, targetTopic string) error {
	return PublishRawMessageContext(context.Background(), []byte(message), targetTopic, nil)
}

func PublishRawMessageContext(ctx context.Context, payload []byte, targetTopic string, headers []sarama.RecordHeader) error {
	if KafkaProducer == nil {
		return fmt.Errorf("尚未初始化 Kafka Producer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_, _, err := KafkaProducer.SendMessage(&sarama.ProducerMessage{
		Topic:   targetTopic,
		Value:   sarama.ByteEncoder(payload),
		Headers: headers,
	})
	if err != nil {
		return fmt.Errorf("向 Kafka 发布消息失败: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
