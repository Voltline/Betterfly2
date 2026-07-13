package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestBuildPostAckResponse(t *testing.T) {
	resp := buildPostAckResponse(&storage.StoreMsgRsp{MessageId: 12345, ClientMessageId: "client-42"})

	if resp.GetPostAckRsp() == nil {
		t.Fatalf("expected PostAckRsp payload, got %T", resp.Payload)
	}
	if resp.GetPostAckRsp().GetMessageId() != 12345 {
		t.Fatalf("expected message_id 12345, got %d", resp.GetPostAckRsp().GetMessageId())
	}
	if resp.GetPostAckRsp().GetClientMessageId() != "client-42" {
		t.Fatalf("expected client_message_id client-42, got %q", resp.GetPostAckRsp().GetClientMessageId())
	}
}

func TestBuildFriendResponsesPreserveClientFields(t *testing.T) {
	t.Run("group info", func(t *testing.T) {
		resp := buildGroupInfoResponse(&friend.GroupInfoRsp{GroupId: 10, GroupName: "Team", Avatar: "group-avatar", ClientNeedSave: true})
		group := resp.GetGroupInfo()
		if group.GetQueryGroupId() != 10 || group.GetQueryGroupName() != "Team" || group.GetAvatar() != "group-avatar" || !group.GetClientNeedSave() {
			t.Fatalf("group info mapping mismatch: %+v", group)
		}
	})

	t.Run("group members", func(t *testing.T) {
		resp := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 10, Members: []*friend.GroupMemberContact{{
			UserId: 2, Account: "alice", Name: "Alice", Avatar: "avatar", Role: "owner", UpdateTime: "2026-07-11T12:00:00Z",
		}}})
		members := resp.GetGroupMembersRsp()
		if members.GetGroupId() != 10 || len(members.GetMembers()) != 1 {
			t.Fatalf("group member list mapping mismatch: %+v", members)
		}
		member := members.GetMembers()[0]
		if member.GetUserId() != 2 || member.GetAccount() != "alice" || member.GetAvatar() != "avatar" || member.GetRole() != "owner" || member.GetUpdateTime() == "" {
			t.Fatalf("group member mapping mismatch: %+v", member)
		}
	})

	t.Run("joined groups", func(t *testing.T) {
		resp := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{{
			GroupId: 10, GroupName: "Team", Avatar: "group-avatar", OwnerUserId: 2, UpdateTime: "2026-07-11T12:00:00Z",
		}}})
		groups := resp.GetJoinedGroupsRsp().GetGroups()
		if len(groups) != 1 || groups[0].GetGroupId() != 10 || groups[0].GetOwnerUserId() != 2 || groups[0].GetAvatar() != "group-avatar" {
			t.Fatalf("joined group mapping mismatch: %+v", groups)
		}
	})

	t.Run("contacts", func(t *testing.T) {
		resp := buildContactListResponse(&friend.FriendListRsp{Contacts: []*friend.FriendContact{{
			UserId: 2, Account: "alice", Name: "Alice", Avatar: "avatar", Alias: "同学", IsNotify: false, UpdateTime: "2026-07-11T12:00:00Z",
		}}}, 1001)
		contacts := resp.GetContactListRsp().GetContacts()
		if len(contacts) != 1 || contacts[0].GetAlias() != "同学" || contacts[0].GetIsNotify() || contacts[0].GetUpdateTime() == "" {
			t.Fatalf("contact mapping mismatch: %+v", contacts)
		}
	})
}

func TestOperationResponseUsesServerForSuccessAndWarnForFailure(t *testing.T) {
	tests := []struct {
		name    string
		resp    *pb.ResponseMessage
		want    string
		success bool
	}{
		{name: "friend success", resp: buildFriendOperationResponse(&friend.FriendOperationRsp{Operation: "update_friend_alias"}, "操作成功"), want: "好友备注更新成功", success: true},
		{name: "friend missing", resp: buildFriendOperationResponse(&friend.FriendOperationRsp{Operation: "update_friend_notify"}, "记录不存在"), want: "好友关系不存在，无法更新通知设置"},
		{name: "group success", resp: buildGroupOperationResponse(&friend.GroupOperationRsp{Operation: "remove_group_member"}, "操作成功"), want: "退出群组成功", success: true},
		{name: "group duplicate", resp: buildGroupOperationResponse(&friend.GroupOperationRsp{Operation: "add_group_member"}, "已存在"), want: "你已经在该群中了"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.success {
				if got := tt.resp.GetServer().GetServerMsg(); got != tt.want {
					t.Fatalf("unexpected success message: %q", got)
				}
				return
			}
			if got := tt.resp.GetWarn().GetWarningMessage(); got != tt.want {
				t.Fatalf("unexpected warning message: %q", got)
			}
		})
	}
}

func TestBuildRelationshipResponsesPreserveStructuredResults(t *testing.T) {
	request := &friend.RelationshipRequestInfo{
		RequestId: 7, RequestType: "group_join", RequesterUserId: 2, RequesterName: "Alice",
		GroupId: 10, GroupName: "Team", Status: "pending", CreatedAt: "2026-07-13T00:00:00Z", ExpiresAt: "2026-07-20T00:00:00Z",
	}
	operation := buildRelationshipOperationResponse(&friend.RelationshipOperationRsp{Operation: "resolve_group_join_request", Request: request}, friend.FriendResult_FORBIDDEN)
	if got := operation.GetRelationshipOperationRsp(); got.GetResult() != "FORBIDDEN" || got.GetRequest().GetGroupName() != "Team" || got.GetRequest().GetExpiresAt() == "" {
		t.Fatalf("relationship operation mapping mismatch: %+v", got)
	}

	list := buildRelationshipRequestListResponse(&friend.RelationshipRequestListRsp{Requests: []*friend.RelationshipRequestInfo{request}})
	if got := list.GetRelationshipRequestListRsp().GetRequests(); len(got) != 1 || got[0].GetRequestId() != 7 {
		t.Fatalf("relationship list mapping mismatch: %+v", got)
	}

	member := buildGroupMemberOperationResponse(&friend.GroupOperationRsp{
		Operation: "update_group_member_role", GroupId: 10, UserId: 2, Role: "admin", UpdateTime: "2026-07-13T01:00:00Z",
	}, friend.FriendResult_FRIEND_OK).GetGroupMemberOperationRsp()
	if member.GetResult() != "FRIEND_OK" || member.GetRole() != "admin" || member.GetGroupId() != 10 {
		t.Fatalf("group member operation mapping mismatch: %+v", member)
	}
}
