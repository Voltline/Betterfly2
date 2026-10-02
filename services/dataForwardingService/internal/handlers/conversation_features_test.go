package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestGroupNotifyBridgeUsesAuthenticatedActor(t *testing.T) {
	request := buildUpdateGroupNotifyFriendRequest(2, &pb.UpdateGroupNotify{TargetGroupId: 9, IsNotify: false}, "df-pod")
	if request.GetTargetUserId() != 2 || request.GetFromKafkaTopic() != "df-pod" || request.GetUpdateGroupNotify().GetGroupId() != 9 || request.GetUpdateGroupNotify().GetIsNotify() {
		t.Fatal(request)
	}
	unauthenticated := &pb.RequestMessage{Payload: &pb.RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &pb.UpdateGroupNotify{TargetGroupId: 9}}}
	if _, err := getDFRequestRouter().Dispatch(dfRequestContext{fromID: 2, message: unauthenticated}, unauthenticated.Payload); err == nil {
		t.Fatal("notification preference accepted without JWT")
	}
}

func TestReplyBridgeAndCrossPodPostPreserveReference(t *testing.T) {
	post := &pb.Post{FromId: 1, ToId: 2, MessageId: 45, MsgType: "text", Msg: "reply", ReplyToMessageId: 44, ClientMessageId: "stable"}
	if err := validatePostPayload(post); err != nil {
		t.Fatal(err)
	}
	if buildStoreNewMessageStorageRequest(post, "pod").GetStoreNewMessage().GetReplyToMessageId() != 44 {
		t.Fatal("storage bridge dropped reference")
	}
	for _, topic := range []string{"local", "remote"} {
		encoded, err := buildPostDeliveryMessageBytes(topic, "local", post, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}})
		if err != nil {
			t.Fatal(err)
		}
		var decoded *pb.Post
		if topic == "local" {
			response := &pb.ResponseMessage{}
			if err := proto.Unmarshal(encoded, response); err != nil {
				t.Fatal(err)
			}
			decoded = response.GetPost()
		} else {
			request := &pb.RequestMessage{}
			if err := proto.Unmarshal(encoded, request); err != nil {
				t.Fatal(err)
			}
			decoded = request.GetPost()
		}
		if !proto.Equal(post, decoded) {
			t.Fatal("cross Pod dropped reference")
		}
	}
	post.ReplyToMessageId = -1
	if validatePostPayload(post) == nil {
		t.Fatal("negative reply accepted")
	}
}
