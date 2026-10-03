package push

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
)

func TestGroupNotificationPreferenceReadAndDatabaseFailure(t *testing.T) {
	store, mock := newStoreMock(t)
	for _, count := range []int64{0, 1} {
		mock.ExpectQuery(`(?s)SELECT count\(\*\).*groups.is_delete = FALSE.*group_members.notifications_muted = FALSE`).WithArgs(int64(9), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
		enabled, err := store.MessageNotificationsEnabled(context.Background(), 2, 9, true)
		if err != nil || enabled != (count == 1) {
			t.Fatalf("preference: %t %v", enabled, err)
		}
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT count\(\*\)`).WillReturnError(injected)
	if enabled, err := store.MessageNotificationsEnabled(context.Background(), 2, 9, true); enabled || !errors.Is(err, injected) {
		t.Fatalf("preference failed open: %t %v", enabled, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMutedQueuedMessageIsNotSentButRecallAndVoIPAreUnaffected(t *testing.T) {
	sender := &memorySender{}
	service := NewService(&memoryStore{}, sender, "com.Voltline.Betterfly2")
	now := time.Now().UTC()
	message, _ := proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{MessageId: 45, SenderUserId: 1, ConversationId: 9, IsGroup: true, MessageType: "text", Preview: "reply", SentAt: now.Format(time.RFC3339)}}})
	claim := DurableDeliveryClaim{MessageID: 45, JobID: "muted-job", Token: db.PushDeviceToken{ID: 1, UserID: 2, Token: "valid", IsActive: true}, RequestPayload: message, RecipientExcluded: true, ClaimToken: "claim", Attempt: 1}
	prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, []DurableDeliveryClaim{claim})
	update := service.sendDurableDelivery(context.Background(), prepared[0])
	if update.Status != DeliveryPermanent || update.DeactivateToken || len(sender.sent) != 0 {
		t.Fatal("muted message was sent or token deactivated")
	}
	claim.MessageID = -45
	claim.RecipientExcluded = false
	claim.RequestPayload, _ = proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessageRecall{MessageRecall: &pushpb.MessageRecallPushRequest{MessageId: 45, ConversationId: 9, IsGroup: true, RecalledAt: now.Format(time.RFC3339)}}})
	prepared = service.prepareDeliveries(context.Background(), deliveryKindMessage, []DurableDeliveryClaim{claim})
	if prepared[0].prepareErr != nil || prepared[0].notification.Kind != NotificationRecall {
		t.Fatal("mute suppressed recall replacement")
	}
	claim.RequestPayload, _ = proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{CallId: "call", CallerUserId: 1, CalleeUserId: 2, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}}})
	prepared = service.prepareDeliveries(context.Background(), deliveryKindVoIP, []DurableDeliveryClaim{claim})
	if prepared[0].prepareErr != nil || prepared[0].notification.Kind != NotificationVoIP {
		t.Fatal("mute suppressed VoIP")
	}
	if !strings.Contains(messageFanoutSQL, "member.notifications_muted = FALSE") || !strings.Contains(claimMessageDeliverySQL, "updated.message_id > 0 AND member.notifications_muted = TRUE") {
		t.Fatal("enqueue or claim forgot preference gate")
	}
}

func TestGroupVoIPDurableWorkerPreservesInvitationMetadata(t *testing.T) {
	service := NewService(&memoryStore{}, &memorySender{}, "com.Voltline.Betterfly2")
	payload, _ := proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{CallId: "group-call", CallerUserId: 1, CalleeUserId: 2, CallType: "video", GroupId: 10, GroupName: "群会议", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)}}})
	prepared := service.prepareDeliveries(context.Background(), deliveryKindVoIP, []DurableDeliveryClaim{{Token: db.PushDeviceToken{ID: 1, UserID: 2, Token: "device-token", IsActive: true}, RequestPayload: payload}})
	if len(prepared) != 1 || prepared[0].prepareErr != nil || !prepared[0].notification.IsGroup || prepared[0].notification.ConversationID != 10 || prepared[0].notification.GroupName != "群会议" {
		t.Fatal("group invitation lost in durable worker")
	}
}
