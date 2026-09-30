package router

import (
	"context"
	"data_forwarding_service/internal/connection"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
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

func TestFullLocalQueueIsNotMistakenForOfflineWhenRouteIsMissing(t *testing.T) {
	t.Setenv("HOSTNAME", "local")
	redisServer := miniredis.RunT(t)
	previous := redisClient.Rdb
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	redisClient.Rdb = client
	t.Cleanup(func() { redisClient.Rdb = previous; _ = client.Close() })
	manager := connection.NewConnectionManager()
	added := make(chan *connection.Connection, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		added <- manager.AddConnection(conn)
	}))
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var local *connection.Connection
	select {
	case local = <-added:
	case <-time.After(time.Second):
		t.Fatal("local connection not registered")
	}
	defer manager.RemoveConnection(local.ID)
	if err := manager.Login(context.Background(), local.ID, "42"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(local.SendChan); i++ {
		if err := local.EnqueueMessage([]byte("queued")); err != nil {
			t.Fatal(err)
		}
	}
	redisServer.HDel("ws_connection_mapping", "42")
	redisServer.Del("ws_route_lease:42")
	err = NewRouter(manager).RouteMessage("42", []byte("message"))
	if err == nil || errors.Is(err, ErrUserOffline) || !strings.Contains(err.Error(), "发送通道已满") {
		t.Fatalf("queue failure was lost during route fallback: %v", err)
	}
}
