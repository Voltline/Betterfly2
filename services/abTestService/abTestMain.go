package main

import (
	"Betterfly2/shared/logger"
	"abTestService/internal/abtest"
	"abTestService/internal/http_server"
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
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

	store := abtest.NewGormStore()
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 3*time.Second)
	bus, busErr := abtest.NewRedisInvalidationBus(redisCtx, os.Getenv("REDIS_ADDR"))
	redisCancel()
	if busErr != nil {
		sugar.Warnf("AB Test跨副本缓存通知不可用，将依赖5秒短TTL收敛: %v", busErr)
	}
	if bus != nil {
		defer bus.Close()
	}
	service := abtest.NewServiceWithInvalidation(store, bus, 0)
	defer service.Close()
	server := http_server.NewServer(service)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		sugar.Infof("ABTestService HTTP服务启动: port=%s", port)
		errCh <- httpServer.ListenAndServe()
	}()

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigterm:
		sugar.Infof("收到终止信号 %s，准备关闭ABTestService", sig)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			sugar.Fatalf("ABTestService HTTP服务异常退出: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		sugar.Errorf("ABTestService关闭失败: %v", err)
	}
}
