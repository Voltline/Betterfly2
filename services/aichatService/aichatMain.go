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
	"aichatService/internal/bot"
	"aichatService/internal/config"
	"aichatService/internal/consumer"
	"aichatService/internal/llm"
	"aichatService/internal/media"
	"aichatService/internal/publisher"

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
		sugar.Fatalf("AIChatService配置无效: %v", err)
	}
	database := db.DB()
	ensureCtx, ensureCancel := context.WithTimeout(ctx, 30*time.Second)
	err = bot.EnsureBotUser(ensureCtx, database, bot.Identity{UserID: cfg.BotUserID, Account: cfg.Account, Name: cfg.Name, Avatar: cfg.Avatar})
	ensureCancel()
	if err == nil {
		ensureCtx, ensureCancel = context.WithTimeout(ctx, 30*time.Second)
		err = bot.RegisterKafkaBot(ensureCtx, database, cfg.BotUserID, cfg.Topic, "内置AI助手")
		ensureCancel()
	}
	if err != nil {
		sugar.Fatalf("初始化机器人账号失败: %v", err)
	}

	if err := publisher.WaitForBrokers(cfg.Brokers, 60*time.Second); err != nil {
		sugar.Fatalf("Kafka未就绪: %v", err)
	}
	kafkaPublisher, err := publisher.NewKafkaPublisher(cfg.Brokers)
	if err != nil {
		sugar.Fatalf("初始化Kafka生产者失败: %v", err)
	}
	defer kafkaPublisher.Close()

	var completer bot.Completer
	if cfg.LLMConfigured() {
		completer = llm.New(llm.Config{
			BaseURL: cfg.APIBaseURL, APIKey: cfg.APIKey, Model: cfg.Model, VisionModel: cfg.VisionModel, MaxTokens: cfg.MaxTokens,
			Temperature: cfg.Temperature, Timeout: cfg.RequestTimeout, MaxRetries: cfg.MaxRetries,
		})
	} else {
		sugar.Warn("未配置AICHAT_API_BASE_URL或AICHAT_MODEL，机器人只回复未配置提示")
	}
	var images bot.Images
	if cfg.VisionEnabled() {
		objects := media.NewS3Objects(cfg.RustFSEndpoint, cfg.RustFSRegion, cfg.RustFSAccessKey, cfg.RustFSSecretKey, cfg.RustFSBucket)
		images = media.NewLoader(objects, cfg.ImageMaxBytes, cfg.ImageMaxSide)
	} else if cfg.Vision {
		sugar.Warn("未配置RUSTFS_ENDPOINT_URL或RustFS凭据，AI助手不读取图片")
	}
	service := bot.NewService(cfg, bot.NewGormStore(database), completer, images, kafkaPublisher.PublishKeyed)

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
				sugar.Errorf("AIChatService Kafka消费失败: %v", err)
				time.Sleep(time.Second)
			}
		}
	}()
	sugar.Infow("AIChatService启动", "bot_user_id", cfg.BotUserID, "topic", cfg.Topic, "model", cfg.Model, "llm_configured", cfg.LLMConfigured(), "vision", images != nil)

	mux := http.NewServeMux()
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
	httpServer := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Errorf("AIChatService HTTP服务退出: %v", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}
