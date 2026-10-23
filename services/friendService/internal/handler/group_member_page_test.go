package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGroupMemberCursorRoundTripAndRejectsForeignOrMalformed(t *testing.T) {
	cursor := encodeGroupMemberCursor(3001, 1500)
	if userID, ok := decodeGroupMemberCursor(cursor, 3001); !ok || userID != 1500 {
		t.Fatalf("round trip = (%d, %t)", userID, ok)
	}
	if userID, ok := decodeGroupMemberCursor("", 3001); !ok || userID != 0 {
		t.Fatalf("empty cursor should mean first page, got (%d, %t)", userID, ok)
	}
	for _, invalid := range []string{cursor + "!", "not-base64!", encodeGroupMemberCursor(3002, 1500), encodeGroupMemberCursor(3001, 0), "MzAwMQ"} {
		if _, ok := decodeGroupMemberCursor(invalid, 3001); ok {
			t.Fatalf("cursor %q should be invalid", invalid)
		}
	}
}

func TestGroupMemberPageHonorsLimitAndByteBudget(t *testing.T) {
	members := make([]db.GroupMemberContact, 0, 5)
	for id := int64(1); id <= 5; id++ {
		members = append(members, db.GroupMemberContact{UserID: id, Name: "m"})
	}
	if page, more := groupMemberPage(members, 2); len(page) != 2 || !more || page[1].GetUserId() != 2 {
		t.Fatalf("limit page = %d more=%t", len(page), more)
	}
	if page, more := groupMemberPage(members[:2], 2); len(page) != 2 || more {
		t.Fatalf("exact final page = %d more=%t", len(page), more)
	}

	large := make([]db.GroupMemberContact, 0, 4)
	for id := int64(1); id <= 4; id++ {
		large = append(large, db.GroupMemberContact{UserID: id, Name: strings.Repeat("n", 300<<10)})
	}
	page, more := groupMemberPage(large, 0)
	if len(page) != 2 || !more {
		t.Fatalf("byte budget page = %d more=%t, want 2 members then truncation", len(page), more)
	}
	huge := []db.GroupMemberContact{{UserID: 1, Name: strings.Repeat("n", groupMemberPageByteBudget)}, {UserID: 2}}
	if page, more := groupMemberPage(huge, 0); len(page) != 1 || !more {
		t.Fatalf("an oversized first member must still advance the cursor: %d more=%t", len(page), more)
	}
}

func TestQueryGroupMembersRejectsInvalidCursorWithGroupID(t *testing.T) {
	for _, payload := range []*friend.QueryGroupMembers{
		{RequestUserId: 1001, GroupId: 3001, Cursor: encodeGroupMemberCursor(3002, 5)},
		{RequestUserId: 1001, GroupId: 3001, Limit: -1},
	} {
		response, err := (&FriendHandler{}).handleQueryGroupMembersWithDB(nil, &friend.RequestMessage{TargetUserId: 1001}, payload)
		if err != nil {
			t.Fatal(err)
		}
		list := response.GetGroupMemberListRsp()
		if response.GetResult() != friend.FriendResult_INVALID_ARGUMENT || list.GetGroupId() != 3001 || list.GetCursor() != payload.GetCursor() || len(list.GetMembers()) != 0 {
			t.Fatalf("invalid request response = %+v", response)
		}
	}
}

func friendPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("friend_members_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := database.DB(); sql.Close() })
	if err := db.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestQueryGroupMembersPostgresPaginatesWholeGroup(t *testing.T) {
	database := friendPostgres(t)
	for id := int64(1); id <= 7; id++ {
		if err := database.Create(&db.User{ID: id, Account: fmt.Sprintf("u%d", id), Name: fmt.Sprintf("user%d", id)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := db.CreateGroupWithOwnerWithDB(database, 4, 3001, "team"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{7, 1, 5, 2, 6, 3} {
		if err := database.Create(&db.GroupMember{GroupID: 3001, UserID: id, Role: db.GroupRoleMember, JoinedAt: "2026-10-23T00:00:00Z", UpdateTime: "2026-10-23T00:00:00Z"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := &FriendHandler{database: database}
	query := func(cursor string, limit int32) *friend.GroupMemberListRsp {
		t.Helper()
		response, err := handler.handleQueryGroupMembersWithDB(database, &friend.RequestMessage{TargetUserId: 4},
			&friend.QueryGroupMembers{RequestUserId: 4, GroupId: 3001, Cursor: cursor, Limit: limit})
		if err != nil || response.GetResult() != friend.FriendResult_FRIEND_OK {
			t.Fatalf("query cursor=%q: result=%s err=%v", cursor, response.GetResult(), err)
		}
		return response.GetGroupMemberListRsp()
	}

	var got []int64
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		page := query(cursor, 3)
		if cursor == "" && page.GetTotalCount() != 7 {
			t.Fatalf("first page total_count = %d", page.GetTotalCount())
		}
		if cursor != "" && page.GetTotalCount() != 0 {
			t.Fatalf("later pages must not carry total_count: %d", page.GetTotalCount())
		}
		if page.GetCursor() != cursor {
			t.Fatalf("response cursor = %q, want request cursor %q", page.GetCursor(), cursor)
		}
		for _, member := range page.GetMembers() {
			got = append(got, member.GetUserId())
		}
		if !page.GetHasMore() {
			if page.GetNextCursor() != "" {
				t.Fatal("last page must not carry next_cursor")
			}
			break
		}
		cursor = page.GetNextCursor()
	}
	if fmt.Sprint(got) != "[1 2 3 4 5 6 7]" {
		t.Fatalf("paged members = %v", got)
	}

	if legacy := query("", 0); len(legacy.GetMembers()) != 7 || legacy.GetHasMore() || legacy.GetTotalCount() != 7 {
		t.Fatalf("legacy full list = %+v", legacy)
	}
	if capped := query("", 9999); len(capped.GetMembers()) != 7 || capped.GetHasMore() {
		t.Fatalf("oversized limit should be clamped, not rejected: %+v", capped)
	}

	info, err := handler.handleQueryGroupWithDB(database, &friend.RequestMessage{TargetUserId: 4}, &friend.QueryGroup{RequestUserId: 4, GroupId: 3001})
	if err != nil || info.GetGroupInfoRsp().GetMemberCount() != 7 {
		t.Fatalf("group info member_count = %d err=%v", info.GetGroupInfoRsp().GetMemberCount(), err)
	}
	joined, err := handler.handleQueryJoinedGroupsWithDB(database, &friend.RequestMessage{TargetUserId: 4}, &friend.QueryJoinedGroups{UserId: 4})
	if err != nil || len(joined.GetJoinedGroupListRsp().GetGroups()) != 1 || joined.GetJoinedGroupListRsp().GetGroups()[0].GetMemberCount() != 7 {
		t.Fatalf("joined groups = %+v err=%v", joined.GetJoinedGroupListRsp(), err)
	}
}
