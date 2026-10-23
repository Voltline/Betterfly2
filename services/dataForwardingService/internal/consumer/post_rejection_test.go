package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	storage "Betterfly2/proto/storage"
	"data_forwarding_service/internal/router"
	"testing"
)

func TestStorageRejectionReleasesClaimAndSendsAckThenWarn(t *testing.T) {
	for result, want := range map[storage.StorageResult]pb.PostAckResult{
		storage.StorageResult_INVALID_ARGUMENT: pb.PostAckResult_POST_ACK_INVALID_ARGUMENT,
		storage.StorageResult_FORBIDDEN:        pb.PostAckResult_POST_ACK_FORBIDDEN,
	} {
		released := false
		var sent []*pb.ResponseMessage
		err := processRejectedPostResponse(result, &storage.StoreMsgRsp{ClientMessageId: "c-1", FromUserId: 1, ToUserId: 2}, func() { released = true }, func(response *pb.ResponseMessage) error {
			if !released {
				t.Fatal("claim must be released before the sender can retry")
			}
			sent = append(sent, response)
			return nil
		})
		if err != nil || len(sent) != 2 {
			t.Fatalf("err=%v sent=%v", err, sent)
		}
		ack := sent[0].GetPostAckRsp()
		if ack.GetResult() != want || ack.GetMessageId() != 0 || ack.GetClientMessageId() != "c-1" || ack.GetReason() == "" {
			t.Fatalf("ack = %+v", ack)
		}
		if sent[1].GetWarn().GetWarningMessage() != ack.GetReason() {
			t.Fatalf("warn = %+v", sent[1])
		}
	}
}

func TestStorageRejectionForOfflineSenderIsNotRetried(t *testing.T) {
	calls := 0
	err := processRejectedPostResponse(storage.StorageResult_FORBIDDEN, &storage.StoreMsgRsp{ClientMessageId: "c-1"}, func() {}, func(*pb.ResponseMessage) error {
		calls++
		return router.ErrUserOffline
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
