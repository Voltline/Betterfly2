package handler

import (
	channel "Betterfly2/proto/channel"
	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"context"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestDiscussionRoutesValidateBeforeDatabase(t *testing.T) {
	database, mock := setupMockDB(t)
	for _, payload := range []any{
		&storage.RequestMessage_GetDiscussion{GetDiscussion: &channel.GetDiscussion{ChannelId: 0, PostMessageId: 1}},
		&storage.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: 1, PageSize: -1}},
	} {
		r := &storage.RequestMessage{TargetUserId: 2}
		switch p := payload.(type) {
		case *storage.RequestMessage_GetDiscussion:
			r.Payload = p
		case *storage.RequestMessage_QueryDiscussionReplies:
			r.Payload = p
		}
		resp, err := getStorageRequestRouter().Dispatch(storageRequestContext{database: database, request: r}, r.Payload)
		if err != nil || resp.GetChannelResponse().GetResult() != channel.ChannelResult_CHANNEL_INVALID_ARGUMENT {
			t.Fatal(resp, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionQueryCacheAuthorizesEveryHit(t *testing.T) {
	for _, layer := range []string{"L1", "L2"} {
		t.Run(layer, func(t *testing.T) {
			database, mock := setupMockDB(t)
			cache := newMockCache()
			cache.Set("message:102", &db.Message{MessageID: 102, FromUserID: 2, ToUserID: 9002, IsGroup: true, Content: "comment", MessageType: "text", DiscussionRootMessageID: 101}, 0)
			h := &StorageHandler{database: database, l1Cache: cache}
			if layer == "L2" {
				h.l1Cache = newMockCache()
				h.l2Cache = cache
			}
			for _, count := range []int{1, 0} {
				mock.ExpectQuery(`SELECT "is_recalled".*"discussion_root_message_id"`).WillReturnRows(sqlmock.NewRows([]string{"discussion_root_message_id"}).AddRow(101))
				mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
				mock.ExpectQuery(`(?s)SELECT count\(\*\).*origin.is_public`).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
				resp, err := h.handleQueryMessageWithDB(database, &storage.RequestMessage{TargetUserId: 2}, &storage.QueryMessage{MessageId: 102})
				if err != nil {
					t.Fatal(err)
				}
				if count == 1 {
					if resp.GetMsgRsp().GetDiscussionRootMessageId() != 101 {
						t.Fatal(resp)
					}
				} else if resp.GetResult() != storage.StorageResult_RECORD_NOT_EXIST {
					t.Fatal("departed author cached authorization", resp)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDiscussionStoragePostgresPublicationAndPagination(t *testing.T) {
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("storage_discussion_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := database.DB(); sql.Close() })
	if err := db.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	for _, user := range []db.User{{ID: 1, Account: "owner"}, {ID: 2, Account: "reader"}, {ID: 3, Account: "outsider"}} {
		if err := database.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateChannelWithDB(database, 1, 9001, "channel", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateGroupWithOwnerWithDB(database, 1, 9002, "discussion"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDiscussionGroupWithDB(database, 1, 9001, 9002); err != nil {
		t.Fatal(err)
	}
	h := &StorageHandler{database: database, l1Cache: newMockCache()}
	postReq := &storage.RequestMessage{FromKafkaTopic: "df", TargetUserId: 1, Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: &storage.StoreNewMessage{FromUserId: 1, ToUserId: 9001, IsGroup: true, Content: "image-hash", Caption: "# Announcement\ncaption", MessageType: "image", ClientMessageId: "post"}}}
	raw, _ := proto.Marshal(postReq)
	for range 2 {
		if err := h.HandleMessage(kafkaconsumer.WithOperationKey(context.Background(), "post/0/1"), raw); err != nil {
			t.Fatal(err)
		}
	}
	var events []db.OutboxEvent
	if err := database.Order("event_id").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatal("post and root not atomically persisted", len(events))
	}
	var source, root *storage.StoreMsgRsp
	for _, event := range events {
		env := &envelope.Envelope{}
		if err := proto.Unmarshal(event.Payload, env); err != nil {
			t.Fatal(err)
		}
		resp := &storage.ResponseMessage{}
		if err := proto.Unmarshal(env.Payload, resp); err != nil {
			t.Fatal(err)
		}
		if resp.GetStoreMsgRsp().GetSourceChannelMessageId() > 0 {
			root = resp.GetStoreMsgRsp()
		} else {
			source = resp.GetStoreMsgRsp()
		}
	}
	if source == nil || root == nil || source.GetDiscussionRootMessageId() != root.GetMessageId() || root.GetContent() != "" || root.GetCaption() != "" {
		t.Fatal(source, root)
	}
	query := func(actor int64, payload any) *channel.ChannelResponse {
		t.Helper()
		r := &storage.RequestMessage{TargetUserId: actor}
		switch p := payload.(type) {
		case *storage.RequestMessage_GetDiscussion:
			r.Payload = p
		case *storage.RequestMessage_QueryDiscussionReplies:
			r.Payload = p
		}
		resp, err := getStorageRequestRouter().Dispatch(storageRequestContext{handler: h, database: database, request: r}, r.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return resp.GetChannelResponse()
	}
	get := &storage.RequestMessage_GetDiscussion{GetDiscussion: &channel.GetDiscussion{RequestId: "get", ChannelId: 9001, PostMessageId: source.GetMessageId()}}
	info := query(2, get)
	if info.GetResult() != channel.ChannelResult_CHANNEL_OK || info.GetDiscussion().GetJoined() || info.GetDiscussion().GetCanComment() || info.GetDiscussion().GetPost().GetCaption() != source.GetCaption() {
		t.Fatal(info)
	}
	if r := query(2, &storage.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: root.GetMessageId()}}); r.GetResult() != channel.ChannelResult_CHANNEL_FORBIDDEN {
		t.Fatal(r)
	}
	if err := database.Create(&db.GroupMember{GroupID: 9002, UserID: 2, Role: "member", JoinedAt: "2100-01-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	var comments []int64
	for i := 0; i < 3; i++ {
		r := &storage.RequestMessage{FromKafkaTopic: "df", TargetUserId: 2, Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: &storage.StoreNewMessage{FromUserId: 2, ToUserId: 9002, IsGroup: true, Content: fmt.Sprintf("comment-%d", i), MessageType: "text", ClientMessageId: fmt.Sprintf("comment-%d", i), DiscussionRootMessageId: root.GetMessageId(), ReplyToMessageId: root.GetMessageId()}}}
		raw, _ := proto.Marshal(r)
		if err := h.HandleMessage(kafkaconsumer.WithOperationKey(context.Background(), fmt.Sprintf("comment/0/%d", i)), raw); err != nil {
			t.Fatal(err)
		}
		var m db.Message
		database.First(&m, "client_message_id = ?", fmt.Sprintf("comment-%d", i))
		comments = append(comments, m.MessageID)
	}
	page := query(2, &storage.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: root.GetMessageId(), PageSize: 2}})
	if !page.HasMore || len(page.Posts) != 2 || page.Posts[0].MessageId != comments[2] || page.Discussion.ReplyCount != 3 {
		t.Fatal(page)
	}
	next := query(2, &storage.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: root.GetMessageId(), PageSize: 2, BeforeMessageId: page.NextBeforeMessageId}})
	if next.HasMore || len(next.Posts) != 1 || next.Posts[0].MessageId != comments[0] {
		t.Fatal(next)
	}
	if _, err := db.SetDiscussionGroupWithDB(database, 1, 9001, 0); err != nil {
		t.Fatal(err)
	}
	if info := query(2, get); !info.Discussion.Closed || info.Discussion.CanComment {
		t.Fatal("unlinked thread writable", info)
	}
	if page := query(2, &storage.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: root.GetMessageId()}}); len(page.Posts) != 3 {
		t.Fatal("unlink deleted comments", page)
	}
	// A card may already have been moderated by a different group manager.
	if err := database.Create(&db.GroupMember{GroupID: 9002, UserID: 3, Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	var recallResponse *storage.ResponseMessage
	if err := database.Transaction(func(tx *gorm.DB) error {
		if _, err := db.RecallMessageWithDB(tx, 3, root.GetMessageId(), time.Now()); err != nil {
			return err
		}
		req := &storage.RequestMessage{TargetUserId: 1}
		var err error
		recallResponse, err = h.handleRecallMessageWithDB(tx, req, &storage.RecallMessage{MessageId: source.GetMessageId()}, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if recallResponse.GetRecallMessageRsp().GetDiscussionRoot().GetOperatorUserId() != 3 {
		t.Fatal("cascade overwrote canonical root recall operator", recallResponse)
	}
}
