package push

import (
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"testing"
	"time"
)

func TestDiscussionPushPostgresEligibilityAndLateMembership(t *testing.T) {
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("push_discussion_%d", time.Now().UnixNano())
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
	for id := int64(1); id <= 3; id++ {
		if err := database.Create(&db.User{ID: id, Account: fmt.Sprintf("u%d", id)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.CreateChannelWithDB(database, 1, 9001, "private channel", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateGroupWithOwnerWithDB(database, 1, 9002, "discussion"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDiscussionGroupWithDB(database, 1, 9001, 9002); err != nil {
		t.Fatal(err)
	}
	for _, member := range []db.GroupMember{{GroupID: 9001, UserID: 2, Role: "member"}, {GroupID: 9002, UserID: 2, Role: "member"}, {GroupID: 9002, UserID: 3, Role: "member"}} {
		if err := database.Create(&member).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{2, 3} {
		if err := database.Create(&db.PushDeviceToken{UserID: id, DeviceID: "phone", Token: fmt.Sprintf("token%d", id), Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var root, comment *db.Message
	if err := database.Transaction(func(tx *gorm.DB) error {
		post, _, err := db.StoreNewMessageWithDB(tx, 1, 9001, "announcement", "text", "", true, "post", "", 0)
		if err != nil {
			return err
		}
		root, err = db.CreateDiscussionRootWithDB(tx, post)
		if err != nil {
			return err
		}
		comment, _, err = db.StoreNewMessageWithDB(tx, 1, 9002, "comment", "text", "", true, "comment", "", 0, root.MessageID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := &GormStore{db: database}
	r := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{TargetUserIds: []int64{2, 3}, SenderUserId: 1, ConversationId: 9002, IsGroup: true, MessageType: "text", MessageId: comment.MessageID, SentAt: comment.Timestamp, Preview: "comment"}}}
	if err := store.EnqueueRequest(context.Background(), "comment/0/1", r, "bundle"); err != nil {
		t.Fatal(err)
	}
	var rows []db.PushMessageDelivery
	database.Find(&rows)
	if len(rows) != 1 {
		t.Fatal("private channel leaked to group-only token", len(rows))
	}
	if r.GetMessagePush().GetDiscussionRootMessageId() != root.MessageID {
		t.Fatal("canonical root dropped")
	}
	if err := database.Where("group_id = ? AND user_id = ?", 9001, 2).Delete(&db.GroupMember{}).Error; err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimMessageDeliveryBatch(context.Background(), 16, time.Now(), time.Minute, 10)
	if err != nil || len(claims) != 1 || !claims[0].RecipientExcluded {
		t.Fatal("worker trusted stale membership", claims, err)
	}
	// Direct root requests are suppressed, even if a stale producer emits one.
	r.GetMessagePush().MessageId = root.MessageID
	if err := store.EnqueueRequest(context.Background(), "root/0/1", r, "bundle"); err != nil {
		t.Fatal(err)
	}
	var count int64
	database.Model(&db.PushJob{}).Count(&count)
	if count != 1 {
		t.Fatal("root created duplicate APNs job", count)
	}
}

func TestDiscussionWorkerPreservesNotificationRouting(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, &memorySender{}, "bundle")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{SenderUserId: 1, ConversationId: 9002, IsGroup: true, MessageType: "text", MessageId: 102, DiscussionRootMessageId: 101, Preview: "comment", SentAt: time.Now().UTC().Format(time.RFC3339)}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	claim := DurableDeliveryClaim{MessageID: 102, JobID: "comment-job", RequestPayload: payload, Token: db.PushDeviceToken{Token: "valid", IsActive: true}}
	prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, []DurableDeliveryClaim{claim})
	if len(prepared) != 1 || prepared[0].notification.DiscussionRootMessageID != 101 {
		t.Fatal(prepared)
	}
}
