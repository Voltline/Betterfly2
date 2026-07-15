package main

import (
	_ "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/outbox"
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

	"storageService/internal/cache"
	"storageService/internal/consumer"
	"storageService/internal/http_server"
	"storageService/internal/publisher"

	"github.com/IBM/sarama"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	sugar := logger.Sugar()
	defer func() {
		if err := logger.Sync(); err != nil {
			sugar.Errorf("同步日志失败: %v", err)
		}
	}()

	sugar.Infoln("存储服务启动中")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. 初始化 Kafka 生产者
	sugar.Infoln("初始化 Kafka 生产者...")
	err := publisher.InitKafkaProducer()
	if err != nil {
		sugar.Errorf("初始化 Kafka 生产者失败: %v，将在后台重试", err)
		// 不直接退出，允许服务继续启动，Kafka连接会在后台重试
		// 或者可以选择退出：sugar.Fatalf("初始化 Kafka 生产者失败: %v", err)
	}
	defer func() {
		if publisher.KafkaProducer != nil {
			if err := publisher.KafkaProducer.Close(); err != nil {
				sugar.Errorf("关闭Kafka生产者失败: %v", err)
			}
		}
	}()

	// 2. 初始化缓存
	sugar.Infoln("初始化缓存...")
	initCache()

	// Inbox事务提交后由Outbox后台投递响应，Kafka offset不再依赖同步网络发布。
	database := db.DB()
	relay := outbox.New(database, func(publishCtx context.Context, event db.OutboxEvent) error {
		headers := []sarama.RecordHeader{
			{Key: []byte("event_id"), Value: []byte(event.EventID)},
			{Key: []byte("operation_key"), Value: []byte(event.OperationKey)},
			{Key: []byte("outbox_service"), Value: []byte(event.Service)},
		}
		return publisher.PublishRawMessageContext(publishCtx, event.Payload, event.Topic, headers)
	}, outbox.LoadConfig("storage", "STORAGE"))
	go func() {
		if err := relay.Run(ctx); err != nil && ctx.Err() == nil {
			sugar.Errorf("Storage Outbox relay退出: %v", err)
			cancel()
		}
	}()
	go db.RunReliabilityCleanup(ctx, database, db.LoadRetentionConfig())

	// 3. 初始化HTTP服务器
	sugar.Infoln("初始化HTTP服务器...")
	httpServer, err := http_server.NewHTTPServer()
	if err != nil {
		sugar.Fatalf("初始化HTTP服务器失败: %v", err)
	}

	// 4. 启动HTTP服务器（在goroutine中）
	go func() {
		if err := httpServer.Start(); err != nil && err != http.ErrServerClosed {
			sugar.Fatalf("HTTP服务器启动失败: %v", err)
		}
	}()

	if envBool("METRICS_ENABLED", true) {
		go func() {
			metricsPort := "9091"
			sugar.Infof("启动metrics HTTP服务器，端口: %s", metricsPort)
			http.Handle("/metrics", promhttp.Handler())
			err := http.ListenAndServe(":"+metricsPort, nil)
			if err != nil {
				sugar.Errorf("metrics HTTP服务器启动失败: %v", err)
			}
		}()
	} else {
		sugar.Info("metrics HTTP服务器已禁用")
	}

	// 5. 启动 Kafka 消费者（后台运行，跟随主上下文退出）
	sugar.Infoln("启动 Kafka 消费者...")
	consumerErrCh := make(chan error, 1)
	go func() {
		consumerErrCh <- startKafkaConsumer(ctx)
	}()

	sugar.Infoln("存储服务启动完成，等待终止信号...")

	// 6. 等待终止信号
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigterm:
		sugar.Infof("收到终止信号 %s，准备关闭服务", sig)
	case err := <-consumerErrCh:
		if err != nil {
			sugar.Errorf("Kafka 消费者异常退出: %v", err)
		} else {
			sugar.Info("Kafka 消费者已停止")
		}
	}

	// 优雅关闭
	sugar.Infoln("收到终止信号，正在优雅关闭...")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// 关闭HTTP服务器
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		sugar.Errorf("关闭HTTP服务器失败: %v", err)
	}

	select {
	case err := <-consumerErrCh:
		if err != nil {
			sugar.Errorf("等待 Kafka 消费者退出时出错: %v", err)
		}
	case <-time.After(5 * time.Second):
		sugar.Warn("等待 Kafka 消费者退出超时")
	}

	sugar.Infoln("存储服务正常退出")
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// initCache 初始化缓存系统
func initCache() {
	sugar := logger.Sugar()

	// 初始化 L1 缓存
	sugar.Info("初始化 L1 缓存 (Ristretto)...")
	_ = cache.NewL1Cache() // 初始化但不存储，handler会重新初始化
	sugar.Info("L1 缓存初始化完成")

	// L2 缓存由 handler.NewStorageHandler() 内部初始化
	// 这里不重复初始化，避免重复连接和提前关闭问题
	sugar.Info("L2 Redis 缓存将在 handler 中初始化")

	// 缓存实例已通过 handler.NewStorageHandler() 内部初始化
	// 这里主要是确保缓存系统就绪
}

// startKafkaConsumer 启动 Kafka 消费者并阻塞直到上下文取消或消费失败
func startKafkaConsumer(ctx context.Context) error {
	sugar := logger.Sugar()

	// 配置
	topic := os.Getenv("KAFKA_STORAGE_TOPIC")
	if topic == "" {
		topic = "storage-service"
	}

	broker := os.Getenv("KAFKA_BROKER")
	if broker == "" {
		broker = "localhost:9092"
	}

	consumerGroup := os.Getenv("KAFKA_CONSUMER_GROUP")
	if consumerGroup == "" {
		consumerGroup = "storage-service-group"
	}

	sugar.Infof("Kafka 配置: broker=%s, group=%s, topic=%s",
		broker, consumerGroup, topic)

	// 等待 Kafka 就绪（增加等待时间到60秒）
	sugar.Info("等待 Kafka 服务就绪...")
	brokerList := strings.Split(broker, ",")
	for _, brokerAddr := range brokerList {
		if err := publisher.WaitForKafkaReady(brokerAddr, 60*time.Second); err != nil {
			sugar.Errorf("Kafka %s 启动超时: %v，消费者将在后台继续重试", brokerAddr, err)
			// 不直接返回错误，允许消费者在后台继续重试连接
		}
	}

	// 创建消费者组配置
	config := sarama.NewConfig()
	config.Version = sarama.V2_1_0_0
	config.Consumer.Return.Errors = true
	config.Consumer.Offsets.Initial = sarama.OffsetNewest

	// 创建消费者组
	sugar.Infof("创建 Kafka 消费者组: %s", consumerGroup)
	consumerGroupClient, err := sarama.NewConsumerGroup(brokerList, consumerGroup, config)
	if err != nil {
		return fmt.Errorf("创建 Kafka 消费者组失败: %v", err)
	}
	defer func() {
		if err := consumerGroupClient.Close(); err != nil {
			sugar.Errorf("关闭Kafka消费者组失败: %v", err)
		}
	}()

	// 创建消息处理器
	handler := consumer.NewKafkaConsumerGroupHandler(nil)

	// 消费循环遵循外部上下文，避免阻塞主启动流程。
	sugar.Info("启动 Kafka 消息消费循环...")
	for {
		if ctx.Err() != nil {
			sugar.Info("Kafka 消费者收到关闭信号，停止消费")
			return nil
		}

		if err := consumerGroupClient.Consume(ctx, []string{topic}, handler); err != nil {
			if errors.Is(err, sarama.ErrClosedConsumerGroup) || ctx.Err() != nil {
				sugar.Info("Kafka 消费者组已关闭")
				return nil
			}
			return fmt.Errorf("消费错误: %v", err)
		}
	}
}
