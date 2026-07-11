package handlers

import (
	"testing"

	pb "Betterfly2/proto/data_forwarding"
)

func TestMembersWithoutSender(t *testing.T) {
	targets := membersWithoutSender([]int64{1, 2, 3}, 1)
	if len(targets) != 2 || targets[0] != 2 || targets[1] != 3 {
		t.Fatalf("unexpected push targets: %v", targets)
	}
}

func TestMessagePushIncludesSafeTextPreview(t *testing.T) {
	post := &pb.Post{FromId: 1, ToId: 88, IsGroup: true, Msg: "private message", MsgType: "text", Timestamp: "2026-07-11T10:00:00Z"}
	request := buildMessagePushRequest([]int64{2, 3}, post).GetMessagePush()
	if request.GetSenderUserId() != 1 || request.GetConversationId() != 88 || !request.GetIsGroup() || request.GetMessageType() != "text" || len(request.GetTargetUserIds()) != 2 {
		t.Fatalf("unexpected message push metadata: %+v", request)
	}
	if request.GetPreview() != "private message" {
		t.Fatalf("unexpected message preview: %q", request.GetPreview())
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
	request := buildMessagePushRequest([]int64{9}, post).GetMessagePush()
	if request.GetConversationId() != 7 || request.GetIsGroup() {
		t.Fatalf("unexpected direct conversation metadata: %+v", request)
	}
}
