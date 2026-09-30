package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "Betterfly2/proto/data_forwarding"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/router"
	"github.com/IBM/sarama/mocks"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestEnsurePostClientMessageIDPreservesExplicitID(t *testing.T) {
	post := &pb.Post{ClientMessageId: "  client-42  "}
	if got := ensurePostClientMessageID(post); got != "client-42" || post.GetClientMessageId() != "client-42" {
		t.Fatalf("explicit client message ID was not normalized: %q", got)
	}
}

func TestEnsurePostClientMessageIDCreatesStableLegacyID(t *testing.T) {
	post := &pb.Post{
		FromId: 1, ToId: 2, Msg: "hello", MsgType: "text", Timestamp: "2026-07-12T09:00:00Z",
	}
	first := ensurePostClientMessageID(post)
	post.ClientMessageId = ""
	second := ensurePostClientMessageID(post)

	if first != second || !strings.HasPrefix(first, "legacy:") {
		t.Fatalf("legacy ID must be stable: first=%q second=%q", first, second)
	}
	post.ClientMessageId = ""
	post.Msg = "different"
	if got := ensurePostClientMessageID(post); got == first {
		t.Fatal("different messages received the same legacy ID")
	}
}

func TestValidatePostPayloadRejectsOversizedClientMessageID(t *testing.T) {
	post := &pb.Post{Msg: "hello", MsgType: "text", ClientMessageId: strings.Repeat("x", 129)}
	if err := validatePostPayload(post); err == nil {
		t.Fatal("expected oversized client_message_id to be rejected")
	}
}

func TestPostIdempotencyKeyScopesBySender(t *testing.T) {
	first := postIdempotencyKey(1, "same-id")
	if second := postIdempotencyKey(2, "same-id"); first == second {
		t.Fatal("idempotency keys must be scoped by sender")
	}
}

func postTestRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	previous := redisClient.Rdb
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	redisClient.Rdb = client
	t.Cleanup(func() { redisClient.Rdb = previous; _ = client.Close() })
	return server
}

func TestPostEffectsCrashRecoveryAndOwnerFencing(t *testing.T) {
	server := postTestRedis(t)
	ctx := context.Background()
	first, err := claimPostEffects(ctx, 42)
	if err != nil || first == "" {
		t.Fatalf("initial claim: %q %v", first, err)
	}
	if _, err := claimPostEffects(ctx, 42); err == nil {
		t.Fatal("pending work was treated as completed")
	}
	server.FastForward(postPendingTTL)
	second, err := claimPostEffects(ctx, 42)
	if err != nil || second == "" || first == second {
		t.Fatalf("crash recovery: %q %v", second, err)
	}
	if err := finishPostEffects(ctx, 42, first, false); err == nil {
		t.Fatal("expired owner removed new reservation")
	}
	if err := finishPostEffects(ctx, 42, first, true); err == nil {
		t.Fatal("expired owner completed new reservation")
	}
	if err := finishPostEffects(ctx, 42, second, true); err != nil {
		t.Fatal(err)
	}
	if next, err := claimPostEffects(ctx, 42); err != nil || next != "" {
		t.Fatalf("completed work replayed: %q %v", next, err)
	}
	if ttl := server.TTL("post:effects:42"); ttl != postEffectsTTL {
		t.Fatalf("completion TTL: %v", ttl)
	}
}

func TestPostEffectsFailureCanRetryAndOldCompletionIsRespected(t *testing.T) {
	server := postTestRedis(t)
	ctx := context.Background()
	owner, err := claimPostEffects(ctx, 43)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishPostEffects(ctx, 43, owner, false); err != nil {
		t.Fatal(err)
	}
	if next, err := claimPostEffects(ctx, 43); err != nil || next == "" {
		t.Fatalf("failed work cannot retry: %q %v", next, err)
	}
	server.Set("post:effects:44", "1")
	if next, err := claimPostEffects(ctx, 44); err != nil || next != "" {
		t.Fatalf("legacy completion replayed: %q %v", next, err)
	}
}

func TestPostAckCachePreservesOldEntryFormat(t *testing.T) {
	server := postTestRedis(t)
	ctx := context.Background()
	if err := CompletePostIdempotency(ctx, 1, "new", 42); err != nil {
		t.Fatal(err)
	}
	claim, err := claimPost(ctx, 1, "new")
	if err != nil || claim.messageID != 42 {
		t.Fatalf("canonical ACK cache: %+v %v", claim, err)
	}
	if value, err := server.Get(postIdempotencyKey(1, "new")); err != nil || value != "ack:42" {
		t.Fatalf("old DF cannot read ACK: %q %v", value, err)
	}
	server.Set(postIdempotencyKey(1, "old"), "ack:41")
	claim, err = claimPost(ctx, 1, "old")
	if err != nil || claim.messageID != 41 {
		t.Fatalf("legacy ACK cache: %+v %v", claim, err)
	}
}

func TestDeliverStoredPostOfflineDoesNotCompleteEffects(t *testing.T) {
	server := postTestRedis(t)
	producer := mocks.NewSyncProducer(t, nil)
	previous := publisher.KafkaProducer
	publisher.KafkaProducer = producer
	t.Cleanup(func() { publisher.KafkaProducer = previous; _ = producer.Close() })
	post := &pb.Post{FromId: 1, ToId: 2, MsgType: "text", Timestamp: "2026-09-30T10:00:00Z"}
	for attempt := 0; attempt < 2; attempt++ {
		producer.ExpectSendMessageAndSucceed()
		if err := DeliverStoredPost(42, post); !errors.Is(err, router.ErrUserOffline) {
			t.Fatalf("offline silently succeeded: %v", err)
		}
		if server.Exists("post:effects:42") {
			t.Fatal("failed delivery marked permanently complete")
		}
	}
}

func TestDeliverStoredPostPropagatesPushPublishFailure(t *testing.T) {
	server := postTestRedis(t)
	producer := mocks.NewSyncProducer(t, nil)
	previous := publisher.KafkaProducer
	publisher.KafkaProducer = producer
	t.Cleanup(func() { publisher.KafkaProducer = previous; _ = producer.Close() })
	producer.ExpectSendMessageAndFail(errors.New("injected push publish failure"))
	err := DeliverStoredPost(42, &pb.Post{FromId: 1, ToId: 2, MsgType: "text"})
	if err == nil || !strings.Contains(err.Error(), "injected push publish failure") {
		t.Fatalf("push failure swallowed: %v", err)
	}
	if server.Exists("post:effects:42") {
		t.Fatal("push failure left completion marker")
	}
}

func TestStoredPostCompletesOnlyAfterKafkaDeliveryAndSuppressesReplay(t *testing.T) {
	server := postTestRedis(t)
	t.Setenv("HOSTNAME", "local")
	server.HSet("ws_connection_mapping", "2", "other-pod")
	server.Set("ws_route_lease:2", "other-pod|test-owner")
	previousHandler := GetWebSocketHandler()
	SetGlobalWebSocketHandler(testWebSocketHandler(testWebSocketConfig()))
	t.Cleanup(func() { SetGlobalWebSocketHandler(previousHandler) })
	producer := mocks.NewSyncProducer(t, nil)
	previousProducer := publisher.KafkaProducer
	publisher.KafkaProducer = producer
	t.Cleanup(func() { publisher.KafkaProducer = previousProducer; _ = producer.Close() })
	post := &pb.Post{FromId: 1, ToId: 2, MsgType: "text", Timestamp: "2026-09-30T10:00:00Z"}
	producer.ExpectSendMessageAndSucceed() // APNs request persisted by Kafka.
	producer.ExpectSendMessageAndFail(errors.New("realtime Kafka failure"))
	if err := DeliverStoredPost(42, post); err == nil {
		t.Fatal("realtime publish failure ignored")
	}
	if server.Exists("post:effects:42") {
		t.Fatal("partial success marked complete")
	}
	producer.ExpectSendMessageAndSucceed()
	producer.ExpectSendMessageAndSucceed()
	if err := DeliverStoredPost(42, post); err != nil {
		t.Fatal(err)
	}
	if value, err := server.Get("post:effects:42"); err != nil || value != "1" {
		t.Fatalf("successful delivery not complete: %q %v", value, err)
	}
	// No further producer expectations: replay must not publish either event.
	if err := DeliverStoredPost(42, post); err != nil {
		t.Fatal(err)
	}
	if post.GetMessageId() != 42 {
		t.Fatal("realtime server ID missing")
	}
}
