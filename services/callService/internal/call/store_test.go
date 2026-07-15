package call

import (
	"context"
	"errors"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisStoreEnforcesBusyAndAcceptState(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := NewRedisStore(client, 30*time.Second, time.Hour)
	now := time.Now().UTC()
	session := Session{
		ID: "call-1", CallerUserID: 1, CalleeUserID: 2, CallType: callpb.CallType_AUDIO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "sdp"},
		CreatedAt: now, RingDeadline: now.Add(30 * time.Second),
	}
	if err := store.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.CreateSession(context.Background(), session); err != nil {
		t.Fatalf("same operation should replay existing session: %v", err)
	}
	busy := session
	busy.ID = "call-2"
	busy.CallerUserID = 3
	if err := store.CreateSession(context.Background(), busy); !errors.Is(err, ErrUserBusy) {
		t.Fatalf("expected user busy, got %v", err)
	}
	accepted, err := store.AcceptSession(context.Background(), session.ID, 2, Description{Type: "answer", SDP: "answer"})
	if err != nil {
		t.Fatalf("accept session: %v", err)
	}
	if accepted.State != StateActive || accepted.Answer == nil {
		t.Fatalf("unexpected accepted session: %+v", accepted)
	}
}

func TestExpiredCallCleanupDoesNotDeleteNewCallIndex(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := NewRedisStore(client, time.Second, time.Hour)
	now := time.Now().UTC()
	oldSession := Session{
		ID: "old-call", CallerUserID: 1, CalleeUserID: 2, CallType: callpb.CallType_VIDEO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "old"},
		CreatedAt: now, RingDeadline: now.Add(time.Second),
	}
	if err := store.CreateSession(context.Background(), oldSession); err != nil {
		t.Fatal(err)
	}

	server.FastForward(2 * time.Second)
	newSession := Session{
		ID: "new-call", CallerUserID: 1, CalleeUserID: 3, CallType: callpb.CallType_AUDIO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "new"},
		CreatedAt: now.Add(2 * time.Second), RingDeadline: now.Add(32 * time.Second),
	}
	if err := store.CreateSession(context.Background(), newSession); err != nil {
		t.Fatalf("create replacement session: %v", err)
	}
	if _, err := store.ExpireRinging(context.Background(), now.Add(2*time.Second), 10); err != nil {
		t.Fatalf("expire old session: %v", err)
	}
	got, err := server.Get(userCallKey(1))
	if err != nil {
		t.Fatalf("read replacement index: %v", err)
	}
	if got != newSession.ID {
		t.Fatalf("old timeout removed replacement index: got %q want %q", got, newSession.ID)
	}
}

func TestUserTopicRequiresLiveRouteLease(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := NewRedisStore(client, time.Minute, time.Hour)
	ctx := context.Background()
	if err := client.HSet(ctx, "ws_connection_mapping", "42", "df-a").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UserTopic(ctx, 42); !errors.Is(err, ErrUserOffline) {
		t.Fatalf("stale route without lease must be offline, got %v", err)
	}
	if err := client.Set(ctx, "ws_route_lease:42", "df-a|owner-token", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if topic, err := store.UserTopic(ctx, 42); err != nil || topic != "df-a" {
		t.Fatalf("expected live df-a route, topic=%q err=%v", topic, err)
	}
}
