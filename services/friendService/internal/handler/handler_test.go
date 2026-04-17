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
