package call

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	"github.com/IBM/sarama"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestConcurrentCreateOperationProducesOneSessionAndOneLogicalEvent(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, time.Minute, time.Hour)
	now := time.Now().UTC()
	session := Session{
		ID: "call-concurrent", CallerUserID: 10, CalleeUserID: 20, CallType: callpb.CallType_VIDEO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "offer-sdp"},
		CreatedAt: now, RingDeadline: now.Add(time.Minute),
	}
	events := []PendingEvent{{
		EventID: "call:call-concurrent:outgoing", OperationKey: "call-service/0/100",
		Topic: "df-caller", Payload: []byte("outgoing-envelope"),
	}}

	start := make(chan struct{})
	errorsByWorker := make(chan error, 32)
	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.CreateSessionWithEvents(context.Background(), session, "call-service/0/100", events)
			errorsByWorker <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	if length, err := client.XLen(context.Background(), callOutboxStream).Result(); err != nil || length != 1 {
		t.Fatalf("duplicate operation generated logical events: stream_length=%d err=%v", length, err)
	}
	if callID, err := client.Get(context.Background(), userCallKey(10)).Result(); err != nil || callID != session.ID {
		t.Fatalf("session ownership was not committed once: call_id=%q err=%v", callID, err)
	}
	if completed, err := store.OperationCompleted(context.Background(), "call-service/0/100"); err != nil || !completed {
		t.Fatalf("operation ledger missing: completed=%v err=%v", completed, err)
	}
}

func TestConcurrentTransitionOperationProducesOneAcceptedEvent(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, time.Minute, time.Hour)
	now := time.Now().UTC()
	expected := Session{
		ID: "call-accept", CallerUserID: 1, CalleeUserID: 2, CallType: callpb.CallType_AUDIO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "offer"},
		CreatedAt: now, RingDeadline: now.Add(time.Minute),
	}
	if _, err := store.CreateSessionWithEvents(context.Background(), expected, "create-op", []PendingEvent{{EventID: "created", OperationKey: "create-op", Topic: "df", Payload: []byte("created")}}); err != nil {
		t.Fatal(err)
	}
	updated := expected
	updated.State = StateActive
	answer := Description{Type: "answer", SDP: "answer"}
	updated.Answer = &answer
	updated.AcceptedAt = &now
	events := []PendingEvent{{EventID: "accepted", OperationKey: "accept-op", Topic: "df-caller", Payload: []byte("accepted")}}

	start := make(chan struct{})
	errorsByWorker := make(chan error, 16)
	var workers sync.WaitGroup
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.TransitionSessionWithEvents(context.Background(), expected, updated, false, "accept-op", events)
			errorsByWorker <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	if length, err := client.XLen(context.Background(), callOutboxStream).Result(); err != nil || length != 2 {
		t.Fatalf("accept replay generated duplicate events: stream_length=%d err=%v", length, err)
	}
	stored, err := store.GetSession(context.Background(), expected.ID)
	if err != nil || stored.State != StateActive || stored.Answer == nil || stored.Answer.SDP != "answer" {
		t.Fatalf("atomic accept state is invalid: session=%+v err=%v", stored, err)
	}
}

func TestCallRelayReplaysWhenProcessCrashesAfterPublish(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	ctx := context.Background()
	if err := client.XGroupCreateMkStream(ctx, callOutboxStream, callOutboxGroup, "0").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.XAdd(ctx, &redis.XAddArgs{Stream: callOutboxStream, Values: map[string]any{
		"event_id": "call-event-1", "operation_key": "call-op-1", "topic": "df-caller", "payload": []byte("envelope"),
	}}).Result(); err != nil {
		t.Fatal(err)
	}
	streams, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: callOutboxGroup, Consumer: "worker-before-crash", Streams: []string{callOutboxStream, ">"}, Count: 1,
	}).Result()
	if err != nil || len(streams) != 1 || len(streams[0].Messages) != 1 {
		t.Fatalf("claim stream event: streams=%+v err=%v", streams, err)
	}
	messages := streams[0].Messages
	var publishes atomic.Int32
	relay := NewEventRelay(client, func(context.Context, string, []byte, []sarama.RecordHeader) error {
		publishes.Add(1)
		return client.Close()
	})
	if err := relay.process(ctx, messages); err == nil || !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("expected crash between publish and ledger write, got %v", err)
	}

	recoveredClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = recoveredClient.Close() })
	recovered := NewEventRelay(recoveredClient, func(context.Context, string, []byte, []sarama.RecordHeader) error {
		publishes.Add(1)
		return nil
	})
	if err := recovered.process(ctx, messages); err != nil {
		t.Fatal(err)
	}
	if publishes.Load() != 2 {
		t.Fatalf("event was not replayed across the at-least-once crash boundary: publishes=%d", publishes.Load())
	}
	if exists, err := recoveredClient.Exists(ctx, "call:outbox:sent:call-event-1").Result(); err != nil || exists != 1 {
		t.Fatalf("recovered send ledger missing: exists=%d err=%v", exists, err)
	}
}

func TestCallLedgersOutliveKafkaReplayWindow(t *testing.T) {
	t.Setenv("KAFKA_MAX_REPLAY_WINDOW", "168h")
	t.Setenv("CALL_OPERATION_RETENTION", "24h")
	t.Setenv("CALL_OUTBOX_SENT_RETENTION", "24h")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisStore(client, time.Minute, time.Hour)
	relay := NewEventRelay(client, func(context.Context, string, []byte, []sarama.RecordHeader) error { return nil })
	minimum := 168*time.Hour + 24*time.Hour
	if store.operationTTL < minimum || relay.ledgerTTL < minimum {
		t.Fatalf("call ledgers can expire inside the replay window: operation=%s sent=%s", store.operationTTL, relay.ledgerTTL)
	}
}
