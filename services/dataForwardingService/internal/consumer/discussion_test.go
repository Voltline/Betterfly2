package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestDiscussionRootDeliveryDoesNotAckOrCompleteClientPost(t *testing.T) {
	stored := &storage.StoreMsgRsp{MessageId: 101, FromUserId: 1, ToUserId: 9002, IsGroup: true, Created: true, DiscussionRootMessageId: 101, SourceChannelMessageId: 100}
	delivered := 0
	if err := processStoredPostResponse(stored, func() error { t.Fatal("root completed client idempotency"); return nil }, func(id int64, post *pb.Post) error {
		delivered++
		if id != 101 || post.GetDiscussionRootMessageId() != 101 || post.GetSourceChannelMessageId() != 100 || post.GetMsg() != "" {
			t.Fatal(post)
		}
		return nil
	}, func(*pb.ResponseMessage) error { t.Fatal("root generated a second ACK"); return nil }); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatal(delivered)
	}
}

func TestDiscussionCommentCanonicalReplayAndSync(t *testing.T) {
	stored := &storage.StoreMsgRsp{MessageId: 102, FromUserId: 2, ToUserId: 9002, IsGroup: true, Created: true, Content: "comment", DiscussionRootMessageId: 101, ReplyToMessageId: 101, ClientMessageId: "comment-id", ServerTimestamp: "2026-10-02T00:00:00Z"}
	deliveries, acks := 0, 0
	for _, created := range []bool{true, false} {
		stored.Created = created
		err := processStoredPostResponse(stored, func() error { return nil }, func(id int64, p *pb.Post) error {
			deliveries++
			if p.GetDiscussionRootMessageId() != 101 || p.GetReplyToMessageId() != 101 || p.GetMsg() != "comment" {
				t.Fatal(p)
			}
			return nil
		}, func(r *pb.ResponseMessage) error {
			acks++
			if r.GetPostAckRsp().GetMessageId() != 102 || r.GetPostAckRsp().GetTimestamp() != stored.GetServerTimestamp() {
				t.Fatal(r)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if deliveries != 1 || acks != 2 {
		t.Fatal(deliveries, acks)
	}
	for _, m := range convertStorageMessages([]*storage.MessageRsp{{MessageId: 101, DiscussionRootMessageId: 101, SourceChannelMessageId: 100}, {MessageId: 102, DiscussionRootMessageId: 101, IsRecalled: true}}) {
		if m.GetDiscussionRootMessageId() != 101 {
			t.Fatal(m)
		}
	}
}
