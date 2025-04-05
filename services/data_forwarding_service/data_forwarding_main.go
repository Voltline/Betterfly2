package main

import (
	"context"
	"data_forwarding_service/config"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/logger_config"
	"data_forwarding_service/internal/publisher"
	"data_forwarding_service/internal/redis_client"
	"github.com/IBM/sarama"
	"go.uber.org/zap"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type KafkaConsumerGroupHandler struct{}

func consumerRoutine() {
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()

	topic := os.Getenv("HOSTNAME")
	if topic == "" {
		topic = "message-topic"
	}
	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = config.DefaultNsServer
	}

	sugar.Infof("启动 Kafka 消费者, broker: %s, topic: %s", broker, topic)

	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V2_1_0_0
	saramaConfig.Consumer.Return.Errors = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest

	groupID := "message-consumer-group"

	error := publisher.WaitForKafkaReady(broker, 30*time.Second)
	if error != nil {
		sugar.Fatalf("Kafka 启动超时: %v", error)
	}

	consumerGroup, err := sarama.NewConsumerGroup([]string{broker}, groupID, saramaConfig)
	if err != nil {
		sugar.Fatalf("创建 Kafka 消费组失败: %v", err)
	}
	defer consumerGroup.Close()

	ctx := context.Background()
	handler := &KafkaConsumerGroupHandler{}

	go func() {
		for {
			err := consumerGroup.Consume(ctx, []string{topic}, handler)
			if err != nil {
				sugar.Errorf("Kafka 消费错误: %v", err)
				time.Sleep(time.Second)
			}
		}
	}()

	// 等待退出信号
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)
	<-sigterm
	sugar.Info("Kafka 消费者退出")
}

func main() {
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()
	sugar.Infoln("Betterfly2服务器启动中")

	// 初始化 Kafka 生产者
	err := publisher.InitKafkaProducer()
	if err != nil {
		sugar.Fatalln(err)
	}
	defer publisher.KafkaProducer.Close()

	// 初始化 Redis 客户端
	err = redis_client.InitRedis()
	if err != nil {
		sugar.Fatalln(err)
	}
	defer redis_client.Rdb.Close()

	go consumerRoutine()

	sugar.Infoln("Betterfly2服务器启动完成")
	err = handlers.StartWebSocketServer()
	if err != nil {
		sugar.Fatalln("启动 WebSocket 服务器失败: ", err)
	}
}

func (h *KafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *KafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// 实现samara的消费处理器协议
func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()

	for msg := range claim.Messages() {
		sugar.Infof("Kafka 收到消息: %s", string(msg.Value))
		// TODO: 自定义业务逻辑
		err := handlers.SendMessage(string(msg.Value), "你好，这是来自服务器的回应!")
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			continue
		}
		sugar.Infof("成功发送回显消息: %s", string(msg.Value))

		session.MarkMessage(msg, "")
	}
	return nil
}
