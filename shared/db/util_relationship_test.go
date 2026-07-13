package db

import (
	"testing"
	"time"
)

func TestRelationshipRequestWindowIsExactlySevenDays(t *testing.T) {
	created := time.Date(2026, 7, 13, 8, 30, 0, 123, time.UTC)
	createdAt, expiresAt := relationshipWindow(created)
	parsedCreated, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	parsedExpires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsedExpires.Sub(parsedCreated); got != 7*24*time.Hour {
		t.Fatalf("request lifetime = %s, want 168h", got)
	}
}

func TestRelationshipTimeSortsLexicographically(t *testing.T) {
	base := time.Date(2026, 7, 13, 8, 30, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(time.Nanosecond), base.Add(100 * time.Millisecond), base.Add(time.Second)}
	for i := 1; i < len(times); i++ {
		if previous, current := relationshipTime(times[i-1]), relationshipTime(times[i]); previous >= current {
			t.Fatalf("timestamp strings do not preserve order: %q >= %q", previous, current)
		}
	}
}

func TestRelationshipUpdateTimeFitsLegacySynchronizationColumns(t *testing.T) {
	value := relationshipUpdateTime(time.Date(2026, 7, 13, 3, 5, 50, 123456789, time.UTC))
	if value != "2026-07-13T03:05:50Z" {
		t.Fatalf("unexpected update time: %q", value)
	}
	if len(value) > 25 {
		t.Fatalf("update time exceeds varchar(25): length=%d value=%q", len(value), value)
	}
}

func TestGroupPermissionMatrix(t *testing.T) {
	tests := []struct {
		name       string
		actorRole  string
		targetRole string
		canManage  bool
		canKick    bool
		canPromote bool
	}{
		{name: "owner over member", actorRole: GroupRoleOwner, targetRole: GroupRoleMember, canManage: true, canKick: true, canPromote: true},
		{name: "owner over admin", actorRole: GroupRoleOwner, targetRole: GroupRoleAdmin, canManage: true, canKick: true, canPromote: true},
		{name: "owner protected", actorRole: GroupRoleOwner, targetRole: GroupRoleOwner, canManage: true},
		{name: "admin over member", actorRole: GroupRoleAdmin, targetRole: GroupRoleMember, canManage: true, canKick: true},
		{name: "admin cannot kick admin", actorRole: GroupRoleAdmin, targetRole: GroupRoleAdmin, canManage: true},
		{name: "admin cannot kick owner", actorRole: GroupRoleAdmin, targetRole: GroupRoleOwner, canManage: true},
		{name: "member has no management", actorRole: GroupRoleMember, targetRole: GroupRoleMember},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := canManageGroup(test.actorRole); got != test.canManage {
				t.Fatalf("canManageGroup() = %t, want %t", got, test.canManage)
			}
			if got := canKickGroupMember(test.actorRole, test.targetRole); got != test.canKick {
				t.Fatalf("canKickGroupMember() = %t, want %t", got, test.canKick)
			}
			if got := canChangeGroupMemberRole(test.actorRole, test.targetRole, GroupRoleAdmin); got != test.canPromote {
				t.Fatalf("canChangeGroupMemberRole() = %t, want %t", got, test.canPromote)
			}
		})
	}
}
