package consumer

import (
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestBuildPostAckResponse(t *testing.T) {
	resp := buildPostAckResponse(&storage.StoreMsgRsp{MessageId: 12345})

	if resp.GetPostAckRsp() == nil {
		t.Fatalf("expected PostAckRsp payload, got %T", resp.Payload)
	}
	if resp.GetPostAckRsp().GetMessageId() != 12345 {
		t.Fatalf("expected message_id 12345, got %d", resp.GetPostAckRsp().GetMessageId())
	}
}
