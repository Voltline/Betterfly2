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
	if _, err := store.CreateSessionWithEvents(context.Background(), session, "create-1", nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if replayed, err := store.CreateSessionWithEvents(context.Background(), session, "create-1", nil); err != nil || !replayed {
		t.Fatalf("same operation should replay existing session: %v", err)
	}
	busy := session
	busy.ID = "call-2"
	busy.CallerUserID = 3
	if _, err := store.CreateSessionWithEvents(context.Background(), busy, "create-2", nil); !errors.Is(err, ErrUserBusy) {
		t.Fatalf("expected user busy, got %v", err)
	}
	accepted := session
	answer := Description{Type: "answer", SDP: "answer"}
	accepted.State = StateActive
	accepted.Answer = &answer
	acceptedAt := time.Now().UTC()
	accepted.AcceptedAt = &acceptedAt
	_, err := store.TransitionSessionWithEvents(context.Background(), session, accepted, false, "accept-1", nil)
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
	if _, err := store.CreateSessionWithEvents(context.Background(), oldSession, "old-create", nil); err != nil {
		t.Fatal(err)
	}

	server.FastForward(2 * time.Second)
	newSession := Session{
		ID: "new-call", CallerUserID: 1, CalleeUserID: 3, CallType: callpb.CallType_AUDIO,
		State: StateRinging, Offer: Description{Type: "offer", SDP: "new"},
		CreatedAt: now.Add(2 * time.Second), RingDeadline: now.Add(32 * time.Second),
	}
	if _, err := store.CreateSessionWithEvents(context.Background(), newSession, "new-create", nil); err != nil {
		t.Fatalf("create replacement session: %v", err)
	}
	expired, err := store.ExpiredRinging(context.Background(), now.Add(2*time.Second), 10)
	if err != nil || len(expired) != 1 {
		t.Fatalf("expire old session: %v", err)
	}
	ended := expired[0]
	ended.State = StateEnded
	endedAt := now.Add(2 * time.Second)
	ended.EndedAt = &endedAt
	if _, err := store.TransitionSessionWithEvents(context.Background(), expired[0], ended, true, "old-timeout", nil); err != nil {
		t.Fatal(err)
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
