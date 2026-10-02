package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestReplyCanonicalDeliveryAckAndSyncMapping(t *testing.T) {
	stored := &storage.StoreMsgRsp{MessageId: 45, ReplyToMessageId: 44, ClientMessageId: "stable", ServerTimestamp: "2026-10-02T00:00:00Z", Created: true, Content: "canonical"}
	deliveries := 0
	for _, created := range []bool{true, false} {
		stored.Created = created
		err := processStoredPostResponse(stored, func() error { return nil }, func(id int64, post *pb.Post) error {
			deliveries++
			if post.GetReplyToMessageId() != 44 || id != 45 || post.GetMsg() != "canonical" {
				t.Fatal("canonical reference lost")
			}
			return nil
		}, func(response *pb.ResponseMessage) error {
			if ack := response.GetPostAckRsp(); ack.GetMessageId() != 45 || ack.GetClientMessageId() != "stable" || ack.GetTimestamp() != stored.GetServerTimestamp() {
				t.Fatal(ack)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if deliveries != 1 {
		t.Fatal("canonical replay delivered twice")
	}
	response := buildSyncMessagesResponse(&storage.SyncMessagesRsp{Msgs: []*storage.MessageRsp{{MessageId: 45, ReplyToMessageId: 44}}, RecalledMsgs: []*storage.MessageRsp{{MessageId: 46, ReplyToMessageId: 44, IsRecalled: true}}})
	if response.GetSyncMsgsRsp().Msgs[0].GetReplyToMessageId() != 44 || response.GetSyncMsgsRsp().RecalledMsgs[0].GetReplyToMessageId() != 44 {
		t.Fatal("sync conversion lost reference")
	}
}

func TestGroupNotifyResponseAndJoinedListMapping(t *testing.T) {
	if !isStructuredGroupOperation("update_group_notify") {
		t.Fatal("preference response became legacy text")
	}
	response := buildGroupMemberOperationResponse(&friend.GroupOperationRsp{Operation: "update_group_notify", GroupId: 9, UserId: 2, NotificationsMuted: true}, friend.FriendResult_FRIEND_OK)
	if op := response.GetGroupMemberOperationRsp(); op.GetResult() != "FRIEND_OK" || !op.GetNotificationsMuted() || op.GetUserId() != 2 {
		t.Fatal(op)
	}
	joined := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{{GroupId: 9, IsChannel: true, NotificationsMuted: true}}})
	if !joined.GetJoinedGroupsRsp().Groups[0].GetNotificationsMuted() {
		t.Fatal("joined list lost preference")
	}
}
