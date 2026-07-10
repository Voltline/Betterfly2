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

	"Betterfly2/shared/logger"
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

	consumerConfig := sarama.NewConfig()
	consumerConfig.Version = sarama.V2_1_0_0
	consumerConfig.Consumer.Offsets.Initial = sarama.OffsetNewest
	consumerGroup, err := sarama.NewConsumerGroup(brokers, env("KAFKA_CONSUMER_GROUP", "push-service-group"), consumerConfig)
	if err != nil {
		sugar.Fatalf("初始化Kafka消费者失败: %v", err)
	}
	defer consumerGroup.Close()
	go consume(ctx, consumerGroup, env("KAFKA_PUSH_TOPIC", "push-service"), consumer.NewHandler(service))

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
