package db

import "testing"

func TestListGroupManagerIDsPostgresReturnsOwnerAndAdminsOnly(t *testing.T) {
	database := discussionPostgres(t)
	for _, member := range []*GroupMember{
		newGroupMember(70, 3, GroupRoleAdmin, "2026-10-23T00:00:00Z"),
		newGroupMember(70, 1, GroupRoleOwner, "2026-10-23T00:00:00Z"),
		newGroupMember(70, 2, GroupRoleMember, "2026-10-23T00:00:00Z"),
		newGroupMember(71, 4, GroupRoleAdmin, "2026-10-23T00:00:00Z"),
	} {
		if err := database.Create(member).Error; err != nil {
			t.Fatal(err)
		}
	}
	managers, err := ListGroupManagerIDsWithDB(database, 70)
	if err != nil {
		t.Fatal(err)
	}
	if len(managers) != 2 || managers[0] != 1 || managers[1] != 3 {
		t.Fatalf("managers = %v, want [1 3]", managers)
	}
}
