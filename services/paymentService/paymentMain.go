package main

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"paymentService/internal/grpcClient"
	"paymentService/internal/http_server"
	"paymentService/internal/payment"
	"syscall"
	"time"
)

func main() {
	sugar := logger.Sugar()
	defer func() {
		if err := logger.Sync(); err != nil {
			sugar.Errorf("同步日志失败: %v", err)
		}
	}()
	defer grpcClient.CloseConn()

	store := payment.NewGormStore()
	provider := payment.NewMockProviderFromEnv()
	service := payment.NewService(store, provider)
	server := http_server.NewServer(service)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8084"
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		sugar.Infof("PaymentService HTTP服务启动: port=%s", port)
		errCh <- httpServer.ListenAndServe()
	}()

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigterm:
		sugar.Infof("收到终止信号 %s，准备关闭PaymentService", sig)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Fatalf("PaymentService HTTP服务异常退出: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		sugar.Errorf("PaymentService关闭失败: %v", err)
	}
}
