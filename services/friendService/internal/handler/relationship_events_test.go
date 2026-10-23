package handler

import (
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
)

func relationshipOperationResponse(result friend.FriendResult, operation string, request *friend.RelationshipRequestInfo) *friend.ResponseMessage {
	return &friend.ResponseMessage{Result: result, TargetUserId: 1, Payload: &friend.ResponseMessage_RelationshipOperationRsp{
		RelationshipOperationRsp: &friend.RelationshipOperationRsp{Operation: operation, Request: request},
	}}
}

func decodeRelationshipEvents(t *testing.T, events []db.PendingOutboxEvent) map[int64]*friend.RelationshipChangedEvent {
	t.Helper()
	decoded := map[int64]*friend.RelationshipChangedEvent{}
	for _, event := range events {
		if event.Topic != "df-topic-1" {
			t.Fatalf("event topic = %q, want origin DF topic", event.Topic)
		}
		var env envelope.Envelope
		if err := proto.Unmarshal(event.Payload, &env); err != nil || env.GetType() != envelope.MessageType_FRIEND_RESPONSE {
			t.Fatalf("event envelope invalid: type=%s err=%v", env.GetType(), err)
		}
		var resp friend.ResponseMessage
		if err := proto.Unmarshal(env.GetPayload(), &resp); err != nil {
			t.Fatalf("decode event response: %v", err)
		}
		if resp.GetRelationshipChangedEvent() == nil || resp.GetResult() != friend.FriendResult_FRIEND_OK {
			t.Fatalf("unexpected event response: %+v", &resp)
		}
		if _, duplicate := decoded[resp.GetTargetUserId()]; duplicate {
			t.Fatalf("duplicate event for user %d", resp.GetTargetUserId())
		}
		decoded[resp.GetTargetUserId()] = resp.GetRelationshipChangedEvent()
	}
	return decoded
}

func TestRelationshipEventsNotifyOtherPartyOfFriendRequest(t *testing.T) {
	req := &friend.RequestMessage{FromKafkaTopic: "df-topic-1", TargetUserId: 1001}
	pending := &friend.RelationshipRequestInfo{RequestId: 7, RequestType: db.RequestTypeFriend, RequesterUserId: 1001, TargetUserId: 1002,
		Status: db.RequestStatusPending, CreatedAt: "2026-10-23T00:00:00.000000000Z"}

	events, err := relationshipEventOutbox(nil, "topic/0/1", req, relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "create_friend_request", pending))
	if err != nil {
		t.Fatalf("build created events: %v", err)
	}
	decoded := decodeRelationshipEvents(t, events)
	if len(decoded) != 1 || decoded[1002].GetOperation() != "created" || decoded[1002].GetChangedAt() != pending.CreatedAt || decoded[1002].GetRequest().GetRequestId() != 7 {
		t.Fatalf("created event should go only to target: %+v", decoded)
	}

	cancelled := proto.Clone(pending).(*friend.RelationshipRequestInfo)
	cancelled.Status, cancelled.ResolvedBy, cancelled.ResolvedAt = db.RequestStatusCancelled, 1001, "2026-10-23T01:00:00.000000000Z"
	events, err = relationshipEventOutbox(nil, "topic/0/2", req, relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "resolve_friend_request", cancelled))
	if err != nil {
		t.Fatalf("build cancelled events: %v", err)
	}
	decoded = decodeRelationshipEvents(t, events)
	if len(decoded) != 1 || decoded[1002].GetOperation() != "cancelled" || decoded[1002].GetChangedAt() != cancelled.ResolvedAt {
		t.Fatalf("cancel event should go to target: %+v", decoded)
	}

	accepted := proto.Clone(pending).(*friend.RelationshipRequestInfo)
	accepted.Status, accepted.ResolvedBy, accepted.ResolvedAt = db.RequestStatusAccepted, 1002, "2026-10-23T01:00:00.000000000Z"
	events, err = relationshipEventOutbox(nil, "topic/0/3", &friend.RequestMessage{FromKafkaTopic: "df-topic-1", TargetUserId: 1002},
		relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "resolve_friend_request", accepted))
	if err != nil {
		t.Fatalf("build resolved events: %v", err)
	}
	decoded = decodeRelationshipEvents(t, events)
	if len(decoded) != 1 || decoded[1001].GetOperation() != "resolved" || decoded[1001].GetRequest().GetStatus() != db.RequestStatusAccepted {
		t.Fatalf("resolve event should go to requester: %+v", decoded)
	}
}

func TestRelationshipEventsNotifyGroupManagersOfJoinRequest(t *testing.T) {
	gormDB, mock := setupMockDB(t)
	managerQuery := `SELECT "user_id" FROM "group_members" WHERE group_id = \$1 AND role IN \(\$2,\$3\) ORDER BY user_id`
	mock.ExpectQuery(managerQuery).WithArgs(int64(50), db.GroupRoleOwner, db.GroupRoleAdmin).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(10)).AddRow(int64(11)))
	mock.ExpectQuery(managerQuery).WithArgs(int64(50), db.GroupRoleOwner, db.GroupRoleAdmin).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(10)).AddRow(int64(11)))

	req := &friend.RequestMessage{FromKafkaTopic: "df-topic-1"}
	request := &friend.RelationshipRequestInfo{RequestId: 9, RequestType: db.RequestTypeGroupJoin, RequesterUserId: 2001, GroupId: 50,
		Status: db.RequestStatusPending, CreatedAt: "2026-10-23T00:00:00.000000000Z"}
	events, err := relationshipEventOutbox(gormDB, "topic/0/4", req, relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "create_group_join_request", request))
	if err != nil {
		t.Fatalf("build join created events: %v", err)
	}
	decoded := decodeRelationshipEvents(t, events)
	if len(decoded) != 2 || decoded[10] == nil || decoded[11] == nil {
		t.Fatalf("join request should notify owner and admins: %+v", decoded)
	}

	resolved := proto.Clone(request).(*friend.RelationshipRequestInfo)
	resolved.Status, resolved.ResolvedBy, resolved.ResolvedAt = db.RequestStatusRejected, 11, "2026-10-23T01:00:00.000000000Z"
	events, err = relationshipEventOutbox(gormDB, "topic/0/5", req, relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "resolve_group_join_request", resolved))
	if err != nil {
		t.Fatalf("build join resolved events: %v", err)
	}
	decoded = decodeRelationshipEvents(t, events)
	if len(decoded) != 2 || decoded[2001].GetOperation() != "resolved" || decoded[10] == nil || decoded[11] != nil {
		t.Fatalf("join resolution should notify requester and other managers only: %+v", decoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRelationshipEventsSkipUnchangedOrFailedOperations(t *testing.T) {
	req := &friend.RequestMessage{FromKafkaTopic: "df-topic-1"}
	request := &friend.RelationshipRequestInfo{RequestId: 7, RequestType: db.RequestTypeFriend, RequesterUserId: 1001, TargetUserId: 1002, Status: db.RequestStatusPending}
	for name, resp := range map[string]*friend.ResponseMessage{
		"duplicate create": relationshipOperationResponse(friend.FriendResult_REQUEST_PENDING, "create_friend_request", request),
		"expired":          relationshipOperationResponse(friend.FriendResult_REQUEST_EXPIRED, "resolve_friend_request", nil),
		"list":             relationshipList(req, friend.FriendResult_FRIEND_OK, nil),
		"other operation":  relationshipOperationResponse(friend.FriendResult_FRIEND_OK, "unknown", request),
	} {
		events, err := relationshipEventOutbox(nil, "topic/0/6", req, resp)
		if err != nil || len(events) != 0 {
			t.Fatalf("%s: events=%d err=%v, want none", name, len(events), err)
		}
	}
}
