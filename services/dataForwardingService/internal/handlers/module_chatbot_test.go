package handlers

import (
	"errors"
	"testing"

	chatbotpb "Betterfly2/proto/chatbot"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	sharedDB "Betterfly2/shared/db"

	"google.golang.org/protobuf/proto"
)

const (
	testChatbotID = int64(900000000000000002)
	testOtherBot  = int64(900000000000000105)
)

func withTestChatbot(t *testing.T) {
	t.Helper()
	previous := chatbot
	chatbot = chatbotConfig{enabled: true, topic: "chatbot-service"}
	t.Cleanup(func() { chatbot = previous })
}

func TestLoadChatbotConfig(t *testing.T) {
	t.Setenv("CHATBOT_ENABLED", "")
	if cfg := loadChatbotConfig(); cfg.enabled {
		t.Fatalf("unset flag enabled chatbot: %+v", cfg)
	}
	t.Setenv("CHATBOT_ENABLED", "true")
	t.Setenv("CHATBOT_TOPIC", "bots")
	if cfg := loadChatbotConfig(); !cfg.enabled || cfg.topic != "bots" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestChatbotDisabledByDefault(t *testing.T) {
	previous := chatbot
	chatbot = chatbotConfig{}
	t.Cleanup(func() { chatbot = previous })
	if IsChatbotID(0) || IsChatbotID(testChatbotID) {
		t.Fatal("disabled chatbot matched an id")
	}
	members := []int64{1, testChatbotID}
	if rest, bots := withoutChatbot(members); bots != nil || len(rest) != 2 {
		t.Fatalf("disabled chatbot filtered members: %v", rest)
	}
}

func TestWithoutChatbotSplitsBotMembers(t *testing.T) {
	withTestChatbot(t)
	members := []int64{1, testChatbotID, 3, testOtherBot, 900000000000000001}
	rest, bots := withoutChatbot(members)
	if len(rest) != 3 || rest[0] != 1 || rest[1] != 3 || rest[2] != 900000000000000001 || len(bots) != 2 || bots[1] != testOtherBot || members[1] != testChatbotID {
		t.Fatalf("rest=%v bots=%v members=%v", rest, bots, members)
	}
	if rest, bots := withoutChatbot([]int64{1, 2}); bots != nil || len(rest) != 2 {
		t.Fatalf("no bots: rest=%v bots=%v", rest, bots)
	}
}

func TestChatbotSeesOnlyMainTimelinePosts(t *testing.T) {
	withTestChatbot(t)
	cases := map[string]struct {
		post *pb.Post
		want bool
	}{
		"text":             {&pb.Post{FromId: 1, MsgType: "text", Msg: "大家好"}, true},
		"image":            {&pb.Post{FromId: 1, MsgType: "image"}, true},
		"from bot":         {&pb.Post{FromId: testOtherBot, MsgType: "text", Msg: "@Betterfly 助手"}, false},
		"discussion reply": {&pb.Post{FromId: 1, MsgType: "text", Msg: "@Betterfly 助手", DiscussionRootMessageId: 9}, false},
		"channel card":     {&pb.Post{FromId: 1, MsgType: "text", Msg: "@Betterfly 助手", SourceChannelMessageId: 9}, false},
	}
	for name, tc := range cases {
		if got := chatbotSeesGroupPost(tc.post); got != tc.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

func decodeChatbotRequest(t *testing.T, encoded []byte) *chatbotpb.RequestMessage {
	t.Helper()
	var env envelope.Envelope
	var request chatbotpb.RequestMessage
	if err := proto.Unmarshal(encoded, &env); err != nil || env.GetType() != envelope.MessageType_CHATBOT_REQUEST {
		t.Fatalf("envelope = %v %v", env.GetType(), err)
	}
	if err := proto.Unmarshal(env.GetPayload(), &request); err != nil {
		t.Fatal(err)
	}
	return &request
}

func TestBuildChatbotTriggerKeysByConversation(t *testing.T) {
	encoded, key, err := buildChatbotTrigger(88, &pb.Post{FromId: 1, ToId: 500, IsGroup: true}, []int64{testChatbotID, testOtherBot})
	if err != nil || key != "g:500" {
		t.Fatalf("key=%s err=%v", key, err)
	}
	trigger := decodeChatbotRequest(t, encoded).GetTrigger()
	if trigger.GetMessageId() != 88 || trigger.GetFromUserId() != 1 || trigger.GetToId() != 500 || !trigger.GetIsGroup() || len(trigger.GetBotUserIds()) != 2 {
		t.Fatalf("trigger = %+v", trigger)
	}
	if _, key, _ := buildChatbotTrigger(1, &pb.Post{FromId: 3, ToId: testChatbotID}, nil); key != "u:3" {
		t.Fatalf("direct key = %s", key)
	}
}

func TestDeliverStoredPostToChatbotOnlyPublishesTrigger(t *testing.T) {
	withTestChatbot(t)
	previous := publishChatbotMessage
	t.Cleanup(func() { publishChatbotMessage = previous })
	var topic, key string
	publishChatbotMessage = func(_ []byte, targetTopic, partitionKey string) error {
		topic, key = targetTopic, partitionKey
		return nil
	}
	post := &pb.Post{FromId: 3, ToId: testOtherBot, Msg: "你好", MsgType: "text"}
	if err := DeliverStoredPost(77, post); err != nil {
		t.Fatal(err)
	}
	if topic != "chatbot-service" || key != "u:3" {
		t.Fatalf("published topic=%s key=%s", topic, key)
	}

	publishChatbotMessage = func([]byte, string, string) error { return errors.New("kafka down") }
	if err := DeliverStoredPost(78, post); err == nil {
		t.Fatal("trigger publish failure must be retried")
	}
}

func chatbotRelationshipResponse(target int64, operation string, result friend.FriendResult, request *friend.RelationshipRequestInfo) *friend.ResponseMessage {
	return &friend.ResponseMessage{Result: result, TargetUserId: target, Payload: &friend.ResponseMessage_RelationshipOperationRsp{
		RelationshipOperationRsp: &friend.RelationshipOperationRsp{Operation: operation, Request: request},
	}}
}

func TestChatbotPendingRequestSelectsRequestsToBot(t *testing.T) {
	withTestChatbot(t)
	friendRequest := &friend.RelationshipRequestInfo{RequestId: 5, RequestType: sharedDB.RequestTypeFriend, RequesterUserId: 3, TargetUserId: testChatbotID, Status: sharedDB.RequestStatusPending}
	invitation := &friend.RelationshipRequestInfo{RequestId: 6, RequestType: sharedDB.RequestTypeGroupInvite, RequesterUserId: 3, TargetUserId: testChatbotID, GroupId: 500, Status: sharedDB.RequestStatusPending}
	accepted := proto.Clone(friendRequest).(*friend.RelationshipRequestInfo)
	accepted.Status = sharedDB.RequestStatusAccepted
	toHuman := proto.Clone(friendRequest).(*friend.RelationshipRequestInfo)
	toHuman.TargetUserId = 4
	cases := map[string]struct {
		resp *friend.ResponseMessage
		want int64
	}{
		"friend request":   {chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest), 5},
		"resubmitted":      {chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_REQUEST_PENDING, friendRequest), 5},
		"group invitation": {chatbotRelationshipResponse(3, "invite_group_member", friend.FriendResult_FRIEND_OK, invitation), 6},
		"already resolved": {chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, accepted), 0},
		"to human":         {chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, toHuman), 0},
		"failed":           {chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_SERVICE_ERROR, friendRequest), 0},
		"bot's own copy":   {chatbotRelationshipResponse(testChatbotID, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest), 0},
		"type mismatch":    {chatbotRelationshipResponse(3, "invite_group_member", friend.FriendResult_FRIEND_OK, friendRequest), 0},
		"other operation":  {chatbotRelationshipResponse(3, "resolve_friend_request", friend.FriendResult_FRIEND_OK, friendRequest), 0},
	}
	for name, tc := range cases {
		if got := chatbotPendingRequest(tc.resp).GetRequestId(); got != tc.want {
			t.Errorf("%s: request id = %d, want %d", name, got, tc.want)
		}
	}
}

func TestChatbotRelationshipEventForwardsRequestsAndChanges(t *testing.T) {
	withTestChatbot(t)
	invitation := &friend.RelationshipRequestInfo{RequestId: 6, RequestType: sharedDB.RequestTypeGroupInvite, RequesterUserId: 3, TargetUserId: testOtherBot, GroupId: 500, Status: sharedDB.RequestStatusPending}
	if event := chatbotRelationshipEvent(chatbotRelationshipResponse(3, "invite_group_member", friend.FriendResult_FRIEND_OK, invitation)); event.GetType() != BotEventRelationshipRequest ||
		event.GetBotUserId() != testOtherBot || event.GetUserId() != 3 || event.GetGroupId() != 500 || event.GetRequestId() != 6 || event.GetRequestType() != sharedDB.RequestTypeGroupInvite {
		t.Fatalf("request event = %+v", event)
	}

	accepted := proto.Clone(invitation).(*friend.RelationshipRequestInfo)
	accepted.Status = sharedDB.RequestStatusAccepted
	changed := func(target int64, operation string, request *friend.RelationshipRequestInfo) *friend.ResponseMessage {
		return &friend.ResponseMessage{TargetUserId: target, Payload: &friend.ResponseMessage_RelationshipChangedEvent{
			RelationshipChangedEvent: &friend.RelationshipChangedEvent{Request: request, Operation: operation, ChangedAt: "2026-10-31T00:00:00Z"},
		}}
	}
	if event := chatbotRelationshipEvent(changed(3, "resolved", accepted)); event.GetType() != BotEventGroupJoined || event.GetGroupId() != 500 || event.GetOccurredAt() != "2026-10-31T00:00:00Z" {
		t.Fatalf("joined event = %+v", event)
	}
	acceptedFriend := proto.Clone(accepted).(*friend.RelationshipRequestInfo)
	acceptedFriend.RequestType, acceptedFriend.GroupId = sharedDB.RequestTypeFriend, 0
	if event := chatbotRelationshipEvent(changed(3, "resolved", acceptedFriend)); event.GetType() != BotEventFriendAdded || event.GetUserId() != 3 {
		t.Fatalf("friend event = %+v", event)
	}
	rejected := proto.Clone(accepted).(*friend.RelationshipRequestInfo)
	rejected.Status = sharedDB.RequestStatusRejected
	for name, resp := range map[string]*friend.ResponseMessage{
		"rejected":       changed(3, "resolved", rejected),
		"created":        changed(3, "created", accepted),
		"other receiver": changed(4, "resolved", accepted),
		"bot copy":       changed(testOtherBot, "resolved", accepted),
	} {
		if event := chatbotRelationshipEvent(resp); event != nil {
			t.Errorf("%s: event = %+v", name, event)
		}
	}

	removed := &friend.ResponseMessage{Result: friend.FriendResult_FRIEND_OK, TargetUserId: 3, Payload: &friend.ResponseMessage_FriendOperationRsp{
		FriendOperationRsp: &friend.FriendOperationRsp{Operation: "remove_direct_friend", UserId: 3, FriendId: testChatbotID},
	}}
	if event := chatbotRelationshipEvent(removed); event.GetType() != BotEventFriendRemoved || event.GetBotUserId() != testChatbotID || event.GetUserId() != 3 {
		t.Fatalf("removed event = %+v", event)
	}
	removed.Result = friend.FriendResult_RECORD_NOT_EXIST
	if event := chatbotRelationshipEvent(removed); event != nil {
		t.Fatalf("failed removal produced %+v", event)
	}
	kicked := &friend.ResponseMessage{Result: friend.FriendResult_FRIEND_OK, TargetUserId: 3, Payload: &friend.ResponseMessage_GroupOperationRsp{
		GroupOperationRsp: &friend.GroupOperationRsp{Operation: "kick_group_member", GroupId: 500, UserId: testOtherBot},
	}}
	if event := chatbotRelationshipEvent(kicked); event.GetType() != BotEventGroupRemoved || event.GetGroupId() != 500 || event.GetBotUserId() != testOtherBot {
		t.Fatalf("kicked event = %+v", event)
	}
	kicked.GetGroupOperationRsp().UserId = 4
	if event := chatbotRelationshipEvent(kicked); event != nil {
		t.Fatalf("kicking a human produced %+v", event)
	}
}

func TestForwardChatbotRelationshipEventPublishesKeyedByBot(t *testing.T) {
	withTestChatbot(t)
	previous := publishChatbotMessage
	t.Cleanup(func() { publishChatbotMessage = previous })
	var topic, key string
	var request *chatbotpb.RequestMessage
	publishChatbotMessage = func(encoded []byte, targetTopic, partitionKey string) error {
		topic, key, request = targetTopic, partitionKey, decodeChatbotRequest(t, encoded)
		return nil
	}
	friendRequest := &friend.RelationshipRequestInfo{RequestId: 5, RequestType: sharedDB.RequestTypeFriend, RequesterUserId: 3, TargetUserId: testChatbotID, Status: sharedDB.RequestStatusPending}
	if err := ForwardChatbotRelationshipEvent(chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest)); err != nil {
		t.Fatal(err)
	}
	if event := request.GetEvent(); topic != "chatbot-service" || key != "b:900000000000000002" || event.GetRequestId() != 5 || event.GetOccurredAt() == "" {
		t.Fatalf("topic=%s key=%s event=%+v", topic, key, event)
	}
	request = nil
	if err := ForwardChatbotRelationshipEvent(chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_ALREADY_FRIEND, nil)); err != nil || request != nil {
		t.Fatalf("unrelated response published %v %v", request, err)
	}
	publishChatbotMessage = func([]byte, string, string) error { return errors.New("kafka down") }
	if err := ForwardChatbotRelationshipEvent(chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest)); err == nil {
		t.Fatal("publish failure must be retried")
	}
}
