package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"momentsService/internal/httpserver"
	"momentsService/internal/media"
	"momentsService/internal/moments"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	defer logger.Sync()
	if os.Getenv("DB_MAX_OPEN_CONNS") == "" {
		os.Setenv("DB_MAX_OPEN_CONNS", "6")
	}
	if os.Getenv("DB_MAX_IDLE_CONNS") == "" {
		os.Setenv("DB_MAX_IDLE_CONNS", "2")
	}
	origin, err := httpserver.Origin(env("MOMENTS_PUBLIC_URL", "http://localhost:8087"))
	if err != nil {
		logger.Sugar().Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	objects, err := media.New(ctx, origin)
	cancel()
	if err != nil {
		logger.Sugar().Fatalw("Moments private storage unavailable", "error", err)
	}
	conn, err := grpc.NewClient(env("AUTH_RPC_ADDR", "localhost:50051"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Sugar().Fatal(err)
	}
	defer conn.Close()
	database := db.DB()
	sql, _ := database.DB()
	defer sql.Close()
	service := moments.New(database, objects)
	if os.Getenv("METRICS_ENABLED") != "false" {
		moments.RegisterMetrics(database)
	}
	server := &http.Server{Addr: ":" + env("PORT", "8087"), Handler: httpserver.New(service, httpserver.RPCAuth{Client: pb.NewAuthServiceClient(conn)}, origin).Routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errorsCh := make(chan error, 1)
	go func() {
		logger.Sugar().Infow("MomentsService HTTP started", "address", server.Addr)
		errorsCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Sugar().Errorw("Moments HTTP stopped", "error", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Sugar().Errorw("Moments shutdown", "error", err)
	}
}
