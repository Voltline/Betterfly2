package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/utils"
	"strings"
	"testing"
)

func TestRejectedPostSendsAckBeforeLegacyWarn(t *testing.T) {
	tests := []struct {
		name   string
		post   *pb.Post
		result pb.PostAckResult
		reason string
		warn   bool
	}{
		{"invalid caption", &pb.Post{ToId: 2, MsgType: "text", Msg: "hi", Caption: "x", ClientMessageId: " c-1 "}, pb.PostAckResult_POST_ACK_INVALID_ARGUMENT, utils.ErrInvalidCaption.Error(), true},
		{"channel source", &pb.Post{ToId: 2, MsgType: "text", Msg: "hi", SourceChannelMessageId: 5, ClientMessageId: "c-2"}, pb.PostAckResult_POST_ACK_FORBIDDEN, errPostForbidden.Error(), true},
		{"NUL content", &pb.Post{ToId: 2, MsgType: "text", Msg: "a\x00b", ClientMessageId: "c-3"}, pb.PostAckResult_POST_ACK_INVALID_ARGUMENT, errInvalidPostFields.Error(), false},
		{"content too long", &pb.Post{ToId: 2, MsgType: "text", Msg: strings.Repeat("中", 701), ClientMessageId: "c-4"}, pb.PostAckResult_POST_ACK_INVALID_ARGUMENT, errInvalidPostFields.Error(), false},
		{"file without hash", &pb.Post{ToId: 2, MsgType: "file", RealFileName: "a.txt", ClientMessageId: "c-5"}, pb.PostAckResult_POST_ACK_INVALID_ARGUMENT, "文件消息缺少file_hash", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := postRequestResult(1, handleAuthenticatedPost(1, test.post))
			if err != nil {
				t.Fatal(err)
			}
			ack := result.response.GetPostAckRsp()
			if ack == nil || ack.GetMessageId() != 0 || ack.GetResult() != test.result || ack.GetReason() != test.reason || ack.GetClientMessageId() != strings.TrimSpace(test.post.GetClientMessageId()) {
				t.Fatalf("ack = %+v", result.response)
			}
			if warn := result.followUp.GetWarn(); test.warn != (warn != nil) || (warn != nil && warn.GetWarningMessage() != test.reason) {
				t.Fatalf("followUp = %+v", result.followUp)
			}
		})
	}
}
