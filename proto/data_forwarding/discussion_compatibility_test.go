package data_forwarding

import (
	channel "Betterfly2/proto/channel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

func TestDiscussionProtocolRoundTripAndLegacyDefaults(t *testing.T) {
	legacy := &RequestMessage{}
	if err := proto.Unmarshal([]byte{0x2a, 0x06, 0x10, 0x01, 0x18, 0x09, 0x22, 0x00}, legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.GetPost() == nil || legacy.GetPost().GetDiscussionRootMessageId() != 0 || legacy.GetPost().GetSourceChannelMessageId() != 0 {
		t.Fatal(legacy)
	}
	for _, message := range []proto.Message{
		&RequestMessage{Payload: &RequestMessage_GetDiscussion{GetDiscussion: &channel.GetDiscussion{RequestId: "get", ChannelId: 9001, PostMessageId: 100}}},
		&RequestMessage{Payload: &RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: 101, PageSize: 50}}},
		&Post{MessageId: 102, DiscussionRootMessageId: 101, ReplyToMessageId: 101},
		&channel.ChannelResponse{Discussion: &channel.DiscussionInfo{ChannelId: 9001, GroupId: 9002, RootMessageId: 101, Joined: true}},
	} {
		data, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		out := message.ProtoReflect().Type().New().Interface()
		if err := proto.Unmarshal(data, out); err != nil || !proto.Equal(message, out) {
			t.Fatal(out, err)
		}
	}
	for _, f := range []struct {
		m      proto.Message
		name   string
		number int
	}{{&RequestMessage{}, "get_discussion", 42}, {&RequestMessage{}, "query_discussion_replies", 43}, {&Post{}, "discussion_root_message_id", 12}, {&Post{}, "source_channel_message_id", 13}, {&MessageRsp{}, "discussion_root_message_id", 14}, {&channel.ChannelRequest{}, "set_discussion_group", 12}, {&channel.ChannelInfo{}, "discussion_group_id", 14}, {&channel.ChannelPost{}, "discussion_root_message_id", 12}, {&channel.ChannelResponse{}, "discussion", 11}} {
		d := f.m.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(f.name))
		if d == nil || int(d.Number()) != f.number {
			t.Fatal(f.name)
		}
	}
}
