package handler

import (
	channel "Betterfly2/proto/channel"
	friend "Betterfly2/proto/friend"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectNoFileGrant(mock sqlmock.Sqlmock, fileHash string, userID int64) {
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM file_access_grants AS g JOIN file_metadata AS f .*is_verified = TRUE.*g.file_hash = \$1 AND g.user_id = \$2`).
		WithArgs(fileHash, userID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}

func TestGroupAvatarRequiresManagerGrantWithoutUpdating(t *testing.T) {
	fileHash := strings.Repeat("f", 128)
	database, mock := setupMockDB(t)
	mock.ExpectQuery(`SELECT \* FROM "group_members" WHERE group_id = \$1 AND user_id = \$2`).
		WithArgs(int64(3001), int64(1001), 1).
		WillReturnRows(groupMemberRows().AddRow(3001, 1001, "owner", "2026-07-18T00:00:00Z"))
	expectNoFileGrant(mock, fileHash, 1001)

	response, err := (&FriendHandler{}).handleUpdateGroupAvatarWithDB(database, &friend.RequestMessage{TargetUserId: 1001},
		&friend.UpdateGroupAvatar{RequestUserId: 1001, GroupId: 3001, AvatarHash: fileHash})
	if err != nil || response.GetResult() != friend.FriendResult_FORBIDDEN || response.GetGroupOperationRsp().GetGroupId() != 3001 {
		t.Fatalf("unowned group avatar accepted: response=%v err=%v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelAvatarRequiresActorGrantBeforeCreate(t *testing.T) {
	fileHash := strings.Repeat("f", 128)
	database, mock := setupMockDB(t)
	expectNoFileGrant(mock, fileHash, 2)

	response, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}},
		&channel.ChannelRequest{RequestId: "r", Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{
			ChannelId: 1, Name: "news", Visibility: channel.Visibility_PUBLIC, AvatarHash: fileHash,
		}}})
	if err != nil || response.GetChannelResponse().GetResult() != channel.ChannelResult_CHANNEL_FORBIDDEN {
		t.Fatalf("unowned channel avatar accepted: response=%v err=%v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
