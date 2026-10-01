package integration

import (
	channel "Betterfly2/proto/channel"
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/db"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestImageCaptionEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("requires dedicated Compose rebuilt with schema v7")
	}
	database := acceptanceDatabase(t)
	var users []*networkProbe
	for i, port := range []string{envOr("BETTERFLY_DF_PORT_1", defaultDFPort1), envOr("BETTERFLY_DF_PORT_2", defaultDFPort2)} {
		p, err := openNetworkProbe(port)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		if err := p.signup(randomAccount(t, fmt.Sprintf("caption%d", i))); err != nil {
			t.Fatal(err)
		}
		users = append(users, p)
	}
	owner, reader := users[0], users[1]
	id := time.Now().UnixMilli() * 1000
	created := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: id, Name: "配文验收", Visibility: channel.Visibility_PUBLIC}}})
	if created.GetResult() != channel.ChannelResult_CHANNEL_OK {
		t.Fatal("create failed")
	}
	t.Cleanup(func() {
		probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Delete{Delete: &channel.DeleteChannel{ChannelId: id}}})
	})
	if joined := probeChannel(t, reader, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Subscribe{Subscribe: &channel.SubscribeChannel{ChannelId: id}}}); joined.GetResult() != channel.ChannelResult_CHANNEL_OK {
		t.Fatal("subscribe failed")
	}
	// Message-link fixture; real object upload/verify has separate HTTP tests.
	digest := sha512.Sum512([]byte("caption-image-fixture"))
	hash := hex.EncodeToString(digest[:])
	caption := " # 公告\n**完整配文** 😀\n最后一行  "
	clientID := fmt.Sprintf("caption-%d", id)
	post := &pb.Post{ToId: id, IsGroup: true, MsgType: "image", Msg: hash, Caption: caption, ClientMessageId: clientID}
	send := func() *pb.PostAckRsp {
		return probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: post}}, func(r *pb.ResponseMessage) bool { return r.GetPostAckRsp().GetClientMessageId() == clientID }).GetPostAckRsp()
	}
	ack := send()
	if ack.GetMessageId() <= 0 || ack.GetTimestamp() == "" {
		t.Fatal("missing canonical ACK")
	}
	received, err := reader.wait(15*time.Second, func(r *pb.ResponseMessage) bool { return r.GetPost().GetMessageId() == ack.GetMessageId() })
	if err != nil {
		t.Fatal(err)
	}
	if got := received.message.GetPost(); got.GetMsg() != hash || got.GetCaption() != caption || got.GetTimestamp() != ack.GetTimestamp() {
		t.Fatal("realtime content mismatch")
	}
	query := func() *pb.MessageRsp {
		return probeRequest(t, reader, &pb.RequestMessage{Payload: &pb.RequestMessage_QueryMessage{QueryMessage: &pb.QueryMessage{MessageId: ack.GetMessageId()}}}, func(r *pb.ResponseMessage) bool { return r.GetMessageRsp().GetMessageId() == ack.GetMessageId() }).GetMessageRsp()
	}
	if got := query(); got.GetCaption() != caption || got.GetContent() != hash {
		t.Fatal("query mismatch")
	}
	if history := probeChannelHistory(t, reader, id, 0, 50); len(history.GetPosts()) != 1 || history.Posts[0].GetCaption() != caption || history.Posts[0].GetContent() != hash {
		t.Fatal("history mismatch")
	}
	page := probeSync(t, reader, &pb.QuerySyncMessages{Timestamp: "2000-01-01T00:00:00Z", PageSize: 50})
	if len(page.GetMsgs()) != 1 || page.Msgs[0].GetCaption() != caption || page.Msgs[0].GetContent() != hash {
		t.Fatal("sync mismatch")
	}
	post.Msg, post.Caption = "changed-hash", "changed retry caption"
	duplicate := send()
	if duplicate.GetMessageId() != ack.GetMessageId() || duplicate.GetTimestamp() != ack.GetTimestamp() {
		t.Fatal("retry changed canonical ACK")
	}
	var count int64
	if err := database.Model(&db.Message{}).Where("from_user_id = ? AND client_message_id = ?", owner.userID, clientID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate persisted")
	}
	if got := query(); got.GetContent() != hash || got.GetCaption() != caption {
		t.Fatal("retry overwrote original")
	}
	if _, err := reader.wait(500*time.Millisecond, func(r *pb.ResponseMessage) bool { return r.GetPost().GetMessageId() == ack.GetMessageId() }); err == nil {
		t.Fatal("duplicate realtime")
	}
	probeRequest(t, reader, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: &pb.Post{ToId: id, IsGroup: true, MsgType: "image", Msg: hash, Caption: caption, ClientMessageId: "forbidden"}}}, func(r *pb.ResponseMessage) bool { return r.GetWarn() != nil })
	probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_RecallMessage{RecallMessage: &pb.RecallMessage{MessageId: ack.GetMessageId()}}}, func(r *pb.ResponseMessage) bool {
		return r.GetMessageRecallEvent().GetMessageId() == ack.GetMessageId()
	})
	if got := query(); !got.GetIsRecalled() || got.GetContent() != "" || got.GetCaption() != "" || got.GetRealFileName() != "" {
		t.Fatal("query recall leak")
	}
	history := probeChannelHistory(t, reader, id, 0, 50)
	if len(history.GetPosts()) != 1 || !history.Posts[0].GetIsRecalled() || history.Posts[0].GetCaption() != "" || history.Posts[0].GetContent() != "" {
		t.Fatal("history recall leak")
	}
	page = probeSync(t, reader, &pb.QuerySyncMessages{Timestamp: "2000-01-01T00:00:00Z", IncludeRecalledChanges: true, PageSize: 50})
	for _, messages := range [][]*pb.MessageRsp{page.GetMsgs(), page.GetRecalledMsgs()} {
		if len(messages) != 1 || !messages[0].GetIsRecalled() || messages[0].GetCaption() != "" || messages[0].GetContent() != "" {
			t.Fatal("sync recall leak")
		}
	}
	if duplicate = send(); duplicate.GetMessageId() != ack.GetMessageId() || duplicate.GetTimestamp() != ack.GetTimestamp() {
		t.Fatal("recall retry changed ACK")
	}
}
