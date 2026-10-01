package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestStoredCaptionCanonicalACKAndDeliveryMapping(t *testing.T) {
	stored := &storage.StoreMsgRsp{MessageId: 41, ClientMessageId: "image-1", Created: true, FromUserId: 1, ToUserId: 9, IsGroup: true, MessageType: "image", Content: "hash", Caption: "# 公告\n配文", ServerTimestamp: "2026-10-01T00:00:00Z"}
	deliveries := 0
	for _, created := range []bool{true, false} {
		stored.Created = created
		err := processStoredPostResponse(stored, func() error { return nil }, func(id int64, post *pb.Post) error {
			deliveries++
			if id != 41 || post.GetCaption() != stored.GetCaption() || post.GetMsg() != stored.GetContent() || post.GetTimestamp() != stored.GetServerTimestamp() {
				t.Fatal("canonical delivery mismatch")
			}
			return nil
		}, func(response *pb.ResponseMessage) error {
			ack := response.GetPostAckRsp()
			if ack.GetMessageId() != 41 || ack.GetTimestamp() != stored.GetServerTimestamp() || ack.GetClientMessageId() != stored.GetClientMessageId() {
				t.Fatal("canonical ACK mismatch")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if deliveries != 1 {
		t.Fatal("duplicate delivery")
	}
	messages := convertStorageMessages([]*storage.MessageRsp{{MessageId: 41, Content: "hash", Caption: stored.GetCaption(), MsgType: "image"}})
	if len(messages) != 1 || messages[0].GetCaption() != stored.GetCaption() || messages[0].GetContent() != "hash" {
		t.Fatal("sync mapping dropped caption")
	}
}

func TestRecalledImageBatchDoesNotReachWebSocket(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previous := db.DB
	db.DB = func(...interface{}) *gorm.DB { return database }
	defer func() { db.DB = previous; _ = sqlDB.Close() }()
	for range 2 {
		mock.ExpectQuery("SELECT \"is_recalled\" FROM \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(true))
		// A nil WebSocket handler would panic if the stale post reached SendMessage.
		if err := (&NewKafkaConsumerGroupHandler{}).deliverGroupPostToUsers(&pb.Post{MessageId: 41, MsgType: "image", Caption: "secret", Msg: "hash"}, []int64{2, 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
