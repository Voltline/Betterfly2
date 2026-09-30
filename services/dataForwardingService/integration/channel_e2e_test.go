package integration

import (
	channel "Betterfly2/proto/channel"
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/db"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func probeChannel(t *testing.T, p *networkProbe, req *channel.ChannelRequest) *channel.ChannelResponse {
	t.Helper()
	if req.RequestId == "" {
		req.RequestId = fmt.Sprintf("channel-%d", time.Now().UnixNano())
	}
	return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_ChannelRequest{ChannelRequest: req}}, func(r *pb.ResponseMessage) bool {
		return r.GetChannelResponse() != nil && r.GetChannelResponse().RequestId == req.RequestId
	}).GetChannelResponse()
}

func probeChannelHistory(t *testing.T, p *networkProbe, id, before int64, size int32) *channel.ChannelResponse {
	t.Helper()
	requestID := fmt.Sprintf("history-%d", time.Now().UnixNano())
	return probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{RequestId: requestID, ChannelId: id, BeforeMessageId: before, PageSize: size}}}, func(r *pb.ResponseMessage) bool {
		return r.GetChannelResponse() != nil && r.GetChannelResponse().RequestId == requestID
	}).GetChannelResponse()
}

func probeChannelPost(t *testing.T, p *networkProbe, id int64, text string) int64 {
	t.Helper()
	clientID := fmt.Sprintf("channel-post-%d", time.Now().UnixNano())
	response := probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: &pb.Post{ToId: id, IsGroup: true, MsgType: "text", Msg: text, ClientMessageId: clientID}}}, func(r *pb.ResponseMessage) bool {
		return r.GetPostAckRsp() != nil && r.GetPostAckRsp().ClientMessageId == clientID
	})
	if response.GetPostAckRsp().GetMessageId() <= 0 {
		t.Fatal("no canonical channel post ID")
	}
	return response.GetPostAckRsp().MessageId
}

func TestChannelsEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("set BETTERFLY_ACCEPTANCE=1 against dedicated migrated Compose")
	}
	database := acceptanceDatabase(t)
	ports := []string{envOr("BETTERFLY_DF_PORT_1", defaultDFPort1), envOr("BETTERFLY_DF_PORT_2", defaultDFPort2), envOr("BETTERFLY_DF_PORT_2", defaultDFPort2)}
	var users []*networkProbe
	for i, port := range ports {
		p, err := openNetworkProbe(port)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.close)
		if err := p.signup(randomAccount(t, fmt.Sprintf("channel%d", i))); err != nil {
			t.Fatal(err)
		}
		users = append(users, p)
	}
	owner, subscriber, outsider := users[0], users[1], users[2]
	id := time.Now().UnixMilli() * 1000
	username := fmt.Sprintf("channel_%d", time.Now().UnixNano())
	created := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: id, Name: "频道验收", Description: "broadcast", Username: username, AvatarHash: "channel-avatar", Visibility: channel.Visibility_PUBLIC}}})
	if created.Result != channel.ChannelResult_CHANNEL_OK || created.Channel.MyRole != "owner" || created.Channel.SubscriberCount != 1 {
		t.Fatalf("create %v", created)
	}
	duplicate := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: id + 1, Name: "collision", Username: username}}})
	if duplicate.Result != channel.ChannelResult_CHANNEL_ALREADY_EXISTS {
		t.Fatalf("username conflict %v", duplicate)
	}
	var rolledBack int64
	if err := database.Model(&db.Group{}).Where("group_id = ?", id+1).Count(&rolledBack).Error; err != nil || rolledBack != 0 {
		t.Fatalf("duplicate username left partial group %d %v", rolledBack, err)
	}
	privateCreated := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: id + 2, Name: "private from creation", Visibility: channel.Visibility_PRIVATE}}})
	if privateCreated.Result != channel.ChannelResult_CHANNEL_OK || privateCreated.Channel.Visibility != channel.Visibility_PRIVATE {
		t.Fatalf("private creation %v", privateCreated)
	}
	if r := probeChannelHistory(t, outsider, id+2, 0, 50); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
		t.Fatalf("private false default became public %v", r)
	}
	if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Delete{Delete: &channel.DeleteChannel{ChannelId: id + 2}}}); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	defer func() {
		if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Delete{Delete: &channel.DeleteChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_OK && r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
			t.Errorf("cleanup channel: %v", r)
		}
	}()
	first := probeChannelPost(t, owner, id, "before subscription")
	preview := probeChannelHistory(t, outsider, id, 0, 1)
	if preview.Result != channel.ChannelResult_CHANNEL_OK || len(preview.Posts) != 1 || preview.Posts[0].MessageId != first {
		t.Fatalf("public preview %v", preview)
	}
	search := probeChannel(t, subscriber, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Search{Search: &channel.SearchChannels{Query: username, PageSize: 1}}})
	if search.Result != channel.ChannelResult_CHANNEL_OK || len(search.Channels) != 1 || search.Channels[0].ChannelId != id {
		t.Fatalf("search %v", search)
	}
	for i := 0; i < 2; i++ {
		joined := probeChannel(t, subscriber, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Subscribe{Subscribe: &channel.SubscribeChannel{ChannelId: id}}})
		if joined.Result != channel.ChannelResult_CHANNEL_OK || joined.Channel.SubscriberCount != 2 || joined.Channel.MyRole != "member" {
			t.Fatalf("subscribe/replay %v", joined)
		}
	}
	list := probeChannel(t, subscriber, &channel.ChannelRequest{Payload: &channel.ChannelRequest_ListSubscribed{ListSubscribed: &channel.ListSubscribedChannels{}}})
	if len(list.Channels) != 1 || list.Channels[0].ChannelId != id {
		t.Fatalf("subscribed list %v", list)
	}
	roster := probeChannel(t, subscriber, &channel.ChannelRequest{Payload: &channel.ChannelRequest_ListMembers{ListMembers: &channel.ListChannelMembers{ChannelId: id}}})
	if roster.Result != channel.ChannelResult_CHANNEL_FORBIDDEN {
		t.Fatalf("subscriber roster leak %v", roster)
	}
	if err := subscriber.send(&pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: &pb.Post{ToId: id, IsGroup: true, MsgType: "text", Msg: "forbidden", ClientMessageId: "channel-denied"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := subscriber.wait(10*time.Second, func(r *pb.ResponseMessage) bool { return r.GetWarn() != nil || r.GetRefused() != nil }); err != nil {
		t.Fatal(err)
	}
	var unauthorized int64
	if err := database.Model(&db.Message{}).Where("to_user_id = ? AND from_user_id = ?", id, subscriber.userID).Count(&unauthorized).Error; err != nil || unauthorized != 0 {
		t.Fatalf("subscriber publication persisted=%d err=%v", unauthorized, err)
	}
	second := probeChannelPost(t, owner, id, "realtime")
	received, err := subscriber.wait(15*time.Second, func(r *pb.ResponseMessage) bool { return r.GetPost() != nil && r.GetPost().MessageId == second })
	if err != nil || received.message.GetPost().ToId != id {
		t.Fatalf("cross-pod delivery %v", err)
	}
	page := probeChannelHistory(t, subscriber, id, 0, 1)
	if !page.HasMore || page.Posts[0].MessageId != second {
		t.Fatalf("history first page %v", page)
	}
	page = probeChannelHistory(t, subscriber, id, page.NextBeforeMessageId, 1)
	if page.HasMore || len(page.Posts) != 1 || page.Posts[0].MessageId != first {
		t.Fatalf("pre-subscription history %v", page)
	}
	name := "改名后的频道"
	updated := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Update{Update: &channel.UpdateChannel{ChannelId: id, Name: &name}}})
	if updated.Result != channel.ChannelResult_CHANNEL_OK || updated.Channel.Name != name || updated.Channel.Description != "broadcast" {
		t.Fatalf("PATCH reset fields %v", updated)
	}
	private := channel.Visibility_PRIVATE
	if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Update{Update: &channel.UpdateChannel{ChannelId: id, Visibility: &private}}}); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	for _, p := range []*networkProbe{outsider} {
		if r := probeChannel(t, p, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Get{Get: &channel.GetChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
			t.Fatalf("private metadata leak %v", r)
		}
		if r := probeChannelHistory(t, p, id, 0, 50); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
			t.Fatalf("private history leak %v", r)
		}
		if r := probeChannel(t, p, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Subscribe{Subscribe: &channel.SubscribeChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
			t.Fatalf("private direct join %v", r)
		}
		if r := probeChannel(t, p, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Unsubscribe{Unsubscribe: &channel.UnsubscribeChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
			t.Fatalf("private unsubscribe leaks existence %v", r)
		}
		legacyJoin := probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroupUser{InsertGroupUser: &pb.InsertGroupUser{TargetGroupId: id}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
		if legacyJoin.GetRequest() != nil {
			t.Fatalf("private channel accepted legacy join request %v", legacyJoin)
		}
	}
	invitation := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_InviteGroupMember{InviteGroupMember: &pb.InviteGroupMember{TargetGroupId: id, TargetUserId: outsider.userID}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
	invitationID := invitation.GetRequest().GetRequestId()
	if invitationID <= 0 {
		t.Fatalf("private invitation %v", invitation)
	}
	accepted := probeRequest(t, outsider, &pb.RequestMessage{Payload: &pb.RequestMessage_ResolveGroupInvitation{ResolveGroupInvitation: &pb.ResolveGroupInvitation{InvitationId: invitationID, Decision: pb.RequestDecision_REQUEST_ACCEPT}}}, func(r *pb.ResponseMessage) bool { return r.GetRelationshipOperationRsp() != nil }).GetRelationshipOperationRsp()
	if accepted.GetRequest().GetStatus() != "accepted" {
		t.Fatalf("private invite accept %v", accepted)
	}
	if page := probeChannelHistory(t, outsider, id, 0, 50); page.Result != channel.ChannelResult_CHANNEL_OK || len(page.Posts) != 2 {
		t.Fatalf("invited full history %v", page)
	}
	// Existing role API grants publication; subscription remains read-only otherwise.
	role := probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_UpdateGroupMemberRole{UpdateGroupMemberRole: &pb.UpdateGroupMemberRole{TargetGroupId: id, TargetUserId: subscriber.userID, Role: "admin"}}}, func(r *pb.ResponseMessage) bool { return r.GetGroupMemberOperationRsp() != nil }).GetGroupMemberOperationRsp()
	if role.GetRole() != "admin" {
		t.Fatal(role)
	}
	third := probeChannelPost(t, subscriber, id, "admin publication")
	probeRequest(t, owner, &pb.RequestMessage{Payload: &pb.RequestMessage_RecallMessage{RecallMessage: &pb.RecallMessage{MessageId: third}}}, func(r *pb.ResponseMessage) bool {
		return r.GetMessageRecallEvent() != nil && r.GetMessageRecallEvent().MessageId == third
	})
	if page := probeChannelHistory(t, owner, id, 0, 1); !page.Posts[0].IsRecalled || page.Posts[0].Content != "" {
		t.Fatalf("channel deletion tombstone %v", page)
	}
	if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Unsubscribe{Unsubscribe: &channel.UnsubscribeChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_INVALID_STATE {
		t.Fatalf("owner leave %v", r)
	}
	if r := probeChannel(t, outsider, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Unsubscribe{Unsubscribe: &channel.UnsubscribeChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	if r := probeChannelHistory(t, outsider, id, 0, 50); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
		t.Fatalf("private after leave %v", r)
	}
	syncPage := probeSync(t, subscriber, &pb.QuerySyncMessages{Timestamp: "2000-01-01T00:00:00Z", PageSize: 50})
	found := false
	for _, m := range syncPage.Msgs {
		if m.MessageId == first {
			found = true
		}
	}
	if !found {
		t.Fatal("channel pre-join posts missing from sync")
	}
	// Real PostgreSQL concurrent transfer: one winner and exactly one owner.
	if r := probeChannel(t, outsider, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Subscribe{Subscribe: &channel.SubscribeChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
		t.Fatal(r)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := db.TransferGroupOwnerWithDB(database, owner.userID, id, subscriber.userID)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, db.ErrRelationshipForbidden) && !errors.Is(err, db.ErrRelationshipInvalidState) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent owner transfers winners=%d", wins)
	}
	var owners int64
	if err := database.Model(&db.GroupMember{}).Where("group_id = ? AND role = ?", id, "owner").Count(&owners).Error; err != nil || owners != 1 {
		t.Fatalf("owner invariant %d %v", owners, err)
	}
	// Transfer back for explicit owner-only deletion and cleanup.
	if _, _, _, err := db.TransferGroupOwnerWithDB(database, subscriber.userID, id, owner.userID); err != nil {
		t.Fatal(err)
	}
	if r := probeChannel(t, owner, &channel.ChannelRequest{Payload: &channel.ChannelRequest_Delete{Delete: &channel.DeleteChannel{ChannelId: id}}}); r.Result != channel.ChannelResult_CHANNEL_OK {
		t.Fatal(r)
	}
	var retained int64
	if err := database.Model(&db.Message{}).Where("to_user_id = ? AND is_group = TRUE", id).Count(&retained).Error; err != nil || retained != 3 {
		t.Fatalf("delete lost posts %d %v", retained, err)
	}
	if r := probeChannelHistory(t, owner, id, 0, 50); r.Result != channel.ChannelResult_CHANNEL_NOT_FOUND {
		t.Fatalf("deleted channel visible %v", r)
	}
	t.Log("PASS channels: public discovery/preview, idempotent subscription, read-only enforcement, cross-pod publication, full history/pagination, PATCH, private invitations, admin deletion, unsubscribe, sync, concurrent ownership and retained tombstones")
}
