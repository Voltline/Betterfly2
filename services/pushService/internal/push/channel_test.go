package push

import (
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
	"time"
)

func TestChannelNotificationUsesChannelIdentityWithoutAuthorPrefix(t *testing.T) {
	store := &memoryStore{presentation: MessagePresentation{IsChannel: true, Title: "公告频道", SenderName: "发布者", Avatar: "channel-avatar", AvatarIsGroup: true, ConversationName: "公告频道", ConversationAvatar: "channel-avatar"}}
	service := &Service{store: store, now: time.Now}
	payload, err := proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{MessageId: 1, SenderUserId: 2, ConversationId: 9, IsGroup: true, MessageType: "text", Preview: "公告内容", SentAt: time.Now().UTC().Format(time.RFC3339)}}})
	if err != nil {
		t.Fatal(err)
	}
	claims := []DurableDeliveryClaim{{MessageID: 1, JobID: "job", RequestPayload: payload, Token: db.PushDeviceToken{Token: "valid", IsActive: true}}, {MessageID: 1, JobID: "job", RequestPayload: payload, RecipientExcluded: true, Token: db.PushDeviceToken{Token: "departed", IsActive: true}}}
	prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, claims)
	if prepared[0].prepareErr != nil || prepared[0].notification.Title != "公告频道" || prepared[0].notification.Body != "公告内容" || prepared[0].notification.Avatar != "channel-avatar" {
		t.Fatalf("wrong channel notification: %+v", prepared[0])
	}
	if prepared[1].prepareErr == nil || prepared[1].prepareTransient || prepared[1].notification.Token != "" {
		t.Fatal("unsubscribed recipient would receive push")
	}
	if !strings.Contains(claimMessageDeliverySQL, "member.user_id IS NULL") || !strings.Contains(claimMessageDeliverySQL, "active_group.is_delete = FALSE") {
		t.Fatal("durable claim missing current subscription check")
	}
}

func TestChannelPresentationReadsSettingsAndPreservesGroupAvatar(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectQuery(`SELECT "id","name","avatar" FROM "users"`).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "avatar"}).AddRow(2, "author", "author-avatar"))
	mock.ExpectQuery(`SELECT "group_id","name","avatar" FROM "groups"`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "name", "avatar"}).AddRow(9, "channel", "channel-avatar"))
	mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(9))
	p, err := store.MessagePresentation(context.Background(), 2, 9, true)
	if err != nil || !p.IsChannel || p.Title != "channel" || p.Avatar != "channel-avatar" || !p.AvatarIsGroup {
		t.Fatalf("presentation %+v %v", p, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestImageCaptionNotificationKeepsChannelGroupAndDirectIdentity(t *testing.T) {
	for _, test := range []struct {
		name           string
		group, channel bool
		title, body    string
	}{
		{"channel", true, true, "公告频道", "[图片] 公告内容"},
		{"group", true, false, "普通群", "发送者：[图片] 公告内容"},
		{"direct", false, false, "发送者", "[图片] 公告内容"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{presentation: MessagePresentation{Title: test.title, IsChannel: test.channel, SenderName: "发送者", Avatar: "avatar-hash", AvatarIsGroup: test.group}}
			service := &Service{store: store, now: time.Now}
			payload, err := proto.Marshal(&pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
				MessageId: 41, SenderUserId: 1, ConversationId: 9, IsGroup: test.group, MessageType: "image", Preview: "[图片] 公告内容", SentAt: "2026-10-01T00:00:00Z",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			prepared := service.prepareDeliveries(context.Background(), deliveryKindMessage, []DurableDeliveryClaim{{MessageID: 41, JobID: "image-job", RequestPayload: payload, Token: db.PushDeviceToken{Token: "valid", IsActive: true}}})
			n := prepared[0].notification
			if prepared[0].prepareErr != nil || n.Title != test.title || n.Body != test.body || n.Avatar != "avatar-hash" || n.IsChannel != test.channel || n.AvatarIsGroup != test.group || n.MessageID != 41 {
				t.Fatal("image notification lost identity or added wrong prefix")
			}
		})
	}
}
