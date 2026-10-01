package handler

import (
	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func expectOrdinaryGroup(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WithArgs(id, 1).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
}

var dbMessageFixture = db.Message{MessageID: 44, FromUserID: 1001, ToUserID: 9001, IsGroup: true, Timestamp: "2026-01-01T00:00:00Z"}

func TestChannelCacheHitRechecksPrivateVisibility(t *testing.T) {
	for _, layer := range []string{"L1", "L2"} {
		t.Run(layer, func(t *testing.T) {
			mock := useMockDB(t)
			mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WithArgs(int64(9001), 1).WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9001, false))
			mock.ExpectQuery(`(?s)channel_settings.is_public = TRUE OR viewer.user_id IS NOT NULL`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
			cache := newMockCache()
			cache.Set("message:44", &dbMessageFixture, 0)
			h := &StorageHandler{l1Cache: cache}
			if layer == "L2" {
				h = &StorageHandler{l1Cache: newMockCache(), l2Cache: cache}
			}
			resp, err := h.handleQueryMessageWithDB(nil, &storage.RequestMessage{TargetUserId: 1001}, &storage.QueryMessage{MessageId: 44})
			if err != nil || resp.Result != storage.StorageResult_RECORD_NOT_EXIST {
				t.Fatalf("former publisher bypassed private auth on %s: %v %v", layer, resp, err)
			}
		})
	}
}

func TestChannelHistoryBoundsAndTombstones(t *testing.T) {
	database, mock := setupMockDB(t)
	h := &StorageHandler{database: database, l1Cache: newMockCache()}
	mock.ExpectQuery(`(?s)SELECT groups.group_id.*JOIN groups`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9, true))
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE.*message_id <.*ORDER BY message_id DESC LIMIT`).WithArgs(int64(9), int64(20), 3).WillReturnRows(sqlmock.NewRows([]string{"message_id", "content", "real_file_name", "is_recalled"}).AddRow(19, "post", "", false).AddRow(18, "private deleted text", "secret.pdf", true).AddRow(17, "older", "", false))
	req := &storage.RequestMessage{TargetUserId: 2, Payload: &storage.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{RequestId: "history", ChannelId: 9, PageSize: 2, BeforeMessageId: 20}}}
	resp, err := getStorageRequestRouter().Dispatch(storageRequestContext{handler: h, database: database, request: req}, req.Payload)
	if err != nil {
		t.Fatal(err)
	}
	page := resp.GetChannelResponse()
	if page.Result != channel.ChannelResult_CHANNEL_OK || !page.HasMore || len(page.Posts) != 2 || page.NextBeforeMessageId != 18 || page.Posts[1].Content != "" || page.Posts[1].RealFileName != "" {
		t.Fatalf("incorrect history: %v", page)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelHistoryDatabaseFailureReturnsError(t *testing.T) {
	database, mock := setupMockDB(t)
	injected := errors.New("database temporarily unavailable")
	mock.ExpectQuery(`(?s)SELECT groups.group_id.*JOIN groups`).WillReturnError(injected)
	h := &StorageHandler{database: database}
	req := &storage.RequestMessage{TargetUserId: 2, Payload: &storage.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{ChannelId: 9}}}
	resp, err := getStorageRequestRouter().Dispatch(storageRequestContext{handler: h, database: database, request: req}, req.Payload)
	if !errors.Is(err, injected) || resp != nil {
		t.Fatalf("database failure cached: %v %v", resp, err)
	}
}

func TestChannelPublicationStorageGateCannotBeBypassedByOldForwarder(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		database, mock := setupMockDB(t)
		if !mismatch {
			mock.ExpectQuery(`(?s)SELECT count\(\*\).*channel_settings.group_id IS NULL OR group_members.role IN`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		}
		sender := int64(2)
		if mismatch {
			sender = 3
		}
		resp, err := (&StorageHandler{database: database}).handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: 2}, &storage.StoreNewMessage{FromUserId: sender, ToUserId: 9, IsGroup: true, Content: "image-hash", MessageType: "image", Caption: fixtureCaption}, nil)
		if err != nil || resp.Result != storage.StorageResult_FORBIDDEN {
			t.Fatalf("publication bypass: %v %v", resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
