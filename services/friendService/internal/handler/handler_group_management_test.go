package handler

import (
	friend "Betterfly2/proto/friend"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpdateGroupNamePermissionsAndValidation(t *testing.T) {
	for _, role := range []string{"owner", "admin"} {
		t.Run(role+" allowed", func(t *testing.T) {
			mock := useMockDB(t)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
				WithArgs(int64(3001), false, 1).
				WillReturnRows(groupRows().AddRow(3001, "Team", "", 1001, false, "2026-07-18T00:00:00Z"))
			mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
				WithArgs(int64(3001), int64(1001), 1).
				WillReturnRows(groupMemberRows().AddRow(3001, 1001, role, "2026-07-18T00:00:00Z"))
			mock.ExpectExec("UPDATE \"groups\" SET \"name\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND is_delete = \\$4").
				WithArgs("新群名称", sqlmock.AnyArg(), int64(3001), false).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			response, err := (&FriendHandler{}).handleUpdateGroupNameWithDB(nil,
				&friend.RequestMessage{TargetUserId: 1001},
				&friend.UpdateGroupName{RequestUserId: 1001, GroupId: 3001, GroupName: "  新群名称  "},
			)
			operation := response.GetGroupOperationRsp()
			if err != nil || response.GetResult() != friend.FriendResult_FRIEND_OK || operation.GetGroupName() != "新群名称" {
				t.Fatalf("%s rename failed: response=%+v err=%v", role, response, err)
			}
		})
	}

	t.Run("member forbidden", func(t *testing.T) {
		mock := useMockDB(t)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), false, 1).
			WillReturnRows(groupRows().AddRow(3001, "Team", "", 1002, false, "2026-07-18T00:00:00Z"))
		mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), int64(1001), 1).
			WillReturnRows(groupMemberRows().AddRow(3001, 1001, "member", "2026-07-18T00:00:00Z"))
		mock.ExpectRollback()
		response, err := (&FriendHandler{}).handleUpdateGroupNameWithDB(nil,
			&friend.RequestMessage{TargetUserId: 1001},
			&friend.UpdateGroupName{RequestUserId: 1001, GroupId: 3001, GroupName: "new name"},
		)
		if err != nil || response.GetResult() != friend.FriendResult_FORBIDDEN {
			t.Fatalf("member rename result: response=%+v err=%v", response, err)
		}
	})

	for _, name := range []string{"   ", strings.Repeat("群", 101)} {
		response, err := (&FriendHandler{}).handleUpdateGroupNameWithDB(nil,
			&friend.RequestMessage{TargetUserId: 1001},
			&friend.UpdateGroupName{RequestUserId: 1001, GroupId: 3001, GroupName: name},
		)
		if err != nil || response.GetResult() != friend.FriendResult_INVALID_ARGUMENT {
			t.Fatalf("invalid group name was accepted: runes=%d response=%+v err=%v", len([]rune(name)), response, err)
		}
	}
}

func TestTransferGroupOwnerPreservesUniqueOwnerInvariant(t *testing.T) {
	mock := useMockDB(t)
	expectSuccessfulOwnerTransfer(mock)

	response, err := (&FriendHandler{}).handleTransferGroupOwnerWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1001},
		&friend.TransferGroupOwner{RequestUserId: 1001, GroupId: 3001, UserId: 1002},
	)
	operation := response.GetGroupOperationRsp()
	if err != nil || response.GetResult() != friend.FriendResult_FRIEND_OK {
		t.Fatalf("owner transfer failed: response=%+v err=%v", response, err)
	}
	if operation.GetUserId() != 1002 || operation.GetRole() != "owner" || operation.GetPreviousOwnerUserId() != 1001 || operation.GetGroupName() != "Team" {
		t.Fatalf("owner transfer response lost fields: %+v", operation)
	}
}

func TestTransferGroupOwnerRollbackLeavesNoPartialSuccess(t *testing.T) {
	mock := useMockDB(t)
	mock.ExpectBegin()
	expectLockedTransferRows(mock, 1001)
	expectGroupOwnerUpdate(mock)
	mock.ExpectExec("UPDATE \"group_members\" SET \"role\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND user_id = \\$4").
		WithArgs("owner", sqlmock.AnyArg(), int64(3001), int64(1002)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE \"group_members\" SET \"role\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND user_id = \\$4").
		WithArgs("admin", sqlmock.AnyArg(), int64(3001), int64(1001)).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	response, err := (&FriendHandler{}).handleTransferGroupOwnerWithDB(nil,
		&friend.RequestMessage{TargetUserId: 1001},
		&friend.TransferGroupOwner{RequestUserId: 1001, GroupId: 3001, UserId: 1002},
	)
	if err == nil || response != nil {
		t.Fatalf("partial transfer was reported as success: response=%+v err=%v", response, err)
	}
}

func TestTransferGroupOwnerRejectsNonOwnerAndNonMemberTarget(t *testing.T) {
	t.Run("actor is not current owner", func(t *testing.T) {
		mock := useMockDB(t)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), false, 1).
			WillReturnRows(groupRows().AddRow(3001, "Team", "", 1002, false, "2026-07-18T00:00:00Z"))
		mock.ExpectRollback()
		response, err := (&FriendHandler{}).handleTransferGroupOwnerWithDB(nil,
			&friend.RequestMessage{TargetUserId: 1001},
			&friend.TransferGroupOwner{RequestUserId: 1001, GroupId: 3001, UserId: 1003},
		)
		if err != nil || response.GetResult() != friend.FriendResult_INVALID_STATE {
			t.Fatalf("non-owner transfer result: response=%+v err=%v", response, err)
		}
	})

	t.Run("target is not a member", func(t *testing.T) {
		mock := useMockDB(t)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), false, 1).
			WillReturnRows(groupRows().AddRow(3001, "Team", "", 1001, false, "2026-07-18T00:00:00Z"))
		mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), int64(1001), 1).
			WillReturnRows(groupMemberRows().AddRow(3001, 1001, "owner", "2026-07-18T00:00:00Z"))
		mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
			WithArgs(int64(3001), int64(1003), 1).
			WillReturnRows(groupMemberRows())
		mock.ExpectRollback()
		response, err := (&FriendHandler{}).handleTransferGroupOwnerWithDB(nil,
			&friend.RequestMessage{TargetUserId: 1001},
			&friend.TransferGroupOwner{RequestUserId: 1001, GroupId: 3001, UserId: 1003},
		)
		if err != nil || response.GetResult() != friend.FriendResult_RECORD_NOT_EXIST {
			t.Fatalf("non-member target result: response=%+v err=%v", response, err)
		}
	})
}

func TestConcurrentOwnerTransfersCannotBothSucceed(t *testing.T) {
	successDB, successMock := setupMockDB(t)
	expectSuccessfulOwnerTransfer(successMock)
	loserDB, loserMock := setupMockDB(t)
	loserMock.ExpectBegin()
	loserMock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
		WithArgs(int64(3001), false, 1).
		WillReturnRows(groupRows().AddRow(3001, "Team", "", 1002, false, "2026-07-18T00:00:00Z"))
	loserMock.ExpectRollback()

	type transferCall struct {
		target  int64
		handler *FriendHandler
	}
	calls := []transferCall{
		{target: 1002, handler: &FriendHandler{database: successDB}},
		{target: 1003, handler: &FriendHandler{database: loserDB}},
	}
	start := make(chan struct{})
	var successes atomic.Int32
	var wait sync.WaitGroup
	for _, call := range calls {
		wait.Add(1)
		go func(call transferCall) {
			defer wait.Done()
			<-start
			response, err := call.handler.handleTransferGroupOwnerWithDB(call.handler.database,
				&friend.RequestMessage{TargetUserId: 1001},
				&friend.TransferGroupOwner{RequestUserId: 1001, GroupId: 3001, UserId: call.target},
			)
			if err == nil && response.GetResult() == friend.FriendResult_FRIEND_OK {
				successes.Add(1)
			}
		}(call)
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent owner transfers produced %d successes", successes.Load())
	}
	if err := successMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := loserMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectSuccessfulOwnerTransfer(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	expectLockedTransferRows(mock, 1001)
	expectGroupOwnerUpdate(mock)
	mock.ExpectExec("UPDATE \"group_members\" SET \"role\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND user_id = \\$4").
		WithArgs("owner", sqlmock.AnyArg(), int64(3001), int64(1002)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE \"group_members\" SET \"role\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND user_id = \\$4").
		WithArgs("admin", sqlmock.AnyArg(), int64(3001), int64(1001)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM \"group_members\" WHERE group_id = \\$1 AND role = \\$2").
		WithArgs(int64(3001), "owner").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2").
		WithArgs(int64(3001), int64(1002), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1002, "owner", "2026-07-18T00:01:00Z"))
	mock.ExpectCommit()
}

func expectLockedTransferRows(mock sqlmock.Sqlmock, ownerID int64) {
	mock.ExpectQuery("SELECT \\* FROM \"groups\" WHERE group_id = \\$1 AND is_delete = \\$2 .*FOR UPDATE").
		WithArgs(int64(3001), false, 1).
		WillReturnRows(groupRows().AddRow(3001, "Team", "", ownerID, false, "2026-07-18T00:00:00Z"))
	mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1001, "owner", "2026-07-18T00:00:00Z"))
	mock.ExpectQuery("SELECT \\* FROM \"group_members\" WHERE group_id = \\$1 AND user_id = \\$2 .*FOR UPDATE").
		WithArgs(int64(3001), int64(1002), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1002, "member", "2026-07-18T00:00:00Z"))
}

func expectGroupOwnerUpdate(mock sqlmock.Sqlmock) {
	mock.ExpectExec("UPDATE \"groups\" SET \"owner_user_id\"=\\$1,\"update_time\"=\\$2 WHERE group_id = \\$3 AND owner_user_id = \\$4 AND is_delete = \\$5").
		WithArgs(int64(1002), sqlmock.AnyArg(), int64(3001), int64(1001), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
