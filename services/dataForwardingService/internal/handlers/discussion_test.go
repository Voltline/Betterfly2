package handlers

import (
	channel "Betterfly2/proto/channel"
	pb "Betterfly2/proto/data_forwarding"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestDiscussionBridgesAndAuthentication(t *testing.T) {
	for _, r := range []*pb.RequestMessage{
		{Payload: &pb.RequestMessage_GetDiscussion{GetDiscussion: &channel.GetDiscussion{ChannelId: 9001, PostMessageId: 100}}},
		{Payload: &pb.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RootMessageId: 101}}},
	} {
		if _, err := getDFRequestRouter().Dispatch(dfRequestContext{fromID: 2, message: r}, r.Payload); err == nil {
			t.Fatal("discussion query accepted without JWT")
		}
	}
	post := &pb.Post{FromId: 2, ToId: 9002, IsGroup: true, Msg: "comment", MsgType: "text", DiscussionRootMessageId: 101, ReplyToMessageId: 101}
	r := buildStoreNewMessageStorageRequest(post, "df-pod")
	if r.GetTargetUserId() != 2 || r.GetStoreNewMessage().GetDiscussionRootMessageId() != 101 {
		t.Fatal(r)
	}
	for _, topic := range []string{"local", "remote"} {
		data, err := buildPostDeliveryMessageBytes(topic, "local", post, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}})
		if err != nil {
			t.Fatal(err)
		}
		var decoded *pb.Post
		if topic == "local" {
			r := &pb.ResponseMessage{}
			if err := proto.Unmarshal(data, r); err != nil {
				t.Fatal(err)
			}
			decoded = r.GetPost()
		} else {
			r := &pb.RequestMessage{}
			if err := proto.Unmarshal(data, r); err != nil {
				t.Fatal(err)
			}
			decoded = r.GetPost()
		}
		if !proto.Equal(post, decoded) {
			t.Fatal(decoded)
		}
	}
	push := buildMessagePushRequest([]int64{1}, post, 102)
	if push.GetMessagePush().GetDiscussionRootMessageId() != 101 {
		t.Fatal(push)
	}
	post.DiscussionRootMessageId = -1
	if validatePostPayload(post) == nil {
		t.Fatal("negative thread accepted")
	}
	post.DiscussionRootMessageId = 101
	post.IsGroup = false
	if validatePostPayload(post) == nil {
		t.Fatal("private chat thread accepted")
	}
}

func TestDiscussionRootNeverPublishesDuplicateAPNs(t *testing.T) {
	if err := publishMessagePush([]int64{1}, &pb.Post{SourceChannelMessageId: 100, DiscussionRootMessageId: 101}, 101); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionLateReplaySuppressesRecalledContent(t *testing.T) {
	mock := captionDeliveryDatabase(t)
	post := &pb.Post{FromId: 1, ToId: 9002, IsGroup: true, MsgType: "text", MessageId: 101, DiscussionRootMessageId: 101, SourceChannelMessageId: 100}
	for range 2 {
		mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(true))
		if err := DeliverStoredPost(101, post); err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"is_recalled"}).AddRow(true))
		if err := InplaceHandlePostMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}); err != nil {
			t.Fatal(err)
		}
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnError(injected)
	if allowed, err := ImagePostDeliveryAllowed(post); allowed || !errors.Is(err, injected) {
		t.Fatal("database failure permitted stale thread delivery", err)
	}
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id", "discussion_root_message_id", "source_channel_message_id"}).AddRow(101, 1, 9002, 999, 100))
	if allowed, err := ImagePostDeliveryAllowed(post); allowed || err != nil {
		t.Fatal("non-canonical thread identity permitted", err)
	}
}

func TestDiscussionCrossPodRecipientFilterIsBatched(t *testing.T) {
	mock := captionDeliveryDatabase(t)
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "to_user_id", "is_group", "discussion_root_message_id"}).AddRow(102, 9002, true, 101))
	mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
	mock.ExpectQuery(`(?s)SELECT gm.user_id.*JOIN group_members gm.*origin.is_public`).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(2))
	ids, err := DiscussionDeliveryRecipients(&pb.Post{MessageId: 102, DiscussionRootMessageId: 101}, []int64{2, 3, 4})
	if err != nil || len(ids) != 1 || ids[0] != 2 {
		t.Fatal("departed/private-source users were not filtered", ids, err)
	}
}
