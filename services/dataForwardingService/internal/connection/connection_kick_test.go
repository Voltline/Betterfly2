package connection

import (
	"context"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type recordedKick struct {
	topic   string
	payload string
}

func recordKafkaKicks(manager *ConnectionManager, err error) *[]recordedKick {
	kicks := &[]recordedKick{}
	manager.publishKafkaKick = func(_ context.Context, payload []byte, topic string) error {
		*kicks = append(*kicks, recordedKick{topic: topic, payload: string(payload)})
		return err
	}
	return kicks
}

var previousKickSession = redisClient.SessionData{ConnectionID: "old-conn", ContainerID: "pod-old", OwnerToken: "0a1b2c"}

func TestKickPreviousSessionSkipsKafkaWhenRedisSubscriberReceives(t *testing.T) {
	useLoginTestRedis(t)
	subscription := redisClient.Rdb.Subscribe(context.Background(), "user_kick:pod-old")
	defer subscription.Close()
	if _, err := subscription.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager := NewConnectionManager()
	kicks := recordKafkaKicks(manager, nil)

	if err := manager.kickPreviousSession(context.Background(), &redisClient.DistributedSessionManager{}, "7", previousKickSession); err != nil {
		t.Fatal(err)
	}
	if len(*kicks) != 0 {
		t.Fatalf("Redis delivered the kick, Kafka fallback should not run: %+v", *kicks)
	}
}

func TestKickPreviousSessionFallsBackToTargetPodTopic(t *testing.T) {
	useLoginTestRedis(t)
	manager := NewConnectionManager()
	kicks := recordKafkaKicks(manager, nil)

	if err := manager.kickPreviousSession(context.Background(), &redisClient.DistributedSessionManager{}, "7", previousKickSession); err != nil {
		t.Fatal(err)
	}
	want := recordedKick{topic: "pod-old", payload: "DELETE USER 7 TARGET pod-old OWNER 0a1b2c"}
	if len(*kicks) != 1 || (*kicks)[0] != want {
		t.Fatalf("kick must go to the old Pod's own topic: got %+v want %+v", *kicks, want)
	}
}

func TestKickPreviousSessionWithoutSubscriberToleratesKafkaFailure(t *testing.T) {
	useLoginTestRedis(t)
	manager := NewConnectionManager()
	kicks := recordKafkaKicks(manager, errors.New("kafka unavailable"))

	if err := manager.kickPreviousSession(context.Background(), &redisClient.DistributedSessionManager{}, "7", previousKickSession); err != nil {
		t.Fatalf("best-effort kick to a Pod without subscriber must not block login: %v", err)
	}
	if len(*kicks) != 1 {
		t.Fatalf("expected one Kafka fallback attempt, got %d", len(*kicks))
	}
}

func TestKickPreviousSessionFailsWhenRedisAndKafkaFail(t *testing.T) {
	server := miniredis.RunT(t)
	previous := redisClient.Rdb
	redisClient.Rdb = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() {
		_ = redisClient.Rdb.Close()
		redisClient.Rdb = previous
	})
	server.Close()
	manager := NewConnectionManager()
	recordKafkaKicks(manager, errors.New("kafka unavailable"))

	if err := manager.kickPreviousSession(context.Background(), &redisClient.DistributedSessionManager{}, "7", previousKickSession); err == nil {
		t.Fatal("login must fail when neither Redis nor Kafka accepted the kick")
	}
}
