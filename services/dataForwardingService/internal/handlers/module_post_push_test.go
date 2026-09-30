package handlers

import (
	"errors"
	"testing"

	pb "Betterfly2/proto/data_forwarding"
	"data_forwarding_service/internal/router"
)

func TestMembersWithoutSender(t *testing.T) {
	targets := membersWithoutSender([]int64{1, 2, 3}, 1)
	if len(targets) != 2 || targets[0] != 2 || targets[1] != 3 {
		t.Fatalf("unexpected push targets: %v", targets)
	}
}

func TestGroupPartialDeliveryFailureIsRetryableAndDoesNotSkipOtherPods(t *testing.T) {
	localFailure := errors.New("local queue full")
	remoteFailure := errors.New("Kafka unavailable")
	var localCalls, remoteCalls int
	err := routeGroupTargets(42, []int64{2, 3, 4, 5}, map[string]string{"2": "local", "3": "remote", "4": "remote"}, "local",
		func(string, string) error { localCalls++; return localFailure },
		func(topic string, users []int64) error {
			remoteCalls++
			if topic != "remote" || len(users) != 2 {
				t.Fatalf("unexpected batch %s %v", topic, users)
			}
			return remoteFailure
		})
	if localCalls != 1 || remoteCalls != 1 || !errors.Is(err, localFailure) || !errors.Is(err, remoteFailure) || errors.Is(err, router.ErrUserOffline) {
		t.Fatalf("partial failure suppressed: local=%d remote=%d err=%v", localCalls, remoteCalls, err)
	}
}

func TestGroupDeliverySuccess(t *testing.T) {
	if err := routeGroupTargets(42, []int64{2, 3}, map[string]string{"2": "local", "3": "remote"}, "local",
		func(string, string) error { return nil }, func(string, []int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestStoredGroupOfflineMemberDoesNotFailOnlineDelivery(t *testing.T) {
	var localCalls, remoteCalls int
	err := routeGroupTargets(42, []int64{4, 2, 3}, map[string]string{"2": "local", "3": "remote"}, "local",
		func(userID, _ string) error {
			localCalls++
			if userID != "2" {
				t.Fatalf("unexpected local target: %s", userID)
			}
			return nil
		}, func(_ string, users []int64) error {
			remoteCalls++
			if len(users) != 1 || users[0] != 3 {
				t.Fatalf("unexpected remote targets: %v", users)
			}
			return nil
		})
	if err != nil || localCalls != 1 || remoteCalls != 1 {
		t.Fatalf("offline member triggered whole-message failure: local=%d remote=%d err=%v", localCalls, remoteCalls, err)
	}
}

func TestMessagePushIncludesSafeTextPreview(t *testing.T) {
	post := &pb.Post{FromId: 1, ToId: 88, IsGroup: true, Msg: "private message", MsgType: "text", Timestamp: "2026-07-11T10:00:00Z"}
	request := buildMessagePushRequest([]int64{2, 3}, post, 123).GetMessagePush()
	if request.GetSenderUserId() != 1 || request.GetConversationId() != 88 || !request.GetIsGroup() || request.GetMessageType() != "text" || len(request.GetTargetUserIds()) != 2 {
		t.Fatalf("unexpected message push metadata: %+v", request)
	}
	if request.GetPreview() != "private message" {
		t.Fatalf("unexpected message preview: %q", request.GetPreview())
	}
	if request.GetMessageId() != 123 {
		t.Fatalf("message ID missing from push request: %+v", request)
	}
}

func TestMessagePushPreviewHidesStorageIdentifiers(t *testing.T) {
	tests := []struct {
		post *pb.Post
		want string
	}{
		{&pb.Post{MsgType: "image", Msg: "private-file-hash"}, "发送了一张图片"},
		{&pb.Post{MsgType: "file", Msg: "private-file-hash", RealFileName: "report.pdf"}, "发送了文件：report.pdf"},
		{&pb.Post{MsgType: "audio", Msg: "private-file-hash"}, "发送了一条语音"},
	}
	for _, test := range tests {
		if got := messagePushPreview(test.post); got != test.want {
			t.Fatalf("preview mismatch: got %q want %q", got, test.want)
		}
	}
}

func TestDirectMessagePushUsesSenderAsRecipientConversation(t *testing.T) {
	post := &pb.Post{FromId: 7, ToId: 9, MsgType: "link"}
	request := buildMessagePushRequest([]int64{9}, post, 124).GetMessagePush()
	if request.GetConversationId() != 7 || request.GetIsGroup() {
		t.Fatalf("unexpected direct conversation metadata: %+v", request)
	}
}
