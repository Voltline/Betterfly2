package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestBuildSyncMessagesStorageRequestRoutesResponseToRequester(t *testing.T) {
	payload := &pb.QuerySyncMessages{
		ToUserId:  2002,
		Timestamp: "2026-03-27T08:00:00Z",
	}

	storeReq := buildSyncMessagesStorageRequest(1001, payload, "df-pod-1")

	if storeReq.FromKafkaTopic != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", storeReq.FromKafkaTopic)
	}
	if storeReq.TargetUserId != 1001 {
		t.Fatalf("expected TargetUserId to be requester 1001, got %d", storeReq.TargetUserId)
	}

	queryPayload, ok := storeReq.Payload.(*storage.RequestMessage_QuerySyncMessages)
	if !ok {
		t.Fatalf("expected QuerySyncMessages payload, got %T", storeReq.Payload)
	}
	if queryPayload.QuerySyncMessages.GetToUserId() != 1001 {
		t.Fatalf("expected sync query target to be forced to requester 1001, got %d", queryPayload.QuerySyncMessages.GetToUserId())
	}
	if queryPayload.QuerySyncMessages.GetTimestamp() != "2026-03-27T08:00:00Z" {
		t.Fatalf("unexpected timestamp: %q", queryPayload.QuerySyncMessages.GetTimestamp())
	}
}

func TestBuildDirectMessageRecallPushUsesSenderConversation(t *testing.T) {
	event := &pb.MessageRecallEvent{MessageId: 78, FromUserId: 1001, ToUserId: 1002, OperatorUserId: 1001}
	request := buildMessageRecallPushRequest([]int64{1002}, event).GetMessageRecall()
	if request.GetConversationId() != 1001 || request.GetIsGroup() {
		t.Fatalf("unexpected direct recall conversation: %+v", request)
	}
}

func TestBuildStoreNewMessageStorageRequestRoutesAckToSender(t *testing.T) {
	post := &pb.Post{
		FromId:          1001,
		ToId:            2002,
		Msg:             "hello",
		MsgType:         "text",
		IsGroup:         false,
		RealFileName:    "",
		Timestamp:       "2026-07-12T09:00:00Z",
		ClientMessageId: "client-42",
	}

	storeReq := buildStoreNewMessageStorageRequest(post, "df-pod-1")

	if storeReq.FromKafkaTopic != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", storeReq.FromKafkaTopic)
	}
	if storeReq.TargetUserId != 1001 {
		t.Fatalf("expected StoreMsgRsp target to be sender 1001, got %d", storeReq.TargetUserId)
	}

	storePayload, ok := storeReq.Payload.(*storage.RequestMessage_StoreNewMessage)
	if !ok {
		t.Fatalf("expected StoreNewMessage payload, got %T", storeReq.Payload)
	}
	if storePayload.StoreNewMessage.GetToUserId() != 2002 {
		t.Fatalf("expected stored message target 2002, got %d", storePayload.StoreNewMessage.GetToUserId())
	}
	if storePayload.StoreNewMessage.GetClientMessageId() != "client-42" || storePayload.StoreNewMessage.GetClientTimestamp() != post.GetTimestamp() {
		t.Fatalf("message correlation fields were not forwarded: %+v", storePayload.StoreNewMessage)
	}
}

func TestBuildRecallMessageStorageRequestUsesAuthenticatedIdentity(t *testing.T) {
	request := buildRecallMessageStorageRequest(1001, 77, "df-pod-1")
	if request.GetFromKafkaTopic() != "df-pod-1" || request.GetTargetUserId() != 1001 {
		t.Fatalf("unexpected recall routing: %+v", request)
	}
	payload := request.GetRecallMessage()
	if payload == nil || payload.GetMessageId() != 77 {
		t.Fatalf("unexpected recall payload: %+v", payload)
	}
}

func TestRecallTargetsExcludeOperatorInvalidAndDuplicates(t *testing.T) {
	got := recallTargetsWithoutOperator([]int64{1001, 1002, 1002, 0, -1, 1003}, 1001)
	want := []int64{1002, 1003}
	if len(got) != len(want) {
		t.Fatalf("targets=%v want=%v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("targets=%v want=%v", got, want)
		}
	}
}

func TestBuildMessageRecallPushRequestPreservesConversationMetadata(t *testing.T) {
	event := &pb.MessageRecallEvent{
		Result: pb.MessageRecallResult_MESSAGE_RECALL_OK, MessageId: 77, ToUserId: 9001,
		IsGroup: true, OperatorUserId: 1001, RecalledAt: "2026-07-21T05:00:00Z",
	}
	request := buildMessageRecallPushRequest([]int64{1002, 1003}, event).GetMessageRecall()
	if request == nil || request.GetMessageId() != 77 || request.GetConversationId() != 9001 || !request.GetIsGroup() || request.GetOperatorUserId() != 1001 || request.GetRecalledAt() != event.GetRecalledAt() || len(request.GetTargetUserIds()) != 2 {
		t.Fatalf("unexpected recall push request: %+v", request)
	}
}

func TestBuildGroupPostBatchDeliveryEnvelope(t *testing.T) {
	post := &pb.Post{
		FromId:  1001,
		ToId:    9001,
		Msg:     "hello group",
		MsgType: "text",
		IsGroup: true,
	}

	envBytes, err := buildGroupPostDeliveryEnvelopeBytes([]int64{2002, 2003}, post)
	if err != nil {
		t.Fatalf("buildGroupPostDeliveryEnvelopeBytes failed: %v", err)
	}

	env := &envelope.Envelope{}
	if err := proto.Unmarshal(envBytes, env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if env.GetType() != envelope.MessageType_DF_RESPONSE {
		t.Fatalf("expected DF_RESPONSE, got %v", env.GetType())
	}

	internalDelivery := &pb.DFInternalDelivery{}
	if err := proto.Unmarshal(env.GetPayload(), internalDelivery); err != nil {
		t.Fatalf("failed to unmarshal internal delivery: %v", err)
	}
	batch := internalDelivery.GetGroupPostBatchDelivery()
	if batch == nil {
		t.Fatalf("expected GroupPostBatchDelivery, got %T", internalDelivery.Payload)
	}
	if len(batch.GetTargetUserIds()) != 2 {
		t.Fatalf("expected 2 target users, got %d", len(batch.GetTargetUserIds()))
	}
	if batch.GetPost().GetMsg() != "hello group" {
		t.Fatalf("unexpected post msg: %s", batch.GetPost().GetMsg())
	}
}

func TestValidatePostPayloadForFileMessages(t *testing.T) {
	tests := []struct {
		name    string
		post    *pb.Post
		wantErr bool
	}{
		{
			name: "valid file message",
			post: &pb.Post{
				Msg:          "sha512-hash",
				MsgType:      "file",
				RealFileName: "report.pdf",
			},
		},
		{
			name: "missing hash",
			post: &pb.Post{
				MsgType:      "file",
				RealFileName: "report.pdf",
			},
			wantErr: true,
		},
		{
			name: "missing real file name",
			post: &pb.Post{
				Msg:     "sha512-hash",
				MsgType: "file",
			},
			wantErr: true,
		},
		{
			name: "non file message clears real file name",
			post: &pb.Post{
				Msg:          "hello",
				MsgType:      "text",
				RealFileName: "ignored.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePostPayload(tt.post)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation to fail")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected validation to pass, got %v", err)
			}
			if tt.post.GetMsgType() != "file" && tt.post.GetRealFileName() != "" {
				t.Fatalf("expected non-file message to clear real_file_name, got %q", tt.post.GetRealFileName())
			}
		})
	}
}

func TestBuildQueryContactsFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildQueryContactsFriendRequest(1001, "df-pod-1")

	if req.GetFromKafkaTopic() != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", req.GetFromKafkaTopic())
	}
	if req.GetTargetUserId() != 1001 {
		t.Fatalf("expected TargetUserId to be requester 1001, got %d", req.GetTargetUserId())
	}

	payload, ok := req.Payload.(*friend.RequestMessage_QueryFriendList)
	if !ok {
		t.Fatalf("expected QueryFriendList payload, got %T", req.Payload)
	}
	if payload.QueryFriendList.GetUserId() != 1001 {
		t.Fatalf("expected friend list query user 1001, got %d", payload.QueryFriendList.GetUserId())
	}
}

func TestBuildDeleteContactFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildDeleteContactFriendRequest(1001, 2002, "df-pod-1")

	if req.GetFromKafkaTopic() != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", req.GetFromKafkaTopic())
	}
	if req.GetTargetUserId() != 1001 {
		t.Fatalf("expected TargetUserId to be requester 1001, got %d", req.GetTargetUserId())
	}

	payload, ok := req.Payload.(*friend.RequestMessage_RemoveDirectFriend)
	if !ok {
		t.Fatalf("expected RemoveDirectFriend payload, got %T", req.Payload)
	}
	if payload.RemoveDirectFriend.GetUserId() != 1001 || payload.RemoveDirectFriend.GetFriendId() != 2002 {
		t.Fatalf("unexpected delete contact payload: user=%d friend=%d", payload.RemoveDirectFriend.GetUserId(), payload.RemoveDirectFriend.GetFriendId())
	}
}

func TestBuildUpdateContactAliasFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildUpdateContactAliasFriendRequest(1001, 2002, "bestie", "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_UpdateFriendAlias)
	if !ok {
		t.Fatalf("expected UpdateFriendAlias payload, got %T", req.Payload)
	}
	if payload.UpdateFriendAlias.GetUserId() != 1001 || payload.UpdateFriendAlias.GetFriendId() != 2002 {
		t.Fatalf("unexpected alias payload user=%d friend=%d", payload.UpdateFriendAlias.GetUserId(), payload.UpdateFriendAlias.GetFriendId())
	}
	if payload.UpdateFriendAlias.GetAlias() != "bestie" {
		t.Fatalf("expected alias bestie, got %q", payload.UpdateFriendAlias.GetAlias())
	}
}

func TestBuildUpdateContactNotifyFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildUpdateContactNotifyFriendRequest(1001, 2002, true, "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_UpdateFriendNotify)
	if !ok {
		t.Fatalf("expected UpdateFriendNotify payload, got %T", req.Payload)
	}
	if payload.UpdateFriendNotify.GetUserId() != 1001 || payload.UpdateFriendNotify.GetFriendId() != 2002 {
		t.Fatalf("unexpected notify payload user=%d friend=%d", payload.UpdateFriendNotify.GetUserId(), payload.UpdateFriendNotify.GetFriendId())
	}
	if !payload.UpdateFriendNotify.GetIsNotify() {
		t.Fatal("expected is_notify=true")
	}
}

func TestBuildQueryGroupFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildQueryGroupFriendRequest(1001, 3003, true, "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_QueryGroup)
	if !ok {
		t.Fatalf("expected QueryGroup payload, got %T", req.Payload)
	}
	if payload.QueryGroup.GetRequestUserId() != 1001 || payload.QueryGroup.GetGroupId() != 3003 {
		t.Fatalf("unexpected query group payload user=%d group=%d", payload.QueryGroup.GetRequestUserId(), payload.QueryGroup.GetGroupId())
	}
	if !payload.QueryGroup.GetClientNeedSave() {
		t.Fatal("expected client_need_save=true")
	}
}

func TestBuildInsertGroupFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildInsertGroupFriendRequest(1001, 3003, "dev-team", "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_CreateGroup)
	if !ok {
		t.Fatalf("expected CreateGroup payload, got %T", req.Payload)
	}
	if payload.CreateGroup.GetOwnerUserId() != 1001 || payload.CreateGroup.GetGroupId() != 3003 {
		t.Fatalf("unexpected create group payload owner=%d group=%d", payload.CreateGroup.GetOwnerUserId(), payload.CreateGroup.GetGroupId())
	}
	if payload.CreateGroup.GetGroupName() != "dev-team" {
		t.Fatalf("expected group name dev-team, got %q", payload.CreateGroup.GetGroupName())
	}
}

func TestBuildInsertGroupUserFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildInsertGroupUserFriendRequestWithMessage(1001, 3003, "", "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_AddGroupMember)
	if !ok {
		t.Fatalf("expected AddGroupMember payload, got %T", req.Payload)
	}
	if payload.AddGroupMember.GetUserId() != 1001 || payload.AddGroupMember.GetGroupId() != 3003 {
		t.Fatalf("unexpected add group member payload user=%d group=%d", payload.AddGroupMember.GetUserId(), payload.AddGroupMember.GetGroupId())
	}
}

func TestBuildUpdateGroupAvatarFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildUpdateGroupAvatarFriendRequest(1001, 3003, "avatar-hash", "df-pod-1")

	if req.GetFromKafkaTopic() != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", req.GetFromKafkaTopic())
	}
	if req.GetTargetUserId() != 1001 {
		t.Fatalf("expected TargetUserId to be requester 1001, got %d", req.GetTargetUserId())
	}

	payload, ok := req.Payload.(*friend.RequestMessage_UpdateGroupAvatar)
	if !ok {
		t.Fatalf("expected UpdateGroupAvatar payload, got %T", req.Payload)
	}
	if payload.UpdateGroupAvatar.GetRequestUserId() != 1001 || payload.UpdateGroupAvatar.GetGroupId() != 3003 {
		t.Fatalf("unexpected update group avatar payload user=%d group=%d", payload.UpdateGroupAvatar.GetRequestUserId(), payload.UpdateGroupAvatar.GetGroupId())
	}
	if payload.UpdateGroupAvatar.GetAvatarHash() != "avatar-hash" {
		t.Fatalf("expected avatar hash avatar-hash, got %q", payload.UpdateGroupAvatar.GetAvatarHash())
	}
}

func TestBuildQueryGroupMembersFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildQueryGroupMembersFriendRequest(1001, 3003, "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_QueryGroupMembers)
	if !ok {
		t.Fatalf("expected QueryGroupMembers payload, got %T", req.Payload)
	}
	if payload.QueryGroupMembers.GetRequestUserId() != 1001 || payload.QueryGroupMembers.GetGroupId() != 3003 {
		t.Fatalf("unexpected query group members payload user=%d group=%d", payload.QueryGroupMembers.GetRequestUserId(), payload.QueryGroupMembers.GetGroupId())
	}
}

func TestBuildQueryJoinedGroupsFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildQueryJoinedGroupsFriendRequest(1001, "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_QueryJoinedGroups)
	if !ok {
		t.Fatalf("expected QueryJoinedGroups payload, got %T", req.Payload)
	}
	if req.FromKafkaTopic != "df-pod-1" || req.TargetUserId != 1001 {
		t.Fatalf("unexpected routing metadata topic=%s target=%d", req.FromKafkaTopic, req.TargetUserId)
	}
	if payload.QueryJoinedGroups.GetUserId() != 1001 {
		t.Fatalf("unexpected query joined groups payload user=%d", payload.QueryJoinedGroups.GetUserId())
	}
}

func TestBuildDeleteGroupUserFriendRequestRoutesResponseToRequester(t *testing.T) {
	req := buildDeleteGroupUserFriendRequest(1001, 3003, "df-pod-1")

	payload, ok := req.Payload.(*friend.RequestMessage_RemoveGroupMember)
	if !ok {
		t.Fatalf("expected RemoveGroupMember payload, got %T", req.Payload)
	}
	if payload.RemoveGroupMember.GetRequestUserId() != 1001 || payload.RemoveGroupMember.GetGroupId() != 3003 || payload.RemoveGroupMember.GetUserId() != 1001 {
		t.Fatalf(
			"unexpected remove group member payload request_user=%d group=%d user=%d",
			payload.RemoveGroupMember.GetRequestUserId(),
			payload.RemoveGroupMember.GetGroupId(),
			payload.RemoveGroupMember.GetUserId(),
		)
	}
}

func TestLegacyInsertAPIsNowCreateRequestsWithVerificationMessage(t *testing.T) {
	friendReq := buildInsertContactFriendRequestWithMessage(1001, 1002, "我是 Alice", "df-pod-1")
	friendPayload := friendReq.GetAddDirectFriend()
	if friendPayload == nil || friendPayload.GetUserId() != 1001 || friendPayload.GetFriendId() != 1002 || friendPayload.GetMessage() != "我是 Alice" {
		t.Fatalf("friend request bridge mismatch: %+v", friendReq)
	}

	groupReq := buildInsertGroupUserFriendRequestWithMessage(1001, 3001, "申请入群", "df-pod-1")
	groupPayload := groupReq.GetAddGroupMember()
	if groupPayload == nil || groupPayload.GetUserId() != 1001 || groupPayload.GetGroupId() != 3001 || groupPayload.GetMessage() != "申请入群" {
		t.Fatalf("group join request bridge mismatch: %+v", groupReq)
	}
}

func TestFriendDecisionMappingRejectsUnspecified(t *testing.T) {
	if got, err := friendDecision(pb.RequestDecision_REQUEST_ACCEPT); err != nil || got != friend.RequestDecision_REQUEST_ACCEPT {
		t.Fatalf("accept decision mapping failed: got=%s err=%v", got, err)
	}
	if _, err := friendDecision(pb.RequestDecision_REQUEST_DECISION_UNSPECIFIED); err == nil {
		t.Fatal("unspecified decision must be rejected")
	}
}

func TestBuildGroupManagementRequestsUseAuthenticatedActor(t *testing.T) {
	rename := buildUpdateGroupNameFriendRequest(1001, 3003, "New Team", "df-pod-1")
	renamePayload := rename.GetUpdateGroupName()
	if rename.GetTargetUserId() != 1001 || rename.GetFromKafkaTopic() != "df-pod-1" || renamePayload.GetRequestUserId() != 1001 || renamePayload.GetGroupId() != 3003 || renamePayload.GetGroupName() != "New Team" {
		t.Fatalf("rename request bridge mismatch: %+v", rename)
	}

	transfer := buildTransferGroupOwnerFriendRequest(1001, 3003, 2002, "df-pod-1")
	transferPayload := transfer.GetTransferGroupOwner()
	if transfer.GetTargetUserId() != 1001 || transferPayload.GetRequestUserId() != 1001 || transferPayload.GetGroupId() != 3003 || transferPayload.GetUserId() != 2002 {
		t.Fatalf("owner transfer request bridge mismatch: %+v", transfer)
	}
}
