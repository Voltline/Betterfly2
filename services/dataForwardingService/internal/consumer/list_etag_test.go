package consumer

import (
	friend "Betterfly2/proto/friend"
	"strings"
	"testing"
)

func TestContactListETagIgnoresOrderAndReturnsNotModified(t *testing.T) {
	contacts := []*friend.FriendContact{{UserId: 2, Name: "b", Avatar: "hash-b"}, {UserId: 1, Name: "a"}}
	first := buildContactListResponse(&friend.FriendListRsp{Contacts: contacts}, 5).GetContactListRsp()
	if !strings.HasPrefix(first.GetEtag(), "v1:") || first.GetNotModified() || len(first.GetContacts()) != 2 {
		t.Fatalf("first response = %+v", first)
	}

	reordered := []*friend.FriendContact{contacts[1], contacts[0]}
	same := buildContactListResponse(&friend.FriendListRsp{Contacts: reordered, RequestEtag: first.GetEtag()}, 5).GetContactListRsp()
	if !same.GetNotModified() || len(same.GetContacts()) != 0 || same.GetEtag() != first.GetEtag() {
		t.Fatalf("unchanged contacts should be not_modified regardless of order: %+v", same)
	}

	changed := buildContactListResponse(&friend.FriendListRsp{Contacts: []*friend.FriendContact{{UserId: 2, Name: "b", Avatar: "hash-new"}, {UserId: 1, Name: "a"}}, RequestEtag: first.GetEtag()}, 5).GetContactListRsp()
	if changed.GetNotModified() || len(changed.GetContacts()) != 2 || changed.GetEtag() == first.GetEtag() {
		t.Fatalf("avatar change must produce a full list and new etag: %+v", changed)
	}
}

func TestJoinedGroupsETagNotModified(t *testing.T) {
	groups := []*friend.JoinedGroupContact{{GroupId: 9, GroupName: "x", MemberCount: 3}, {GroupId: 4, GroupName: "y"}}
	first := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: groups}).GetJoinedGroupsRsp()
	same := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{groups[1], groups[0]}, RequestEtag: first.GetEtag()}).GetJoinedGroupsRsp()
	if first.GetEtag() == "" || !same.GetNotModified() || len(same.GetGroups()) != 0 {
		t.Fatalf("first=%+v same=%+v", first, same)
	}
	groups[0].MemberCount = 4
	if changed := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: groups, RequestEtag: first.GetEtag()}).GetJoinedGroupsRsp(); changed.GetNotModified() || changed.GetEtag() == first.GetEtag() {
		t.Fatalf("member_count change must invalidate etag: %+v", changed)
	}
}

func TestGroupMembersETagOnlyForCompleteSinglePage(t *testing.T) {
	members := []*friend.GroupMemberContact{{UserId: 1, Role: "owner"}, {UserId: 2, Role: "member"}}
	first := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 30, Members: members, TotalCount: 2}).GetGroupMembersRsp()
	if first.GetEtag() == "" {
		t.Fatal("complete single page should carry an etag")
	}
	same := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 30, Members: members, TotalCount: 2, RequestEtag: first.GetEtag()}).GetGroupMembersRsp()
	if !same.GetNotModified() || len(same.GetMembers()) != 0 || same.GetTotalCount() != 0 || same.GetGroupId() != 30 || same.GetEtag() != first.GetEtag() {
		t.Fatalf("not_modified members = %+v", same)
	}
	if other := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 31, Members: members, RequestEtag: first.GetEtag()}).GetGroupMembersRsp(); other.GetNotModified() {
		t.Fatal("etag must be bound to the group")
	}

	paged := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 30, Members: members, HasMore: true, NextCursor: "n", RequestEtag: first.GetEtag()}).GetGroupMembersRsp()
	later := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 30, Members: members, Cursor: "c", RequestEtag: first.GetEtag()}).GetGroupMembersRsp()
	if paged.GetEtag() != "" || paged.GetNotModified() || later.GetEtag() != "" || later.GetNotModified() || len(paged.GetMembers()) != 2 {
		t.Fatalf("paginated pages must not use etag: paged=%+v later=%+v", paged, later)
	}

	emptyETag := buildGroupMembersResponse(&friend.GroupMemberListRsp{GroupId: 30}).GetGroupMembersRsp().GetEtag()
	rejected := buildStructuredFriendResponse(&friend.ResponseMessage{Result: friend.FriendResult_INVALID_ARGUMENT, Payload: &friend.ResponseMessage_GroupMemberListRsp{
		GroupMemberListRsp: &friend.GroupMemberListRsp{GroupId: 30, RequestEtag: emptyETag},
	}}).GetGroupMembersRsp()
	if rejected.GetEtag() != "" || rejected.GetNotModified() || rejected.GetResult() != "INVALID_ARGUMENT" {
		t.Fatalf("rejected page must not carry etag: %+v", rejected)
	}
}
