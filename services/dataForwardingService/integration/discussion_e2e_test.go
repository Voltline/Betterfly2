package integration

import (
	channel "Betterfly2/proto/channel"
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/db"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestChannelDiscussionEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("requires dedicated Compose with schema v9 and all services rebuilt")
	}
	database := acceptanceDatabase(t)
	var users []*networkProbe
	for i, port := range []string{envOr("BETTERFLY_DF_PORT_1", defaultDFPort1), envOr("BETTERFLY_DF_PORT_2", defaultDFPort2)} {
		p, err := openNetworkProbe(port)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		if err := p.signup(randomAccount(t, fmt.Sprintf("discussion%d", i))); err != nil {
			t.Fatal(err)
		}
		users = append(users, p)
	}
	owner, reader := users[0], users[1]
	channelID, groupID := time.Now().UnixMilli()*1000, time.Now().UnixMilli()*1000+1
	if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: channelID, Name: "discussion channel"}}}); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroup{InsertGroup: &pb.InsertGroup{ToBeCreatedGroupId: groupID, ToBeCreatedGroupName: "discussion group"}}}, func(r *pb.ResponseMessage) bool { return r.GetServer() != nil })
	bind := func(p *networkProbe, target int64) *channel.ChannelResponse {
		return probeChannel(t, p, &channel.ChannelRequest{Payload: &channel.ChannelRequest_SetDiscussionGroup{SetDiscussionGroup: &channel.SetDiscussionGroup{ChannelId: channelID, GroupId: target}}})
	}
	if r := bind(reader, groupID); r.Result != channel.ChannelResult_CHANNEL_FORBIDDEN {
		t.Fatal("non-owner linked discussion", r)
	}
	if r := bind(owner, groupID); r.Result != channel.ChannelResult_CHANNEL_OK || r.Channel.DiscussionGroupId != groupID {
		t.Fatal(r)
	}
	postID := probeChannelPost(t, owner, channelID, "announcement")
	discussion := func(p *networkProbe) *channel.ChannelResponse {
		requestID := fmt.Sprintf("discussion-%d", time.Now().UnixNano())
		return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_GetDiscussion{GetDiscussion: &channel.GetDiscussion{RequestId: requestID, ChannelId: channelID, PostMessageId: postID}}}, func(r *pb.ResponseMessage) bool {
			return r.GetChannelResponse().GetRequestId() == requestID
		}).GetChannelResponse()
	}
	info := discussion(reader)
	rootID := info.GetDiscussion().GetRootMessageId()
	if info.Result != channel.ChannelResult_CHANNEL_OK || rootID <= 0 || info.Discussion.Joined || info.Discussion.CanComment {
		t.Fatal("pre-join discovery", info)
	}
	replies := func(p *networkProbe, before int64, size int32) *channel.ChannelResponse {
		requestID := fmt.Sprintf("replies-%d", time.Now().UnixNano())
		return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_QueryDiscussionReplies{QueryDiscussionReplies: &channel.QueryDiscussionReplies{RequestId: requestID, RootMessageId: rootID, BeforeMessageId: before, PageSize: size}}}, func(r *pb.ResponseMessage) bool {
			return r.GetChannelResponse().GetRequestId() == requestID
		}).GetChannelResponse()
	}
	if r := replies(reader, 0, 50); r.Result != channel.ChannelResult_CHANNEL_FORBIDDEN {
		t.Fatal("pre-join comments exposed", r)
	}
	comment := func(p *networkProbe, clientID, content string) *pb.PostAckRsp {
		return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: &pb.Post{ToId: groupID, IsGroup: true, MsgType: "text", Msg: content, ClientMessageId: clientID, DiscussionRootMessageId: rootID}}}, func(r *pb.ResponseMessage) bool {
			return r.GetPostAckRsp().GetClientMessageId() == clientID
		}).GetPostAckRsp()
	}
	old := comment(owner, "old-comment", "before member joined")
	ordinaryID := probeChannelPost(t, owner, groupID, "ordinary pre-join chat")
	// Message and membership timestamps have second precision.
	time.Sleep(1100 * time.Millisecond)
	join := probeRequest(t, reader, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroupUser{InsertGroupUser: &pb.InsertGroupUser{TargetGroupId: groupID}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
	if join.GetRequest().GetRequestId() <= 0 {
		t.Fatal(join)
	}
	accepted := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_ResolveGroupJoinRequest{ResolveGroupJoinRequest: &pb.ResolveGroupJoinRequest{RequestId: join.Request.RequestId, Decision: pb.RequestDecision_REQUEST_ACCEPT}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
	if accepted.GetResult() != "FRIEND_OK" {
		t.Fatal(accepted)
	}
	if r := replies(reader, 0, 50); r.Result != channel.ChannelResult_CHANNEL_OK || len(r.Posts) != 1 || r.Posts[0].MessageId != old.MessageId || !r.Discussion.CanComment {
		t.Fatal("pre-join thread history missing", r)
	}
	current := comment(owner, "new-comment", "cross-pod comment")
	post, err := reader.wait(15*time.Second, func(r *pb.ResponseMessage) bool { return r.GetPost().GetMessageId() == current.MessageId })
	if err != nil || post.message.GetPost().GetDiscussionRootMessageId() != rootID || post.message.GetPost().GetToId() != groupID {
		t.Fatal("realtime thread metadata", err)
	}
	replayed := comment(owner, "new-comment", "different replay content")
	if replayed.MessageId != current.MessageId || replayed.Timestamp != current.Timestamp {
		t.Fatal("non-canonical comment retry", replayed)
	}
	page := replies(reader, 0, 1)
	if !page.HasMore || len(page.Posts) != 1 || page.Posts[0].MessageId != current.MessageId || page.Discussion.ReplyCount != 2 {
		t.Fatal("comment first page", page)
	}
	page = replies(reader, page.NextBeforeMessageId, 1)
	if page.HasMore || len(page.Posts) != 1 || page.Posts[0].MessageId != old.MessageId {
		t.Fatal("comment older page", page)
	}
	syncPage := probeSync(t, reader, &pb.QuerySyncMessages{Timestamp: "2000-01-01T00:00:00Z", PageSize: 100})
	seen := map[int64]bool{}
	for _, m := range syncPage.Msgs {
		seen[m.MessageId] = true
		if m.MessageId == rootID && (m.SourceChannelMessageId != postID || m.Content != "") {
			t.Fatal("reference card contains copied content", m)
		}
	}
	if !seen[rootID] || !seen[old.MessageId] || !seen[current.MessageId] || seen[ordinaryID] {
		t.Fatal("sync history authorization", seen)
	}
	if r := bind(owner, 0); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	if r := replies(reader, 0, 50); !r.Discussion.Closed || r.Discussion.CanComment || len(r.Posts) != 2 {
		t.Fatal("unlink did not preserve read-only history", r)
	}
	probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_RecallMessage{RecallMessage: &pb.RecallMessage{MessageId: postID}}}, func(r *pb.ResponseMessage) bool { return r.GetMessageRecallEvent().GetMessageId() == postID })
	var root db.Message
	if err := database.First(&root, "message_id = ?", rootID).Error; err != nil || !root.IsRecalled {
		t.Fatal("source recall did not recall card", err)
	}
	if r := replies(reader, 0, 50); !r.Discussion.Post.IsRecalled || r.Discussion.Post.Content != "" || len(r.Posts) != 2 {
		t.Fatal("recall lost comments or exposed source", r)
	}
	var count int64
	if err := database.Model(&db.Message{}).Where("from_user_id = ? AND client_message_id = ?", owner.userID, "new-comment").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate comment inserted", count, err)
	}
}
