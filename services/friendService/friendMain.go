package main

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"friendService/internal/consumer"
	"friendService/internal/publisher"

	"github.com/IBM/sarama"
)

func main() {
	sugar := logger.Sugar()
	defer func() {
		if err := logger.Sync(); err != nil {
			sugar.Errorf("同步日志失败: %v", err)
		}
	}()

	sugar.Infoln("friend服务启动中...")

	if err := publisher.InitKafkaProducer(); err != nil {
		sugar.Fatalf("初始化 Kafka 生产者失败: %v", err)
	}
	defer func() {
		if publisher.KafkaProducer != nil {
			_ = publisher.KafkaProducer.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumerErrCh := make(chan error, 1)
	go func() {
		consumerErrCh <- startKafkaConsumer(ctx)
	}()

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigterm:
		sugar.Infof("收到终止信号 %s，准备关闭friend服务", sig)
	case err := <-consumerErrCh:
		if err != nil {
			sugar.Errorf("friend服务消费者异常退出: %v", err)
		}
	}

	cancel()
	select {
	case <-consumerErrCh:
	case <-time.After(5 * time.Second):
		sugar.Warn("等待friend服务消费者退出超时")
	}

	sugar.Infoln("friend服务正常退出")
}

func startKafkaConsumer(ctx context.Context) error {
	sugar := logger.Sugar()

	topic := os.Getenv("KAFKA_FRIEND_TOPIC")
	if topic == "" {
		topic = "friend-service"
	}

	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = "localhost:9092"
	}

	groupID := os.Getenv("KAFKA_CONSUMER_GROUP")
	if groupID == "" {
		groupID = "friend-service-group"
	}

	brokerList := strings.Split(broker, ",")
	for _, brokerAddr := range brokerList {
		if err := publisher.WaitForKafkaReady(brokerAddr, 60*time.Second); err != nil {
			sugar.Warnf("Kafka %s 尚未完全就绪: %v", brokerAddr, err)
		}
	}

	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_1_0_0
	cfg.Consumer.Return.Errors = true
	cfg.Consumer.Offsets.Initial = sarama.OffsetNewest

	consumerGroupClient, err := sarama.NewConsumerGroup(brokerList, groupID, cfg)
	if err != nil {
		return err
	}
	defer func() {
		_ = consumerGroupClient.Close()
	}()

	handler := &consumer.KafkaConsumerGroupHandler{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := consumerGroupClient.Consume(ctx, []string{topic}, handler); err != nil {
			if errors.Is(err, sarama.ErrClosedConsumerGroup) || ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
