package db

import (
	"errors"
	"fmt"
	"testing"
)

func TestRelationshipPostgresInvitationRequiresInviterStillManager(t *testing.T) {
	database := discussionPostgres(t)
	for id := int64(1); id <= 4; id++ {
		if err := database.Create(&User{ID: id, Account: fmt.Sprintf("u%d", id), Name: "user"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := CreateGroupWithOwnerWithDB(database, 1, 9100, "group"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{2} {
		if err := addGroupMemberTx(database, 9100, id, relationshipUpdateTime(relationshipNow())); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := UpdateGroupMemberRoleByWithDB(database, 1, 9100, 2, GroupRoleAdmin); err != nil {
		t.Fatal(err)
	}
	invite, _, err := CreateGroupInvitationWithDB(database, 2, 9100, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	ownerInvite, _, err := CreateGroupInvitationWithDB(database, 1, 9100, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	// 管理员被降级后，其邀请失效。
	if _, err := UpdateGroupMemberRoleByWithDB(database, 1, 9100, 2, GroupRoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveGroupInvitationWithDB(database, 3, invite.ID, RequestStatusAccepted); !errors.Is(err, ErrRelationshipExpired) {
		t.Fatalf("accept after inviter demoted: err = %v, want expired", err)
	}
	if member, err := IsActiveGroupMemberWithDB(database, 9100, 3); err != nil || member {
		t.Fatalf("invitee joined through stale invite: member=%v err=%v", member, err)
	}
	stale, err := GetRelationshipRequestWithDB(database, invite.ID)
	if err != nil || stale.Status != RequestStatusExpired || stale.ActiveKey != nil {
		t.Fatalf("stale invite not closed: %+v err=%v", stale, err)
	}
	// 失效邀请释放了active_key，重新邀请可以创建新的待处理请求。
	if _, created, err := CreateGroupInvitationWithDB(database, 1, 9100, 3, ""); err != nil || !created {
		t.Fatalf("re-invite: created=%v err=%v", created, err)
	}
	// 仍是管理者发出的邀请可以正常接受。
	if _, err := ResolveGroupInvitationWithDB(database, 4, ownerInvite.ID, RequestStatusAccepted); err != nil {
		t.Fatal(err)
	}
	if member, err := IsActiveGroupMemberWithDB(database, 9100, 4); err != nil || !member {
		t.Fatalf("valid invite not applied: member=%v err=%v", member, err)
	}
}

func TestRelationshipPostgresDissolvedGroupCancelsPendingRequests(t *testing.T) {
	database := discussionPostgres(t)
	for id := int64(1); id <= 3; id++ {
		if err := database.Create(&User{ID: id, Account: fmt.Sprintf("u%d", id), Name: "user"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := CreateGroupWithOwnerWithDB(database, 1, 9200, "group"); err != nil {
		t.Fatal(err)
	}
	invite, _, err := CreateGroupInvitationWithDB(database, 1, 9200, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	join, _, err := CreateGroupJoinRequestWithDB(database, 3, 9200, "")
	if err != nil {
		t.Fatal(err)
	}
	exists, removed, _, err := RemoveUserFromGroupWithDB(database, 9200, 1)
	if err != nil || !exists || !removed {
		t.Fatalf("dissolve: exists=%v removed=%v err=%v", exists, removed, err)
	}
	for _, id := range []int64{invite.ID, join.ID} {
		request, err := GetRelationshipRequestWithDB(database, id)
		if err != nil {
			t.Fatal(err)
		}
		if request.Status != RequestStatusCancelled || request.ActiveKey != nil || request.ResolvedBy != 1 {
			t.Fatalf("request %d not cancelled: %+v", id, request)
		}
	}
	if _, err := ResolveGroupInvitationWithDB(database, 2, invite.ID, RequestStatusAccepted); !errors.Is(err, ErrRelationshipInvalidState) {
		t.Fatalf("accept after dissolve: err = %v", err)
	}
}
