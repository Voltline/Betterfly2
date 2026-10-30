package handlers

import (
	"errors"
	"testing"

	chatbotpb "Betterfly2/proto/chatbot"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	sharedDB "Betterfly2/shared/db"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
)

const testChatbotID = int64(900000000000000002)

func withTestChatbot(t *testing.T) {
	t.Helper()
	previous := chatbot
	chatbot = chatbotConfig{userID: testChatbotID, mention: normalizeMention("@Betterfly 助手"), topic: "chatbot-service"}
	t.Cleanup(func() { chatbot = previous })
}

func TestLoadChatbotConfig(t *testing.T) {
	t.Setenv("CHATBOT_USER_ID", "")
	if cfg := loadChatbotConfig(); cfg.userID != 0 {
		t.Fatalf("unset id enabled chatbot: %+v", cfg)
	}
	for _, invalid := range []string{"abc", "1", "900000000000000001"} {
		t.Setenv("CHATBOT_USER_ID", invalid)
		if cfg := loadChatbotConfig(); cfg.userID != 0 {
			t.Fatalf("CHATBOT_USER_ID=%s enabled chatbot", invalid)
		}
	}
	t.Setenv("CHATBOT_USER_ID", "900000000000000002")
	t.Setenv("CHATBOT_NAME", "小 蝶")
	cfg := loadChatbotConfig()
	if cfg.userID != testChatbotID || cfg.mention != "@小蝶" || cfg.topic != defaultChatbotTopic {
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
	if rest, found := withoutChatbot(members); found || len(rest) != 2 {
		t.Fatalf("disabled chatbot filtered members: %v", rest)
	}
}

func TestWithoutChatbotRemovesBotMember(t *testing.T) {
	withTestChatbot(t)
	members := []int64{1, testChatbotID, 3}
	rest, found := withoutChatbot(members)
	if !found || len(rest) != 2 || rest[0] != 1 || rest[1] != 3 || members[1] != testChatbotID {
		t.Fatalf("rest=%v found=%v members=%v", rest, found, members)
	}
}

func TestChatbotRepliesInGroupOnMention(t *testing.T) {
	withTestChatbot(t)
	cases := map[string]struct {
		post *pb.Post
		want bool
	}{
		"mention":          {&pb.Post{FromId: 1, MsgType: "text", Msg: "@betterfly助手 帮我看看"}, true},
		"mention spacing":  {&pb.Post{FromId: 1, MsgType: "text", Msg: "问下 @Betterfly  助手"}, true},
		"no mention":       {&pb.Post{FromId: 1, MsgType: "text", Msg: "大家好"}, false},
		"image":            {&pb.Post{FromId: 1, MsgType: "image", Msg: "@Betterfly 助手"}, false},
		"from bot":         {&pb.Post{FromId: testChatbotID, MsgType: "text", Msg: "@Betterfly 助手"}, false},
		"discussion reply": {&pb.Post{FromId: 1, MsgType: "text", Msg: "@Betterfly 助手", DiscussionRootMessageId: 9}, false},
		"channel card":     {&pb.Post{FromId: 1, MsgType: "text", Msg: "@Betterfly 助手", SourceChannelMessageId: 9}, false},
	}
	for name, tc := range cases {
		if got, err := chatbotRepliesInGroup(tc.post); err != nil || got != tc.want {
			t.Errorf("%s: got %v err %v", name, got, err)
		}
	}
}

func TestChatbotRepliesInGroupWhenQuotingBot(t *testing.T) {
	withTestChatbot(t)
	mock := captionDeliveryDatabase(t)
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id"}).AddRow(40, testChatbotID))
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id"}).AddRow(41, 7))
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnError(errors.New("db down"))
	post := &pb.Post{FromId: 1, MsgType: "text", Msg: "那后来呢", ReplyToMessageId: 40}
	if got, err := chatbotRepliesInGroup(post); err != nil || !got {
		t.Fatalf("quote bot = %v, %v", got, err)
	}
	post.ReplyToMessageId = 41
	if got, err := chatbotRepliesInGroup(post); err != nil || got {
		t.Fatalf("quote user = %v, %v", got, err)
	}
	if _, err := chatbotRepliesInGroup(post); err == nil {
		t.Fatal("db error must be returned for retry")
	}
}

func TestBuildChatbotTriggerKeysByConversation(t *testing.T) {
	encoded, key, err := buildChatbotTrigger(88, &pb.Post{FromId: 1, ToId: 500, IsGroup: true}, "df-1")
	if err != nil || key != "g:500" {
		t.Fatalf("key=%s err=%v", key, err)
	}
	var env envelope.Envelope
	var request chatbotpb.RequestMessage
	if err := proto.Unmarshal(encoded, &env); err != nil || env.GetType() != envelope.MessageType_CHATBOT_REQUEST {
		t.Fatalf("envelope = %v %v", env.GetType(), err)
	}
	if err := proto.Unmarshal(env.GetPayload(), &request); err != nil {
		t.Fatal(err)
	}
	trigger := request.GetTrigger()
	if trigger.GetMessageId() != 88 || trigger.GetFromUserId() != 1 || trigger.GetToId() != 500 || !trigger.GetIsGroup() || trigger.GetReplyTopic() != "df-1" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if _, key, _ := buildChatbotTrigger(1, &pb.Post{FromId: 3, ToId: testChatbotID}, "df"); key != "u:3" {
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
	post := &pb.Post{FromId: 3, ToId: testChatbotID, Msg: "你好", MsgType: "text"}
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

func TestAcceptChatbotRequestResolvesAsBot(t *testing.T) {
	withTestChatbot(t)
	previous := publishChatbotFriendRequest
	t.Cleanup(func() { publishChatbotFriendRequest = previous })
	var sent []*friend.RequestMessage
	publishChatbotFriendRequest = func(req *friend.RequestMessage) error {
		sent = append(sent, req)
		return nil
	}
	friendRequest := &friend.RelationshipRequestInfo{RequestId: 5, RequestType: sharedDB.RequestTypeFriend, RequesterUserId: 3, TargetUserId: testChatbotID, Status: sharedDB.RequestStatusPending}
	invitation := &friend.RelationshipRequestInfo{RequestId: 6, RequestType: sharedDB.RequestTypeGroupInvite, RequesterUserId: 3, TargetUserId: testChatbotID, GroupId: 500, Status: sharedDB.RequestStatusPending}
	for _, resp := range []*friend.ResponseMessage{
		chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest),
		chatbotRelationshipResponse(3, "invite_group_member", friend.FriendResult_FRIEND_OK, invitation),
		chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_ALREADY_FRIEND, nil),
	} {
		if err := AcceptChatbotRequest(resp); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d requests", len(sent))
	}
	if resolve := sent[0].GetResolveFriendRequest(); sent[0].GetTargetUserId() != testChatbotID || resolve.GetUserId() != testChatbotID ||
		resolve.GetRequestId() != 5 || resolve.GetDecision() != friend.RequestDecision_REQUEST_ACCEPT {
		t.Fatalf("friend accept = %+v", sent[0])
	}
	if resolve := sent[1].GetResolveGroupInvitation(); resolve.GetUserId() != testChatbotID || resolve.GetInvitationId() != 6 ||
		resolve.GetDecision() != friend.RequestDecision_REQUEST_ACCEPT {
		t.Fatalf("invitation accept = %+v", sent[1])
	}

	publishChatbotFriendRequest = func(*friend.RequestMessage) error { return errors.New("kafka down") }
	if err := AcceptChatbotRequest(chatbotRelationshipResponse(3, "create_friend_request", friend.FriendResult_FRIEND_OK, friendRequest)); err == nil {
		t.Fatal("publish failure must be retried")
	}
}
