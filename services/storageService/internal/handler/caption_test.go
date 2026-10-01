package handler

import (
	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
)

const fixtureCaption = "  # 公告\n**完整配文** 😀\n保留末尾空格  "

func captionRows(recalled bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"message_id", "client_message_id", "from_user_id", "to_user_id", "content", "caption", "message_type", "timestamp", "real_file_name", "is_group", "is_recalled"}).
		AddRow(41, "image-1", 1, 2, "original-image-hash", fixtureCaption, "image", "2026-10-01T00:00:00Z", "", false, recalled)
}

func assertCaptionMessage(t *testing.T, msg *storage.MessageRsp, recalled bool) {
	t.Helper()
	if msg.GetMessageId() != 41 || msg.GetTimestamp() != "2026-10-01T00:00:00Z" || msg.GetIsRecalled() != recalled {
		t.Fatalf("identity lost: %v", msg)
	}
	if recalled {
		if msg.GetContent() != "" || msg.GetCaption() != "" || msg.GetRealFileName() != "" {
			t.Fatal("recalled image exposed content")
		}
	} else if msg.GetContent() != "original-image-hash" || msg.GetCaption() != fixtureCaption {
		t.Fatal("canonical image/caption changed")
	}
}

func TestImageCaptionStoreAndDuplicateUseCanonicalRecord(t *testing.T) {
	for _, recalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "recalled"}[recalled], func(t *testing.T) {
			mock := useMockDB(t)
			h := &StorageHandler{l1Cache: newMockCache()}
			message := &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, Content: "original-image-hash", MessageType: "image", Caption: fixtureCaption, ClientMessageId: "image-1"}
			req := &storage.RequestMessage{TargetUserId: 1}
			mock.ExpectBegin()
			mock.ExpectQuery("INSERT INTO \"messages\"").WithArgs("image-1", int64(1), int64(2), "original-image-hash", fixtureCaption, sqlmock.AnyArg(), "image", "", false, false, "", int64(0)).WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow(41))
			mock.ExpectCommit()
			resp, err := h.handleStoreNewMessageWithDB(h.requestDatabase(), req, message, nil)
			if err != nil || !resp.GetStoreMsgRsp().GetCreated() || resp.GetStoreMsgRsp().GetCaption() != fixtureCaption {
				t.Fatalf("store failed: %v", err)
			}
			message.Content, message.Caption = "retry-image-hash", "different retry caption"
			mock.ExpectBegin()
			mock.ExpectQuery("INSERT INTO \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"message_id"}))
			mock.ExpectCommit()
			mock.ExpectQuery("SELECT \\* FROM \"messages\" WHERE from_user_id").WillReturnRows(captionRows(recalled))
			resp, err = h.handleStoreNewMessageWithDB(h.requestDatabase(), req, message, nil)
			if err != nil {
				t.Fatal(err)
			}
			stored := resp.GetStoreMsgRsp()
			if stored.GetCreated() || stored.GetMessageId() != 41 || stored.GetServerTimestamp() != "2026-10-01T00:00:00Z" {
				t.Fatal("duplicate changed identity or created twice")
			}
			if recalled {
				if stored.GetCaption() != "" || stored.GetContent() != "" || stored.GetRealFileName() != "" {
					t.Fatal("duplicate exposed recalled content")
				}
			} else if stored.GetCaption() != fixtureCaption || stored.GetContent() != "original-image-hash" {
				t.Fatal("retry overwrote canonical content")
			}
		})
	}
}

func TestStorageRejectsInvalidCaptionWithoutDatabaseAccess(t *testing.T) {
	for _, message := range []*storage.StoreNewMessage{
		{MessageType: "text", Caption: "not supported"}, {MessageType: "image", Caption: strings.Repeat("中", 4097)}, {MessageType: "image", Caption: string([]byte{0xff})},
	} {
		mock := useMockDB(t)
		h := &StorageHandler{}
		resp, err := h.handleStoreNewMessageWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: 1}, message, nil)
		if err != nil || resp.GetResult() != storage.StorageResult_INVALID_ARGUMENT {
			t.Fatalf("validation error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImageCaptionQuerySyncAndCachedRecall(t *testing.T) {
	for _, recalled := range []bool{false, true} {
		for _, layer := range []string{"DB", "L1", "L2"} {
			t.Run(layer+map[bool]string{false: "active", true: "recalled"}[recalled], func(t *testing.T) {
				mock := useMockDB(t)
				h := &StorageHandler{l1Cache: newMockCache()}
				if layer != "DB" {
					stale := newMockCache()
					// An old writer lacks caption and has pre-recall state.
					stale.Set("message:41", &db.Message{MessageID: 41, FromUserID: 1, ToUserID: 2, Content: "stale-hash", MessageType: "image"}, 0)
					if layer == "L1" {
						h.l1Cache = stale
					} else {
						h.l2Cache = stale
					}
				}
				mock.ExpectQuery("SELECT \\* FROM \"messages\"").WillReturnRows(captionRows(recalled))
				resp, err := h.handleQueryMessageWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: 2}, &storage.QueryMessage{MessageId: 41})
				if err != nil {
					t.Fatal(err)
				}
				assertCaptionMessage(t, resp.GetMsgRsp(), recalled)
				mock.ExpectQuery("(?s)SELECT \\*.*m.caption.*UNION ALL.*m.caption.*ORDER BY timestamp ASC, message_id ASC").WillReturnRows(captionRows(recalled))
				resp, err = h.handleQuerySyncMessagesWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: 2}, &storage.QuerySyncMessages{ToUserId: 2, Timestamp: "2000-01-01T00:00:00Z"})
				if err != nil || len(resp.GetSyncMsgsRsp().GetMsgs()) != 1 {
					t.Fatalf("sync failed: %v", err)
				}
				assertCaptionMessage(t, resp.GetSyncMsgsRsp().GetMsgs()[0], recalled)
			})
		}
	}
}

func TestCaptionHistoryAndRecallMask(t *testing.T) {
	database, mock := setupMockDB(t)
	h := &StorageHandler{database: database, l1Cache: newMockCache()}
	mock.ExpectQuery("(?s)SELECT groups.group_id.*JOIN groups").WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9, true))
	mock.ExpectQuery("SELECT \\* FROM \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"message_id", "content", "caption", "message_type", "is_recalled", "real_file_name"}).AddRow(41, "original-image-hash", fixtureCaption, "image", false, "").AddRow(40, "secret-image-hash", "secret caption", "image", true, "secret.jpg"))
	req := &storage.RequestMessage{TargetUserId: 2, Payload: &storage.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{ChannelId: 9, PageSize: 50}}}
	resp, err := getStorageRequestRouter().Dispatch(storageRequestContext{handler: h, database: database, request: req}, req.Payload)
	if err != nil {
		t.Fatal(err)
	}
	posts := resp.GetChannelResponse().GetPosts()
	if len(posts) != 2 || posts[0].GetCaption() != fixtureCaption || posts[0].GetContent() != "original-image-hash" || posts[1].GetCaption() != "" || posts[1].GetContent() != "" || posts[1].GetRealFileName() != "" {
		t.Fatal("history changed image/caption or exposed recall")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptionDatabaseFailureRemainsRetryable(t *testing.T) {
	mock := useMockDB(t)
	injected := errors.New("database temporarily unavailable")
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO \"messages\"").WillReturnError(injected)
	mock.ExpectRollback()
	h := &StorageHandler{}
	resp, err := h.handleStoreNewMessageWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: 1}, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "image", Content: "hash", Caption: fixtureCaption, ClientMessageId: "image-1"}, nil)
	if resp != nil || !errors.Is(err, injected) {
		t.Fatalf("database failure not retryable: %v", err)
	}
	unsafe := errors.New("failing row: " + fixtureCaption)
	safe := captionStorageError{cause: unsafe}
	if strings.Contains(safe.Error(), fixtureCaption) || !errors.Is(safe, unsafe) {
		t.Fatal("database error leaked caption or lost retry identity")
	}
}

func TestChannelManagersCanStoreImageCaption(t *testing.T) {
	for _, sender := range []int64{1, 2} {
		mock := useMockDB(t)
		mock.ExpectQuery("(?s)SELECT count\\(\\*\\).*channel_settings.group_id IS NULL OR group_members.role IN").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow(41))
		mock.ExpectCommit()
		h := &StorageHandler{l1Cache: newMockCache()}
		resp, err := h.handleStoreNewMessageWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: sender}, &storage.StoreNewMessage{FromUserId: sender, ToUserId: 9, IsGroup: true, Content: "image-hash", MessageType: "image", Caption: fixtureCaption, ClientMessageId: "image-1"}, nil)
		if err != nil || resp.GetResult() != storage.StorageResult_OK || resp.GetStoreMsgRsp().GetCaption() != fixtureCaption {
			t.Fatal("manager image publication failed")
		}
	}
}

func TestCaptionTransactionErrorSummaryDoesNotExposeText(t *testing.T) {
	mock := useMockDB(t)
	injected := errors.New("transaction failing row: " + fixtureCaption)
	mock.ExpectBegin().WillReturnError(injected)
	raw, err := proto.Marshal(&storage.RequestMessage{FromKafkaTopic: "pod", TargetUserId: 1, Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "image", Content: "hash", Caption: fixtureCaption}}})
	if err != nil {
		t.Fatal(err)
	}
	h := &StorageHandler{}
	err = h.HandleMessage(kafkaconsumer.WithOperationKey(context.Background(), "storage/0/41"), raw)
	if !errors.Is(err, injected) || strings.Contains(kafkaconsumer.SummarizeError(err), fixtureCaption) {
		t.Fatal("transaction error lost identity or leaked caption to consumer logs/DLQ summary")
	}
}
