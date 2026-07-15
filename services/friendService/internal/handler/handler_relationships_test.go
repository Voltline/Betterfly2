package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDecisionStatus(t *testing.T) {
	tests := []struct {
		decision friend.RequestDecision
		want     string
		valid    bool
	}{
		{friend.RequestDecision_REQUEST_ACCEPT, db.RequestStatusAccepted, true},
		{friend.RequestDecision_REQUEST_REJECT, db.RequestStatusRejected, true},
		{friend.RequestDecision_REQUEST_CANCEL, db.RequestStatusCancelled, true},
		{friend.RequestDecision_REQUEST_DECISION_UNSPECIFIED, "", false},
	}
	for _, test := range tests {
		got, valid := decisionStatus(test.decision)
		if got != test.want || valid != test.valid {
			t.Fatalf("decisionStatus(%s) = (%q, %t), want (%q, %t)", test.decision, got, valid, test.want, test.valid)
		}
	}
}

func TestAcceptFriendRequestCreatesBothRelationsAtomically(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "relationship_requests" WHERE "relationship_requests"\."id" = \$1 ORDER BY "relationship_requests"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(int64(77), 1).
		WillReturnRows(relationshipRequestRows().AddRow(77, "friend", 1001, 1002, 0, "hello", "pending", "friend:1001:1002", "2026-07-13T00:00:00Z", "2099-07-20T00:00:00Z", "", 0))
	mock.ExpectQuery(`SELECT \* FROM "friends" WHERE user_id = \$1 AND friend_id = \$2 ORDER BY "friends"\."user_id" LIMIT \$3`).
		WithArgs(int64(1001), int64(1002), 1).WillReturnRows(friendRows())
	mock.ExpectExec(`INSERT INTO "friends"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "friends" WHERE user_id = \$1 AND friend_id = \$2 ORDER BY "friends"\."user_id" LIMIT \$3`).
		WithArgs(int64(1002), int64(1001), 1).WillReturnRows(friendRows())
	mock.ExpectExec(`INSERT INTO "friends"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "relationship_requests" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT relationship_requests\.\*, requester\.name AS requester_name`).
		WithArgs(int64(77)).
		WillReturnRows(relationshipRequestViewRows().AddRow(77, "friend", 1001, 1002, 0, "hello", "accepted", nil, "2026-07-13T00:00:00Z", "2099-07-20T00:00:00Z", "2026-07-13T01:00:00Z", 1002, "Alice", "", "Bob", "", "", ""))

	response, err := (&FriendHandler{}).handleResolveFriendRequestWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1002},
		&friend.ResolveFriendRequest{UserId: 1002, RequestId: 77, Decision: friend.RequestDecision_REQUEST_ACCEPT},
	)
	if err != nil {
		t.Fatalf("accept friend request failed: %v", err)
	}
	if response.GetResult() != friend.FriendResult_FRIEND_OK || response.GetRelationshipOperationRsp().GetRequest().GetStatus() != "accepted" {
		t.Fatalf("unexpected acceptance response: %+v", response)
	}
}

func TestAdminAcceptsGroupJoinRequestAtomically(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "relationship_requests"`).WithArgs(int64(88), 1).
		WillReturnRows(relationshipRequestRows().AddRow(88, "group_join", 1003, 0, 3001, "join", "pending", "group_join:3001:1003", "2026-07-13T00:00:00Z", "2099-07-20T00:00:00Z", "", 0))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1002), 1).WillReturnRows(groupMemberRows().AddRow(3001, 1002, "admin", "2026-07-13T00:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "groups" WHERE group_id = \$1 AND is_delete = \$2`).
		WithArgs(int64(3001), false, 1).WillReturnRows(groupRows().AddRow(3001, "Team", "", 1001, false, "2026-07-13T00:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1003), 1).WillReturnRows(groupMemberRows())
	mock.ExpectExec(`INSERT INTO "group_members"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "groups" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "relationship_requests" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT relationship_requests\.\*, requester\.name AS requester_name`).WithArgs(int64(88)).
		WillReturnRows(relationshipRequestViewRows().AddRow(88, "group_join", 1003, 0, 3001, "join", "accepted", nil, "2026-07-13T00:00:00Z", "2099-07-20T00:00:00Z", "2026-07-13T01:00:00Z", 1002, "Applicant", "", "", "", "Team", ""))

	response, err := (&FriendHandler{}).handleResolveGroupJoinRequestWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1002},
		&friend.ResolveGroupJoinRequest{RequestUserId: 1002, RequestId: 88, Decision: friend.RequestDecision_REQUEST_ACCEPT},
	)
	if err != nil || response.GetResult() != friend.FriendResult_FRIEND_OK {
		t.Fatalf("admin acceptance failed: response=%+v err=%v", response, err)
	}
}

func TestAdminCannotKickAnotherAdmin(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1002), 1).WillReturnRows(groupMemberRows().AddRow(3001, 1002, "admin", "2026-07-13T00:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1003), 1).WillReturnRows(groupMemberRows().AddRow(3001, 1003, "admin", "2026-07-13T00:00:00Z"))
	mock.ExpectRollback()

	response, err := (&FriendHandler{}).handleKickGroupMemberWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1002},
		&friend.KickGroupMember{RequestUserId: 1002, GroupId: 3001, UserId: 1003},
	)
	if err != nil || response.GetResult() != friend.FriendResult_FORBIDDEN {
		t.Fatalf("admin was allowed to kick admin: response=%+v err=%v", response, err)
	}
}

func TestExpiredRequestCommitsExpiredStateBeforeReturningError(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "relationship_requests"`).WithArgs(int64(99), 1).
		WillReturnRows(relationshipRequestRows().AddRow(99, "friend", 1001, 1002, 0, "", "pending", "friend:1001:1002", "2020-01-01T00:00:00Z", "2020-01-08T00:00:00Z", "", 0))
	mock.ExpectExec(`UPDATE "relationship_requests" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	response, err := (&FriendHandler{}).handleResolveFriendRequestWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1002},
		&friend.ResolveFriendRequest{UserId: 1002, RequestId: 99, Decision: friend.RequestDecision_REQUEST_ACCEPT},
	)
	if err != nil || response.GetResult() != friend.FriendResult_REQUEST_EXPIRED {
		t.Fatalf("expired request result mismatch: response=%+v err=%v", response, err)
	}
}

func TestResolvedRelationshipRequestCanReplaySameDecision(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "relationship_requests"`).WithArgs(int64(100), 1).
		WillReturnRows(relationshipRequestRows().AddRow(100, "friend", 1001, 1002, 0, "", "accepted", nil, "2026-07-13T00:00:00Z", "2026-07-20T00:00:00Z", "2026-07-13T01:00:00Z", 1002))
	mock.ExpectCommit()
	mock.ExpectQuery(`relationship_requests\.\*, requester\.name`).WithArgs(int64(100)).
		WillReturnRows(relationshipRequestViewRows().AddRow(100, "friend", 1001, 1002, 0, "", "accepted", nil, "2026-07-13T00:00:00Z", "2026-07-20T00:00:00Z", "2026-07-13T01:00:00Z", 1002, "Alice", "", "Bob", "", "", ""))

	response, err := (&FriendHandler{}).handleResolveFriendRequestWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1002},
		&friend.ResolveFriendRequest{UserId: 1002, RequestId: 100, Decision: friend.RequestDecision_REQUEST_ACCEPT},
	)
	if err != nil || response.GetResult() != friend.FriendResult_FRIEND_OK || response.GetRelationshipOperationRsp().GetRequest().GetStatus() != "accepted" {
		t.Fatalf("idempotent relationship replay failed: response=%+v err=%v", response, err)
	}
}

func relationshipRequestRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "request_type", "requester_user_id", "target_user_id", "group_id", "message", "status", "active_key", "created_at", "expires_at", "resolved_at", "resolved_by"})
}

func relationshipRequestViewRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "request_type", "requester_user_id", "target_user_id", "group_id", "message", "status", "active_key", "created_at", "expires_at", "resolved_at", "resolved_by", "requester_name", "requester_avatar", "target_name", "target_avatar", "group_name", "group_avatar"})
}

func friendRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"user_id", "friend_id", "is_notify", "alias", "is_delete", "update_time"})
}

func TestRelationshipResponsePreservesLifecycleFields(t *testing.T) {
	view := &db.RelationshipRequestView{RelationshipRequest: db.RelationshipRequest{
		ID: 42, RequestType: db.RequestTypeFriend, RequesterUserID: 1, TargetUserID: 2,
		Message: "hello", Status: db.RequestStatusPending, CreatedAt: "2026-07-13T00:00:00Z", ExpiresAt: "2026-07-20T00:00:00Z",
	}, RequesterName: "Alice", TargetName: "Bob"}
	response := relationshipOperation(&friend.RequestMessage{TargetUserId: 1}, "create_friend_request", friend.FriendResult_FRIEND_OK, view)
	operation := response.GetRelationshipOperationRsp()
	if operation.GetOperation() != "create_friend_request" || operation.GetRequest().GetRequestId() != 42 {
		t.Fatalf("unexpected relationship operation: %+v", operation)
	}
	request := operation.GetRequest()
	if request.GetRequesterName() != "Alice" || request.GetTargetName() != "Bob" || request.GetExpiresAt() != "2026-07-20T00:00:00Z" {
		t.Fatalf("relationship lifecycle fields were lost: %+v", request)
	}
}

func TestRelationshipErrorsAreStructured(t *testing.T) {
	tests := []struct {
		err  error
		want friend.FriendResult
	}{
		{db.ErrRelationshipForbidden, friend.FriendResult_FORBIDDEN},
		{db.ErrRelationshipExpired, friend.FriendResult_REQUEST_EXPIRED},
		{db.ErrRelationshipInvalidState, friend.FriendResult_INVALID_STATE},
		{db.ErrRelationshipNotFound, friend.FriendResult_RECORD_NOT_EXIST},
		{db.ErrAlreadyRelated, friend.FriendResult_ALREADY_EXIST},
	}
	for _, test := range tests {
		if got := relationshipResult(test.err); got != test.want {
			t.Fatalf("relationshipResult(%v) = %s, want %s", test.err, got, test.want)
		}
	}
}
