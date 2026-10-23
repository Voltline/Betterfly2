package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	"data_forwarding_service/internal/router"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestRelationshipChangedEventForwardedToTargetAndOfflineDropped(t *testing.T) {
	event := &friend.RelationshipChangedEvent{
		Request:   &friend.RelationshipRequestInfo{RequestId: 7, RequestType: "friend", RequesterUserId: 1001, TargetUserId: 1002, Status: "pending"},
		Operation: "created", ChangedAt: "2026-10-23T00:00:00.000000000Z",
	}
	var sentTo string
	var sent pb.ResponseMessage
	err := forwardRelationshipChangedEvent(1002, event, func(userID string, payload []byte) error {
		sentTo = userID
		return proto.Unmarshal(payload, &sent)
	})
	if err != nil || sentTo != "1002" {
		t.Fatalf("forward err=%v target=%q", err, sentTo)
	}
	got := sent.GetRelationshipChangedEvent()
	if got.GetOperation() != "created" || got.GetChangedAt() != event.ChangedAt || got.GetRequest().GetRequestId() != 7 || got.GetRequest().GetStatus() != "pending" {
		t.Fatalf("unexpected forwarded event: %+v", got)
	}

	if err := forwardRelationshipChangedEvent(1002, event, func(string, []byte) error { return router.ErrUserOffline }); err != nil {
		t.Fatalf("offline recipient should be dropped, got %v", err)
	}
	injected := errors.New("kafka down")
	if err := forwardRelationshipChangedEvent(1002, event, func(string, []byte) error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("routing failure should be retried, got %v", err)
	}
	if err := forwardRelationshipChangedEvent(0, event, func(string, []byte) error { t.Fatal("invalid target must not be sent"); return nil }); err != nil {
		t.Fatalf("invalid event should be dropped, got %v", err)
	}
}
