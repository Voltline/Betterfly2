package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建模拟数据库失败: %v", err)
	}

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn:       dbMock,
		DriverName: "postgres",
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建GORM连接失败: %v", err)
	}

	return gormDB, mock
}

func useMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()

	gormDB, mock := setupMockDB(t)
	originalDBFunc := db.DB
	db.DB = func(dst ...interface{}) *gorm.DB {
		return gormDB
	}

	t.Cleanup(func() {
		db.DB = originalDBFunc
		sqlDB, err := gormDB.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	})

	return mock
}

func TestHandleQueryGroup_AllowsNonMemberLookup(t *testing.T) {
	mock := useMockDB(t)

	handler := &FriendHandler{}
	req := &friend.RequestMessage{
		TargetUserId: 2001,
		Payload: &friend.RequestMessage_QueryGroup{
			QueryGroup: &friend.QueryGroup{
				RequestUserId:  2001,
				GroupId:        3001,
				ClientNeedSave: true,
			},
		},
	}

	mock.ExpectQuery(`SELECT \* FROM "groups" WHERE group_id = \$1 AND is_delete = \$2 ORDER BY "groups"\."group_id" LIMIT \$3`).
		WithArgs(int64(3001), false, 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "name", "avatar", "owner_user_id", "is_delete", "update_time",
		}).AddRow(3001, "test-group", "group-avatar", 1001, false, "2026-04-16 12:00:00"))

	resp, err := handler.handleQueryGroup(req, req.GetQueryGroup())
	if err != nil {
		t.Fatalf("handleQueryGroup 返回错误: %v", err)
	}
	if resp == nil {
		t.Fatal("handleQueryGroup 返回了空响应")
	}
	if resp.Result != friend.FriendResult_FRIEND_OK {
		t.Fatalf("期望返回 FRIEND_OK，实际为 %v", resp.Result)
	}

	groupInfo := resp.GetGroupInfoRsp()
	if groupInfo == nil {
		t.Fatal("期望返回群信息，但实际为空")
	}
	if groupInfo.GetGroupId() != 3001 {
		t.Fatalf("期望 group_id=3001，实际为 %d", groupInfo.GetGroupId())
	}
	if groupInfo.GetGroupName() != "test-group" {
		t.Fatalf("期望 group_name=test-group，实际为 %s", groupInfo.GetGroupName())
	}
	if !groupInfo.GetClientNeedSave() {
		t.Fatal("期望 client_need_save 被透传为 true")
	}
}

func TestHandleQueryJoinedGroups_ReturnsJoinedGroups(t *testing.T) {
	mock := useMockDB(t)

	handler := &FriendHandler{}
	req := &friend.RequestMessage{
		TargetUserId: 2001,
		Payload: &friend.RequestMessage_QueryJoinedGroups{
			QueryJoinedGroups: &friend.QueryJoinedGroups{
				UserId: 2001,
			},
		},
	}

	mock.ExpectQuery(`SELECT groups\.group_id, groups\.name AS group_name, groups\.avatar, groups\.owner_user_id, groups\.update_time FROM "group_members" JOIN groups ON groups\.group_id = group_members\.group_id WHERE group_members\.user_id = \$1 AND groups\.is_delete = \$2 ORDER BY groups\.group_id ASC`).
		WithArgs(int64(2001), false).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "avatar", "owner_user_id", "update_time",
		}).AddRow(3001, "team-a", "avatar-a", 1001, "2026-04-17 10:00:00").
			AddRow(3002, "team-b", "avatar-b", 1002, "2026-04-17 11:00:00"))

	resp, err := handler.handleQueryJoinedGroups(req, req.GetQueryJoinedGroups())
	if err != nil {
		t.Fatalf("handleQueryJoinedGroups 返回错误: %v", err)
	}
	if resp == nil {
		t.Fatal("handleQueryJoinedGroups 返回了空响应")
	}
	if resp.Result != friend.FriendResult_FRIEND_OK {
		t.Fatalf("期望返回 FRIEND_OK，实际为 %v", resp.Result)
	}

	groupList := resp.GetJoinedGroupListRsp()
	if groupList == nil {
		t.Fatal("期望返回已加入群列表，但实际为空")
	}
	if len(groupList.GetGroups()) != 2 {
		t.Fatalf("期望返回 2 个群，实际为 %d", len(groupList.GetGroups()))
	}
	if groupList.GetGroups()[0].GetGroupId() != 3001 || groupList.GetGroups()[1].GetGroupId() != 3002 {
		t.Fatalf("返回的群列表顺序或内容不符合预期: %+v", groupList.GetGroups())
	}
}

func TestHandleRemoveGroupMember_LastOwnerLeavesAndClosesGroup(t *testing.T) {
	mock := useMockDB(t)

	handler := &FriendHandler{}
	req := removeGroupMemberRequest(1001, 3001)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "groups" WHERE group_id = \$1 AND is_delete = \$2 ORDER BY "groups"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), false, 1).
		WillReturnRows(groupRows().AddRow(3001, "solo", "", 1001, false, "2026-07-11T01:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2 ORDER BY "group_members"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1001, "owner", "2026-07-11T01:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id <> \$2 ORDER BY CASE WHEN role = 'admin' THEN 0 ELSE 1 END,update_time ASC,user_id ASC,"group_members"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows())
	mock.ExpectExec(`UPDATE "groups" SET "is_delete"=\$1,"update_time"=\$2 WHERE group_id = \$3 AND is_delete = \$4`).
		WithArgs(true, sqlmock.AnyArg(), int64(3001), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1001)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := handler.handleRemoveGroupMember(req, req.GetRemoveGroupMember())
	if err != nil {
		t.Fatalf("handleRemoveGroupMember 返回错误: %v", err)
	}
	if resp.GetResult() != friend.FriendResult_FRIEND_OK {
		t.Fatalf("期望最后一名群主可以退出，实际结果为 %v", resp.GetResult())
	}
}

func TestHandleRemoveGroupMember_OwnerTransfersBeforeLeaving(t *testing.T) {
	mock := useMockDB(t)

	handler := &FriendHandler{}
	req := removeGroupMemberRequest(1001, 3001)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "groups" WHERE group_id = \$1 AND is_delete = \$2 ORDER BY "groups"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), false, 1).
		WillReturnRows(groupRows().AddRow(3001, "team", "", 1001, false, "2026-07-11T01:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2 ORDER BY "group_members"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1001, "owner", "2026-07-11T01:00:00Z"))
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id <> \$2 ORDER BY CASE WHEN role = 'admin' THEN 0 ELSE 1 END,update_time ASC,user_id ASC,"group_members"\."group_id" LIMIT \$3 FOR UPDATE`).
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1002, "member", "2026-07-11T02:00:00Z"))
	mock.ExpectExec(`UPDATE "groups" SET "owner_user_id"=\$1,"update_time"=\$2 WHERE group_id = \$3 AND is_delete = \$4`).
		WithArgs(int64(1002), sqlmock.AnyArg(), int64(3001), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "group_members" SET "role"=\$1,"update_time"=\$2 WHERE group_id = \$3 AND user_id = \$4`).
		WithArgs("owner", sqlmock.AnyArg(), int64(3001), int64(1002)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1001)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := handler.handleRemoveGroupMember(req, req.GetRemoveGroupMember())
	if err != nil {
		t.Fatalf("handleRemoveGroupMember 返回错误: %v", err)
	}
	if resp.GetResult() != friend.FriendResult_FRIEND_OK {
		t.Fatalf("期望群主转交后可以退出，实际结果为 %v", resp.GetResult())
	}
}

func removeGroupMemberRequest(userID, groupID int64) *friend.RequestMessage {
	return &friend.RequestMessage{
		TargetUserId: userID,
		Payload: &friend.RequestMessage_RemoveGroupMember{
			RemoveGroupMember: &friend.RemoveGroupMember{
				RequestUserId: userID,
				GroupId:       groupID,
				UserId:        userID,
			},
		},
	}
}

func groupRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"group_id", "name", "avatar", "owner_user_id", "is_delete", "update_time"})
}

func groupMemberRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"group_id", "user_id", "role", "update_time"})
}

func TestFriendHandlersRejectInvalidArgumentsWithoutDatabaseAccess(t *testing.T) {
	handler := &FriendHandler{}
	req := &friend.RequestMessage{TargetUserId: 1001}
	tests := []struct {
		name string
		call func() (*friend.ResponseMessage, error)
	}{
		{name: "query friend list", call: func() (*friend.ResponseMessage, error) {
			return handler.handleQueryFriendList(req, &friend.QueryFriendList{})
		}},
		{name: "remove self as friend", call: func() (*friend.ResponseMessage, error) {
			return handler.handleRemoveDirectFriend(req, &friend.RemoveDirectFriend{UserId: 1, FriendId: 1})
		}},
		{name: "update alias missing friend", call: func() (*friend.ResponseMessage, error) {
			return handler.handleUpdateFriendAlias(req, &friend.UpdateFriendAlias{UserId: 1})
		}},
		{name: "update notify missing user", call: func() (*friend.ResponseMessage, error) {
			return handler.handleUpdateFriendNotify(req, &friend.UpdateFriendNotify{FriendId: 2})
		}},
		{name: "add self as friend", call: func() (*friend.ResponseMessage, error) {
			return handler.handleAddDirectFriend(req, &friend.AddDirectFriend{UserId: 1, FriendId: 1})
		}},
		{name: "create unnamed group", call: func() (*friend.ResponseMessage, error) {
			return handler.handleCreateGroup(req, &friend.CreateGroup{OwnerUserId: 1, GroupId: 2})
		}},
		{name: "add invalid group member", call: func() (*friend.ResponseMessage, error) {
			return handler.handleAddGroupMember(req, &friend.AddGroupMember{UserId: 1})
		}},
		{name: "empty group avatar", call: func() (*friend.ResponseMessage, error) {
			return handler.handleUpdateGroupAvatar(req, &friend.UpdateGroupAvatar{RequestUserId: 1, GroupId: 2})
		}},
		{name: "query invalid group members", call: func() (*friend.ResponseMessage, error) {
			return handler.handleQueryGroupMembers(req, &friend.QueryGroupMembers{RequestUserId: 1})
		}},
		{name: "query joined groups without user", call: func() (*friend.ResponseMessage, error) {
			return handler.handleQueryJoinedGroups(req, &friend.QueryJoinedGroups{})
		}},
		{name: "remove a different user", call: func() (*friend.ResponseMessage, error) {
			return handler.handleRemoveGroupMember(req, &friend.RemoveGroupMember{RequestUserId: 1, GroupId: 2, UserId: 3})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.call()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetResult() != friend.FriendResult_INVALID_ARGUMENT {
				t.Fatalf("expected INVALID_ARGUMENT, got %v", resp.GetResult())
			}
		})
	}
}

func TestHandleQueryFriendListMapsDatabaseContacts(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(`SELECT friends\.friend_id AS user_id, users\.account, users\.name, users\.avatar, friends\.alias, friends\.is_notify, friends\.update_time FROM "friends" JOIN users ON users\.id = friends\.friend_id WHERE friends\.user_id = \$1 AND friends\.is_delete = \$2 ORDER BY friends\.update_time DESC`).
		WithArgs(int64(1001), false).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account", "name", "avatar", "alias", "is_notify", "update_time"}).
			AddRow(1002, "alice", "Alice", "avatar-hash", "同学", true, "2026-07-11T12:00:00Z"))

	handler := &FriendHandler{}
	req := &friend.RequestMessage{TargetUserId: 1001}
	resp, err := handler.handleQueryFriendList(req, &friend.QueryFriendList{UserId: 1001})
	if err != nil {
		t.Fatal(err)
	}
	contacts := resp.GetFriendListRsp().GetContacts()
	if resp.GetResult() != friend.FriendResult_FRIEND_OK || len(contacts) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	contact := contacts[0]
	if contact.GetUserId() != 1002 || contact.GetAccount() != "alice" || contact.GetAlias() != "同学" || !contact.GetIsNotify() {
		t.Fatalf("contact mapping mismatch: %+v", contact)
	}
}

func TestHandleQueryGroupMembersMapsActiveMembers(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1001)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT group_members\.user_id, users\.account, users\.name, users\.avatar, group_members\.role, group_members\.update_time FROM "group_members" JOIN users ON users\.id = group_members\.user_id WHERE group_members\.group_id = \$1 ORDER BY group_members\.user_id ASC`).
		WithArgs(int64(3001)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account", "name", "avatar", "role", "update_time"}).
			AddRow(int64(1001), "alice", "Alice", "avatar-a", "owner", "2026-07-12T10:00:00Z").
			AddRow(int64(1002), "bob", "Bob", "avatar-b", "member", "2026-07-12T10:01:00Z"))

	response, err := (&FriendHandler{}).handleQueryGroupMembers(
		&friend.RequestMessage{TargetUserId: 1001},
		&friend.QueryGroupMembers{RequestUserId: 1001, GroupId: 3001},
	)
	if err != nil {
		t.Fatal(err)
	}
	members := response.GetGroupMemberListRsp().GetMembers()
	if response.GetResult() != friend.FriendResult_FRIEND_OK || len(members) != 2 {
		t.Fatalf("unexpected member list response: %+v", response)
	}
	if members[0].GetUserId() != 1001 || members[0].GetRole() != "owner" || members[1].GetName() != "Bob" || members[1].GetAvatar() != "avatar-b" {
		t.Fatalf("group members were mapped incorrectly: %+v", members)
	}
}

func TestHandleQueryGroupMembersRejectsNonMember(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(2001)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	response, err := (&FriendHandler{}).handleQueryGroupMembers(
		&friend.RequestMessage{TargetUserId: 2001},
		&friend.QueryGroupMembers{RequestUserId: 2001, GroupId: 3001},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != friend.FriendResult_RECORD_NOT_EXIST || response.GetGroupMemberListRsp() != nil {
		t.Fatalf("non-member received group membership data: %+v", response)
	}
}
