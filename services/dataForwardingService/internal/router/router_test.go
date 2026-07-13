package router

import (
	"data_forwarding_service/internal/connection"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRouteMessageDistinguishesOfflineUserFromRedisFailure(t *testing.T) {
	previous := redisClient.Rdb
	t.Cleanup(func() { redisClient.Rdb = previous })
	router := NewRouter(connection.NewConnectionManager())

	server := miniredis.RunT(t)
	redisClient.Rdb = redis.NewClient(&redis.Options{Addr: server.Addr()})
	if err := router.RouteMessage("42", []byte("message")); !errors.Is(err, ErrUserOffline) {
		t.Fatalf("expected explicit offline error, got %v", err)
	}
	_ = redisClient.Rdb.Close()

	redisClient.Rdb = redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = redisClient.Rdb.Close() })
	err := router.RouteMessage("42", []byte("message"))
	if err == nil || errors.Is(err, ErrUserOffline) || errors.Is(err, redisClient.ErrRouteNotFound) {
		t.Fatalf("Redis failure was mistaken for offline delivery: %v", err)
	}
}
