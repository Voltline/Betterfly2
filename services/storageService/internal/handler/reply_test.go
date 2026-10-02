package handler

import (
	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReplyStorageRejectsUnauthorizedOrCrossConversation(t *testing.T) {
	database, mock := setupMockDB(t)
	h := &StorageHandler{database: database}
	for _, test := range []struct {
		actor, reply int64
		want         storage.StorageResult
	}{{0, 44, storage.StorageResult_FORBIDDEN}, {2, 44, storage.StorageResult_FORBIDDEN}, {1, -1, storage.StorageResult_INVALID_ARGUMENT}} {
		response, err := h.handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: test.actor}, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "text", Content: "reply", ReplyToMessageId: test.reply}, nil)
		if err != nil || response.GetResult() != test.want {
			t.Fatalf("identity: %v %v", response, err)
		}
	}
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id"}).AddRow(44, 3, 4))
	response, err := h.handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: 1}, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "text", Content: "reply", ReplyToMessageId: 44}, nil)
	if err != nil || response.GetResult() != storage.StorageResult_INVALID_ARGUMENT {
		t.Fatalf("cross conversation: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplyQueryCacheRechecksRecallWithoutMutatingSharedEntity(t *testing.T) {
	for _, layer := range []string{"L1", "L2"} {
		t.Run(layer, func(t *testing.T) {
			database, mock := setupMockDB(t)
			stale := &db.Message{MessageID: 44, FromUserID: 1, ToUserID: 2, Content: "secret", RealFileName: "secret.pdf", MessageType: "file"}
			cache := newMockCache()
			cache.Set("message:44", stale, 0)
			h := &StorageHandler{database: database, l1Cache: cache}
			if layer == "L2" {
				h.l1Cache = newMockCache()
				h.l2Cache = cache
			}
			mock.ExpectQuery(`SELECT "is_recalled","recalled_at","recalled_by","reply_to_message_id" FROM "messages"`).WithArgs(int64(44), 1).WillReturnRows(sqlmock.NewRows([]string{"is_recalled", "recalled_at", "recalled_by", "reply_to_message_id"}).AddRow(true, "2026-10-02T00:00:00Z", 1, 40))
			response, err := h.handleQueryMessageWithDB(database, &storage.RequestMessage{TargetUserId: 2}, &storage.QueryMessage{MessageId: 44})
			msg := response.GetMsgRsp()
			if err != nil || !msg.GetIsRecalled() || msg.GetContent() != "" || msg.GetRealFileName() != "" || msg.GetReplyToMessageId() != 40 {
				t.Fatalf("stale quote exposed: %v %v", response, err)
			}
			if stale.IsRecalled || stale.ReplyToMessageID != 0 || stale.Content != "secret" {
				t.Fatal("shared cache mutated")
			}
			injected := errors.New("database unavailable")
			mock.ExpectQuery(`SELECT "is_recalled"`).WillReturnError(injected)
			if response, err := h.handleQueryMessageWithDB(database, &storage.RequestMessage{TargetUserId: 2}, &storage.QueryMessage{MessageId: 44}); response != nil || !errors.Is(err, injected) {
				t.Fatalf("recall state failure failed open: %v %v", response, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplyCanonicalStoreSyncAndHistoryMapping(t *testing.T) {
	database, mock := setupMockDB(t)
	h := &StorageHandler{database: database, l1Cache: newMockCache()}
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE from_user_id`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id", "content", "reply_to_message_id", "timestamp"}).AddRow(45, 1, 2, "canonical", 44, "2026-10-02T00:00:00Z"))
	response, err := h.handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: 1}, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, Content: "changed", ClientMessageId: "stable", ReplyToMessageId: 999}, nil)
	if err != nil || response.GetStoreMsgRsp().GetReplyToMessageId() != 44 || response.GetStoreMsgRsp().GetCreated() {
		t.Fatalf("canonical reply: %v %v", response, err)
	}
	if msg := h.buildMessageResponse(&storage.RequestMessage{TargetUserId: 2}, &db.Message{MessageID: 45, ReplyToMessageID: 44}).GetMsgRsp(); msg.GetReplyToMessageId() != 44 {
		t.Fatal(msg)
	}
	mock.ExpectQuery(`(?s)SELECT \*.*m.reply_to_message_id.*UNION ALL.*m.reply_to_message_id`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "reply_to_message_id", "timestamp"}).AddRow(45, 44, "2026-10-02T00:00:00Z"))
	response, err = h.handleQuerySyncMessagesWithDB(database, &storage.RequestMessage{TargetUserId: 2}, &storage.QuerySyncMessages{ToUserId: 2, Timestamp: "2000-01-01T00:00:00Z"})
	if err != nil || len(response.GetSyncMsgsRsp().GetMsgs()) != 1 || response.GetSyncMsgsRsp().Msgs[0].GetReplyToMessageId() != 44 {
		t.Fatalf("sync dropped reference: %v %v", response, err)
	}
	mock.ExpectQuery(`(?s)SELECT groups.group_id.*JOIN groups`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9, true))
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "reply_to_message_id", "is_recalled", "content"}).AddRow(45, 44, true, "hidden"))
	req := &storage.RequestMessage{TargetUserId: 2, Payload: &storage.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{ChannelId: 9}}}
	response, err = getStorageRequestRouter().Dispatch(storageRequestContext{handler: h, database: database, request: req}, req.Payload)
	posts := response.GetChannelResponse().GetPosts()
	if err != nil || len(posts) != 1 || posts[0].GetReplyToMessageId() != 44 || posts[0].GetContent() != "" {
		t.Fatalf("history dropped reference: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
