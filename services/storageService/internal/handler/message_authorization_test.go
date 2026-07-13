package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSyncMessagesRejectsIdentityMismatchBeforeDatabaseQuery(t *testing.T) {
	useMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache()}
	for _, test := range []struct {
		name      string
		requester int64
		queryUser int64
	}{
		{name: "missing requester", requester: 0, queryUser: 0},
		{name: "mismatched requester", requester: 1001, queryUser: 2002},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp, err := handler.handleQuerySyncMessages(
				&storage.RequestMessage{TargetUserId: test.requester},
				&storage.QuerySyncMessages{ToUserId: test.queryUser, Timestamp: "2026-07-13T00:00:00Z"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetResult() != storage.StorageResult_FORBIDDEN {
				t.Fatalf("identity validation result = %s, want FORBIDDEN", resp.GetResult())
			}
		})
	}
}

func TestDirectMessageAuthorizationFromCache(t *testing.T) {
	message := &db.Message{MessageID: 41, FromUserID: 1001, ToUserID: 1002, IsGroup: false}
	for _, test := range []struct {
		name      string
		requester int64
		want      storage.StorageResult
	}{
		{name: "sender", requester: 1001, want: storage.StorageResult_OK},
		{name: "receiver", requester: 1002, want: storage.StorageResult_OK},
		{name: "unrelated", requester: 1003, want: storage.StorageResult_RECORD_NOT_EXIST},
	} {
		t.Run(test.name, func(t *testing.T) {
			l1 := newMockCache()
			l1.Set("message:41", message, 0)
			handler := &StorageHandler{l1Cache: l1}
			resp, err := handler.handleQueryMessage(
				&storage.RequestMessage{TargetUserId: test.requester},
				&storage.QueryMessage{MessageId: 41},
			)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetResult() != test.want {
				t.Fatalf("result = %s, want %s", resp.GetResult(), test.want)
			}
		})
	}
}

func TestGroupMessageSenderCanReadWithoutMembership(t *testing.T) {
	message := &db.Message{MessageID: 42, FromUserID: 1001, ToUserID: 9001, Timestamp: "2026-07-13T01:00:00Z", IsGroup: true}
	l1 := newMockCache()
	l1.Set("message:42", message, 0)
	resp, err := (&StorageHandler{l1Cache: l1}).handleQueryMessage(
		&storage.RequestMessage{TargetUserId: 1001},
		&storage.QueryMessage{MessageId: 42},
	)
	if err != nil || resp.GetResult() != storage.StorageResult_OK {
		t.Fatalf("group sender query failed: response=%+v err=%v", resp, err)
	}
}

func TestGroupMessageAuthorizationUsesCurrentMembershipAndJoinedAt(t *testing.T) {
	tests := []struct {
		name        string
		memberCount int64
		want        storage.StorageResult
	}{
		{name: "current member after join", memberCount: 1, want: storage.StorageResult_OK},
		{name: "non member", memberCount: 0, want: storage.StorageResult_RECORD_NOT_EXIST},
		{name: "departed member", memberCount: 0, want: storage.StorageResult_RECORD_NOT_EXIST},
		{name: "message before join", memberCount: 0, want: storage.StorageResult_RECORD_NOT_EXIST},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mock := useMockDB(t)
			mock.ExpectQuery(`SELECT count\(\*\) FROM "group_members" WHERE group_id = \$1 AND user_id = \$2 AND COALESCE\(NULLIF\(joined_at, ''\), update_time\) <= \$3`).
				WithArgs(int64(9001), int64(1002), "2026-07-13T01:00:00Z").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.memberCount))

			message := &db.Message{MessageID: 43, FromUserID: 1001, ToUserID: 9001, Timestamp: "2026-07-13T01:00:00Z", IsGroup: true}
			l1 := newMockCache()
			l1.Set("message:43", message, 0)
			resp, err := (&StorageHandler{l1Cache: l1}).handleQueryMessage(
				&storage.RequestMessage{TargetUserId: 1002},
				&storage.QueryMessage{MessageId: 43},
			)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetResult() != test.want {
				t.Fatalf("result = %s, want %s", resp.GetResult(), test.want)
			}
		})
	}
}

func TestL2MessageCacheHitStillChecksAuthorization(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "group_members" WHERE group_id = \$1 AND user_id = \$2 AND COALESCE\(NULLIF\(joined_at, ''\), update_time\) <= \$3`).
		WithArgs(int64(9001), int64(1002), "2026-07-13T01:00:00Z").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	l2 := newMockCache()
	l2.Set("message:44", &db.Message{MessageID: 44, FromUserID: 1001, ToUserID: 9001, Timestamp: "2026-07-13T01:00:00Z", IsGroup: true}, 0)
	handler := &StorageHandler{l1Cache: newMockCache(), l2Cache: l2}
	resp, err := handler.handleQueryMessage(
		&storage.RequestMessage{TargetUserId: 1002},
		&storage.QueryMessage{MessageId: 44},
	)
	if err != nil || resp.GetResult() != storage.StorageResult_RECORD_NOT_EXIST {
		t.Fatalf("L2 authorization bypass: response=%+v err=%v", resp, err)
	}
}

func TestMissingAndUnauthorizedMessageAreIndistinguishable(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id = \$1 ORDER BY "messages"\."message_id" LIMIT \$2`).
		WithArgs(int64(404), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}))

	missing, err := (&StorageHandler{l1Cache: newMockCache()}).handleQueryMessage(
		&storage.RequestMessage{TargetUserId: 1003},
		&storage.QueryMessage{MessageId: 404},
	)
	if err != nil {
		t.Fatal(err)
	}

	l1 := newMockCache()
	l1.Set("message:405", &db.Message{MessageID: 405, FromUserID: 1001, ToUserID: 1002}, 0)
	unauthorized, err := (&StorageHandler{l1Cache: l1}).handleQueryMessage(
		&storage.RequestMessage{TargetUserId: 1003},
		&storage.QueryMessage{MessageId: 405},
	)
	if err != nil {
		t.Fatal(err)
	}
	if missing.GetResult() != unauthorized.GetResult() || missing.Payload != nil || unauthorized.Payload != nil {
		t.Fatalf("responses differ: missing=%+v unauthorized=%+v", missing, unauthorized)
	}
}
