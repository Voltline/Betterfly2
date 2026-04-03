package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"testing"
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
	if queryPayload.QuerySyncMessages.GetToUserId() != 2002 {
		t.Fatalf("expected sync query target 2002, got %d", queryPayload.QuerySyncMessages.GetToUserId())
	}
	if queryPayload.QuerySyncMessages.GetTimestamp() != "2026-03-27T08:00:00Z" {
		t.Fatalf("unexpected timestamp: %q", queryPayload.QuerySyncMessages.GetTimestamp())
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
	req := buildInsertGroupUserFriendRequest(1001, 3003, "df-pod-1")

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
