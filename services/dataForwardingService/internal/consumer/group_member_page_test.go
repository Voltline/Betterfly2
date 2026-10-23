package consumer

import (
	friend "Betterfly2/proto/friend"
	"testing"
)

func TestGroupMemberPaginationFieldsAndMemberCountsAreMapped(t *testing.T) {
	members := buildGroupMembersResponse(&friend.GroupMemberListRsp{
		GroupId: 3001, Members: []*friend.GroupMemberContact{{UserId: 1}}, NextCursor: "c", HasMore: true, TotalCount: 900,
	}).GetGroupMembersRsp()
	if members.GetNextCursor() != "c" || !members.GetHasMore() || members.GetTotalCount() != 900 || members.GetResult() != "" || len(members.GetMembers()) != 1 {
		t.Fatalf("unexpected members page: %+v", members)
	}
	if info := buildGroupInfoResponse(&friend.GroupInfoRsp{GroupId: 3001, MemberCount: 900}).GetGroupInfo(); info.GetMemberCount() != 900 {
		t.Fatalf("group info member_count = %d", info.GetMemberCount())
	}
	joined := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{{GroupId: 3001, MemberCount: 900}}}).GetJoinedGroupsRsp()
	if joined.GetGroups()[0].GetMemberCount() != 900 {
		t.Fatalf("joined group member_count = %d", joined.GetGroups()[0].GetMemberCount())
	}
}

func TestInvalidGroupMemberCursorReturnsMembersResponseWithResult(t *testing.T) {
	rsp := buildStructuredFriendResponse(&friend.ResponseMessage{Result: friend.FriendResult_INVALID_ARGUMENT, Payload: &friend.ResponseMessage_GroupMemberListRsp{
		GroupMemberListRsp: &friend.GroupMemberListRsp{GroupId: 3001, Cursor: "stale"},
	}}).GetGroupMembersRsp()
	if rsp.GetGroupId() != 3001 || rsp.GetResult() != "INVALID_ARGUMENT" || rsp.GetCursor() != "stale" || rsp.GetHasMore() || len(rsp.GetMembers()) != 0 {
		t.Fatalf("invalid cursor response = %+v", rsp)
	}
	// 成功页仍走旧映射，result为空。
	if buildStructuredFriendResponse(&friend.ResponseMessage{Result: friend.FriendResult_FRIEND_OK, Payload: &friend.ResponseMessage_GroupMemberListRsp{
		GroupMemberListRsp: &friend.GroupMemberListRsp{GroupId: 3001},
	}}) != nil {
		t.Fatal("successful member page should use the legacy mapping")
	}
	// 没有group_id的旧INVALID_ARGUMENT保持原来的Warn。
	if buildStructuredFriendResponse(&friend.ResponseMessage{Result: friend.FriendResult_INVALID_ARGUMENT}) != nil {
		t.Fatal("legacy invalid argument should keep the Warn mapping")
	}
}
