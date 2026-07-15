package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/outbox"
	"pushService/internal/apns"
	"pushService/internal/consumer"
	"pushService/internal/http_server"
	"pushService/internal/publisher"
	pushservice "pushService/internal/push"

	"github.com/IBM/sarama"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	brokers := splitEnv("KAFKA_BROKER", "localhost:9092")
	if err := publisher.WaitForBrokers(brokers, 60*time.Second); err != nil {
		sugar.Fatalf("Kafka未就绪: %v", err)
	}
	kafkaPublisher, err := publisher.NewKafkaPublisher(brokers)
	if err != nil {
		sugar.Fatalf("初始化Kafka生产者失败: %v", err)
	}
	defer kafkaPublisher.Close()

	privateKey, keyErr := apns.LoadPrivateKey(os.Getenv("APNS_PRIVATE_KEY_PATH"), os.Getenv("APNS_PRIVATE_KEY_BASE64"))
	var sender pushservice.Sender
	if keyErr != nil {
		sugar.Errorf("APNs未配置，PushService将保持not_ready: %v", keyErr)
		sender = pushservice.UnavailableSender{Err: keyErr}
	} else {
		apnsClient, clientErr := apns.NewClient(apns.Config{
			KeyID: env("APNS_KEY_ID", "C6D5695Q4Y"), TeamID: env("APNS_TEAM_ID", "8R5Q4A3RC7"),
			BundleID: env("APNS_BUNDLE_ID", "com.Voltline.Betterfly2"), PrivateKey: privateKey,
		})
		if clientErr != nil {
			sugar.Errorf("初始化APNs客户端失败，PushService将保持not_ready: %v", clientErr)
			sender = pushservice.UnavailableSender{Err: clientErr}
		} else {
			sender = apnsClient
		}
	}

	store := pushservice.NewGormStore()
	service := pushservice.NewService(store, sender, kafkaPublisher, env("APNS_BUNDLE_ID", "com.Voltline.Betterfly2"))
	relay := outbox.New(db.DB(), func(publishCtx context.Context, event db.OutboxEvent) error {
		headers := []sarama.RecordHeader{
			{Key: []byte("event_id"), Value: []byte(event.EventID)},
			{Key: []byte("operation_key"), Value: []byte(event.OperationKey)},
			{Key: []byte("outbox_service"), Value: []byte(event.Service)},
		}
		return kafkaPublisher.PublishRaw(publishCtx, event.Topic, event.Payload, headers)
	}, outbox.LoadConfig("push", "PUSH"))
	go func() {
		if err := relay.Run(ctx); err != nil && ctx.Err() == nil {
			sugar.Errorf("Push Outbox relay退出: %v", err)
			cancel()
		}
	}()
	go func() {
		if err := service.RunWorkers(ctx); err != nil && ctx.Err() == nil {
			sugar.Errorf("Push持久化worker退出: %v", err)
			cancel()
		}
	}()
	go store.RunCleanup(ctx, pushservice.LoadCleanupConfig())
	go db.RunReliabilityCleanup(ctx, db.DB(), db.LoadRetentionConfig())

	messageTopic := env("KAFKA_PUSH_TOPIC", "push-service")
	consumerGroup, err := newConsumerGroup(brokers, env("KAFKA_CONSUMER_GROUP", "push-service-group"), sarama.OffsetNewest)
	if err != nil {
		sugar.Fatalf("初始化Kafka消费者失败: %v", err)
	}
	defer consumerGroup.Close()
	go consume(ctx, consumerGroup, messageTopic, consumer.NewHandler(service, kafkaPublisher.PublishRaw))

	voipTopic := env("KAFKA_VOIP_PUSH_TOPIC", "push-service-voip")
	sugar.Infof("PushService Kafka消费者启动: message_topic=%s voip_topic=%s", messageTopic, voipTopic)
	if voipTopic != messageTopic {
		voipConsumerGroup, groupErr := newConsumerGroup(brokers, env("KAFKA_VOIP_CONSUMER_GROUP", "push-service-voip-group"), sarama.OffsetOldest)
		if groupErr != nil {
			sugar.Fatalf("初始化VoIP Kafka消费者失败: %v", groupErr)
		}
		defer voipConsumerGroup.Close()
		go consume(ctx, voipConsumerGroup, voipTopic, consumer.NewHandler(service, kafkaPublisher.PublishRaw))
	}

	httpServer := &http.Server{
		Addr: ":" + env("HTTP_PORT", "8086"), Handler: http_server.New(service).Handler(), ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		sugar.Infof("PushService HTTP服务启动: %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Errorf("PushService HTTP服务退出: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func newConsumerGroup(brokers []string, groupID string, initialOffset int64) (sarama.ConsumerGroup, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V2_1_0_0
	config.Consumer.Offsets.Initial = initialOffset
	return sarama.NewConsumerGroup(brokers, groupID, config)
}

func consume(ctx context.Context, group sarama.ConsumerGroup, topic string, handler sarama.ConsumerGroupHandler) {
	for ctx.Err() == nil {
		if err := group.Consume(ctx, []string{topic}, handler); err != nil && ctx.Err() == nil {
			logger.Sugar().Errorf("PushService Kafka消费失败: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitEnv(key, fallback string) []string { return strings.Split(env(key, fallback), ",") }
