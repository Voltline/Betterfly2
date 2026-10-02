package data_forwarding

import (
	"testing"

	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestConversationFeatureWireTagsAndRoundTrip(t *testing.T) {
	for _, test := range []struct {
		message proto.Message
		field   protoreflect.Name
		tag     protoreflect.FieldNumber
	}{
		{&Post{ReplyToMessageId: 44}, "reply_to_message_id", 11},
		{&MessageRsp{ReplyToMessageId: 44}, "reply_to_message_id", 13},
		{&storage.StoreNewMessage{ReplyToMessageId: 44}, "reply_to_message_id", 10},
		{&storage.StoreMsgRsp{ReplyToMessageId: 44}, "reply_to_message_id", 13},
		{&storage.MessageRsp{ReplyToMessageId: 44}, "reply_to_message_id", 13},
		{&channel.ChannelPost{ReplyToMessageId: 44}, "reply_to_message_id", 11},
		{&channel.ChannelInfo{PinnedMessageId: 44, NotificationsMuted: true}, "pinned_message_id", 12},
		{&channel.ChannelInfo{NotificationsMuted: true}, "notifications_muted", 13},
		{&JoinedGroupInfo{NotificationsMuted: true}, "notifications_muted", 7},
		{&GroupMemberOperationRsp{NotificationsMuted: true}, "notifications_muted", 9},
		{&RequestMessage{Payload: &RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &UpdateGroupNotify{TargetGroupId: 9, IsNotify: false}}}, "update_group_notify", 41},
		{&channel.ChannelRequest{RequestId: "pin-1", Payload: &channel.ChannelRequest_SetPin{SetPin: &channel.SetChannelPin{ChannelId: 9, MessageId: 44}}}, "set_pin", 11},
	} {
		field := test.message.ProtoReflect().Descriptor().Fields().ByName(test.field)
		if field == nil || field.Number() != test.tag {
			t.Fatalf("tag changed: %T %s", test.message, test.field)
		}
		encoded, err := proto.Marshal(test.message)
		if err != nil {
			t.Fatal(err)
		}
		decoded := test.message.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(encoded, decoded); err != nil || !proto.Equal(decoded, test.message) {
			t.Fatalf("roundtrip %T: %v", test.message, err)
		}
	}
}

func TestOldPostBytesRemainUnquotedAndPreferencesDefaultEnabled(t *testing.T) {
	old := []byte{34, 4, 'h', 'a', 's', 'h', 42, 5, 'i', 'm', 'a', 'g', 'e'}
	post := &Post{}
	if err := proto.Unmarshal(old, post); err != nil || post.GetReplyToMessageId() != 0 {
		t.Fatalf("legacy Post: %v", err)
	}
	encoded, err := proto.Marshal(post)
	if err != nil || string(old) != string(encoded) {
		t.Fatal("zero reference changes old wire bytes")
	}
	info := &channel.ChannelInfo{}
	if err := proto.Unmarshal(nil, info); err != nil || info.GetNotificationsMuted() || info.GetPinnedMessageId() != 0 {
		t.Fatal("old channel defaults changed")
	}
	request := &channel.ChannelRequest{}
	if err := proto.Unmarshal([]byte{34, 2, 8, 9}, request); err != nil || request.GetGet().GetChannelId() != 9 {
		t.Fatalf("old get channel: %v", err)
	}
}
