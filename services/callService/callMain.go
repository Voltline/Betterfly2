package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"Betterfly2/shared/logger"
	callservice "callService/internal/call"
	"callService/internal/consumer"
	"callService/internal/http_server"
	"callService/internal/publisher"

	"github.com/IBM/sarama"
	"github.com/redis/go-redis/v9"
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

	redisClient := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6379")})
	defer redisClient.Close()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		sugar.Fatalf("Redis未就绪: %v", err)
	}

	ringTTL := secondsEnv("CALL_RING_TIMEOUT_SECONDS", 45)
	activeTTL := secondsEnv("CALL_ACTIVE_TTL_SECONDS", 21600)
	credentialTTL := secondsEnv("TURN_CREDENTIAL_TTL_SECONDS", 3600)
	publicHost := env("TURN_PUBLIC_HOST", "localhost")
	store := callservice.NewRedisStore(redisClient, ringTTL, activeTTL)
	ice := callservice.NewStaticICEProvider(
		env("CALL_STUN_URLS", fmt.Sprintf("stun:%s:3478", publicHost)),
		env("CALL_TURN_URLS", fmt.Sprintf("turn:%s:3478?transport=udp,turn:%s:3478?transport=tcp", publicHost, publicHost)),
		env("TURN_SHARED_SECRET", "betterfly-dev-turn-secret"),
		credentialTTL,
	)
	service := callservice.NewService(store, kafkaPublisher, ice, ringTTL)
	eventRelay := callservice.NewEventRelay(redisClient, kafkaPublisher.PublishRaw)
	go func() {
		if err := eventRelay.Run(ctx); err != nil && ctx.Err() == nil {
			sugar.Errorf("Call Redis事件relay退出: %v", err)
			cancel()
		}
	}()

	consumerGroup, err := sarama.NewConsumerGroup(brokers, env("KAFKA_CONSUMER_GROUP", "call-service-group"), sarama.NewConfig())
	if err != nil {
		sugar.Fatalf("初始化Kafka消费者失败: %v", err)
	}
	defer consumerGroup.Close()
	go consume(ctx, consumerGroup, env("KAFKA_CALL_TOPIC", "call-service"), consumer.NewHandler(service, kafkaPublisher.PublishRaw))
	go sweep(ctx, service)

	httpServer := &http.Server{
		Addr:              ":" + env("HTTP_PORT", "8085"),
		Handler:           http_server.New(service).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		sugar.Infof("callService HTTP服务启动: %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Errorf("callService HTTP服务退出: %v", err)
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
			logger.Sugar().Errorf("callService Kafka消费失败: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func sweep(ctx context.Context, service *callservice.Service) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.SweepExpired(ctx); err != nil {
				logger.Sugar().Errorf("清理超时通话失败: %v", err)
			}
		}
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func splitEnv(key, fallback string) []string {
	return strings.Split(env(key, fallback), ",")
}

func secondsEnv(key string, fallback int64) time.Duration {
	value, err := strconv.ParseInt(env(key, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}
