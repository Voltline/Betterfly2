package main

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"chatbotService/internal/config"
	"chatbotService/internal/consumer"
	"chatbotService/internal/platform"
	"chatbotService/internal/publisher"

	"github.com/IBM/sarama"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		sugar.Fatalf("ChatbotService配置无效: %v", err)
	}
	database := db.DB()

	if err := publisher.WaitForBrokers(cfg.Brokers, 60*time.Second); err != nil {
		sugar.Fatalf("Kafka未就绪: %v", err)
	}
	kafkaPublisher, err := publisher.NewKafkaPublisher(cfg.Brokers)
	if err != nil {
		sugar.Fatalf("初始化Kafka生产者失败: %v", err)
	}
	defer kafkaPublisher.Close()

	var objects platform.Objects
	if cfg.FilesEnabled() {
		objects = platform.NewS3Objects(cfg.RustFSEndpoint, cfg.RustFSRegion, cfg.RustFSAccessKey, cfg.RustFSSecretKey, cfg.RustFSBucket)
	} else {
		sugar.Warn("未配置RUSTFS_ENDPOINT_URL或RustFS凭据，Bot API不能上传和下载文件")
	}
	if cfg.AdminToken == "" {
		sugar.Warn("未配置CHATBOT_ADMIN_TOKEN，机器人管理接口未开启")
	}
	service := platform.New(cfg, platform.NewStore(database), kafkaPublisher.PublishKeyed, objects)
	dispatcher := platform.NewDispatcher(service)
	go dispatcher.Run(ctx, 4)
	go service.CleanupLoop(ctx, 10*time.Minute)

	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V2_1_0_0
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest
	group, err := sarama.NewConsumerGroup(cfg.Brokers, cfg.ConsumerGroup, saramaConfig)
	if err != nil {
		sugar.Fatalf("初始化Kafka消费者失败: %v", err)
	}
	defer group.Close()
	handler := consumer.NewHandler(service, kafkaPublisher.PublishRaw)
	go func() {
		for ctx.Err() == nil {
			if err := group.Consume(ctx, []string{cfg.Topic}, handler); err != nil && ctx.Err() == nil {
				sugar.Errorf("ChatbotService Kafka消费失败: %v", err)
				time.Sleep(time.Second)
			}
		}
	}()
	sugar.Infow("ChatbotService启动", "topic", cfg.Topic, "http_port", cfg.HTTPPort, "files", objects != nil, "admin", cfg.AdminToken != "")

	mux := http.NewServeMux()
	service.Routes(mux)
	service.AdminRoutes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		sqlDB, err := database.DB()
		if err == nil {
			pingCtx, pingCancel := context.WithTimeout(r.Context(), 2*time.Second)
			err = sqlDB.PingContext(pingCtx)
			pingCancel()
		}
		if err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	// 不设WriteTimeout：长轮询最长等待CHATBOT_MAX_POLL_TIMEOUT，文件上传下载时间取决于大小。
	httpServer := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Errorf("ChatbotService HTTP服务退出: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
