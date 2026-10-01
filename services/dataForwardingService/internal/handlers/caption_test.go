package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"
	"data_forwarding_service/internal/publisher"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/IBM/sarama/mocks"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gologger "gorm.io/gorm/logger"
)

func captionDeliveryDatabase(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: gologger.Default.LogMode(gologger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := db.DB
	db.DB = func(...interface{}) *gorm.DB { return database }
	t.Cleanup(func() {
		db.DB = previous
		_ = sqlDB.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return mock
}

func TestCaptionValidationAndStorageBridge(t *testing.T) {
	post := &pb.Post{FromId: 1, ToId: 9, IsGroup: true, MsgType: "image", Msg: "image-hash", Caption: " # 公告\n**原文** 😀  ", ClientMessageId: "image-1"}
	if err := validatePostPayload(post); err != nil {
		t.Fatal(err)
	}
	stored := buildStoreNewMessageStorageRequest(post, "pod").GetStoreNewMessage()
	if stored.GetCaption() != post.GetCaption() || stored.GetContent() != post.GetMsg() || stored.GetClientMessageId() != post.GetClientMessageId() {
		t.Fatal("storage bridge changed caption")
	}
	for _, invalid := range []*pb.Post{{MsgType: "text", Caption: "no"}, {MsgType: "image", Caption: strings.Repeat("中", 4097)}, {MsgType: "image", Caption: string([]byte{0xff})}} {
		if err := validatePostPayload(invalid); !errors.Is(err, utils.ErrInvalidCaption) {
			t.Fatal("invalid caption accepted")
		}
	}
}

func TestCaptionCrossPodAndLocalWirePreserveRawText(t *testing.T) {
	post := &pb.Post{FromId: 1, ToId: 9, IsGroup: true, MsgType: "image", Msg: "hash", Caption: "# 标题\n**说明**", MessageId: 41}
	request := &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}
	for _, topic := range []string{"local", "remote"} {
		raw, err := buildPostDeliveryMessageBytes(topic, "local", post, request)
		if err != nil {
			t.Fatal(err)
		}
		var decoded *pb.Post
		if topic == "local" {
			response := &pb.ResponseMessage{}
			if err := proto.Unmarshal(raw, response); err != nil {
				t.Fatal(err)
			}
			decoded = response.GetPost()
		} else {
			response := &pb.RequestMessage{}
			if err := proto.Unmarshal(raw, response); err != nil {
				t.Fatal(err)
			}
			decoded = response.GetPost()
		}
		if !proto.Equal(post, decoded) {
			t.Fatal("cross-Pod image changed")
		}
	}
	raw, err := buildGroupPostDeliveryEnvelopeBytes([]int64{2, 3}, post)
	if err != nil || len(raw) == 0 {
		t.Fatal("batch envelope failed")
	}
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(raw, env); err != nil {
		t.Fatal(err)
	}
	batch := &pb.DFInternalDelivery{}
	if err := proto.Unmarshal(env.GetPayload(), batch); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(post, batch.GetGroupPostBatchDelivery().GetPost()) {
		t.Fatal("Kafka batch dropped caption")
	}
}

func TestImageCaptionPushSummary(t *testing.T) {
	tests := []struct{ caption, want string }{
		{"", "[图片]"}, {"# 公告\n**内容**\n[详情](https://example.test)", "[图片] 公告 内容 详情"}, {" \n\t ", "[图片]"},
	}
	for _, test := range tests {
		post := &pb.Post{MsgType: "image", Msg: "secret-image-hash", Caption: test.caption}
		if got := messagePushPreview(post); got != test.want {
			t.Fatalf("unexpected summary")
		}
	}
	preview := messagePushPreview(&pb.Post{MsgType: "image", Msg: "secret-image-hash", Caption: strings.Repeat("😀", 4096)})
	if len([]rune(preview)) != 181 || !strings.HasSuffix(preview, "…") || strings.Contains(preview, "secret-image-hash") {
		t.Fatal("preview not safely truncated")
	}
}

func TestRecalledImageSuppressesEffectsAndCrossPodReplay(t *testing.T) {
	mock := captionDeliveryDatabase(t)
	post := &pb.Post{MessageId: 41, FromId: 1, ToId: 2, MsgType: "image", Msg: "hash", Caption: "secret"}
	request := &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}
	for range 2 {
		mock.ExpectQuery("SELECT \"is_recalled\" FROM \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(true))
		if err := DeliverStoredPost(41, post); err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery("SELECT \"is_recalled\" FROM \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(true))
		if err := InplaceHandlePostMessage(request); err != nil {
			t.Fatal(err)
		}
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery("SELECT \"is_recalled\" FROM \"messages\"").WillReturnError(injected)
	if err := DeliverStoredPost(41, post); !errors.Is(err, injected) {
		t.Fatal("database fault allowed delivery")
	}
}

func TestImageEffectsAreIdempotent(t *testing.T) {
	server := postTestRedis(t)
	mock := captionDeliveryDatabase(t)
	producer := mocks.NewSyncProducer(t, nil)
	previous := publisher.KafkaProducer
	publisher.KafkaProducer = producer
	t.Cleanup(func() { publisher.KafkaProducer = previous; _ = producer.Close() })
	post := &pb.Post{FromId: 1, ToId: 2, MsgType: "image", Msg: "hash", Caption: "canonical"}
	producer.ExpectSendMessageAndSucceed() // Exactly one APNs Kafka request.
	for range 2 {
		mock.ExpectQuery("SELECT \"is_recalled\" FROM \"messages\"").WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(false))
		if err := DeliverStoredPost(41, post); err != nil {
			t.Fatal(err)
		}
	}
	if value, err := server.Get("post:effects:41"); err != nil || value != "1" {
		t.Fatal("image effects not completed")
	}
}
