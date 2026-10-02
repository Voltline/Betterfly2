package handler

import (
	channel "Betterfly2/proto/channel"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
)

func TestUpdateGroupNotifyUsesTrustedActorAndStructuredResponse(t *testing.T) {
	for _, rows := range []int64{0, 1} {
		database, mock := setupMockDB(t)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "group_members" SET "notifications_muted"`).WithArgs(true, sqlmock.AnyArg(), int64(9), int64(2)).WillReturnResult(sqlmock.NewResult(0, rows))
		mock.ExpectCommit()
		req := &friend.RequestMessage{TargetUserId: 2, Payload: &friend.RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &friend.UpdateGroupNotify{GroupId: 9, IsNotify: false}}}
		response, err := getFriendRequestRouter().Dispatch(friendRequestContext{database: database, request: req}, req.Payload)
		if err != nil {
			t.Fatal(err)
		}
		want := friend.FriendResult_FRIEND_OK
		if rows == 0 {
			want = friend.FriendResult_RECORD_NOT_EXIST
		}
		op := response.GetGroupOperationRsp()
		if response.Result != want || op.Operation != "update_group_notify" || op.UserId != 2 || op.GroupId != 9 || op.NotificationsMuted != (rows == 1) {
			t.Fatalf("wrong preference response: %v", response)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateGroupNotifyFailureIsRetriedNotCompleted(t *testing.T) {
	database, mock := setupMockDB(t)
	for _, req := range []*friend.RequestMessage{{}, {TargetUserId: 2}} {
		payload := &friend.RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &friend.UpdateGroupNotify{}}
		response, err := getFriendRequestRouter().Dispatch(friendRequestContext{database: database, request: req}, payload)
		if err != nil || response.GetResult() != friend.FriendResult_INVALID_ARGUMENT {
			t.Fatalf("invalid request: %v %v", response, err)
		}
	}
	injected := errors.New("database unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "group_members"`).WillReturnError(injected)
	mock.ExpectRollback()
	payload := &friend.RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &friend.UpdateGroupNotify{GroupId: 9}}
	response, err := getFriendRequestRouter().Dispatch(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, payload)
	if response != nil || !errors.Is(err, injected) {
		t.Fatalf("failure cached: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelPinInfoMappingAndInvalidRequest(t *testing.T) {
	info := channelInfo(&db.ChannelView{GroupID: 9, PinnedMessageID: 44, NotificationsMuted: true, Subscribed: true})
	if info.GetPinnedMessageId() != 44 || !info.GetNotificationsMuted() {
		t.Fatal(info)
	}
	database, mock := setupMockDB(t)
	req := &channel.ChannelRequest{Payload: &channel.ChannelRequest_SetPin{SetPin: &channel.SetChannelPin{ChannelId: 9, MessageId: -1}}}
	response, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, req)
	if err != nil || response.GetChannelResponse().GetOperation() != "set_channel_pin" || response.GetChannelResponse().GetResult() != channel.ChannelResult_CHANNEL_INVALID_ARGUMENT {
		t.Fatalf("invalid pin: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFriendNotifyProtocolRoundTrip(t *testing.T) {
	request := &friend.RequestMessage{TargetUserId: 2, Payload: &friend.RequestMessage_UpdateGroupNotify{UpdateGroupNotify: &friend.UpdateGroupNotify{GroupId: 9, IsNotify: true}}}
	if field := request.ProtoReflect().Descriptor().Fields().ByName("update_group_notify"); field == nil || field.Number() != 27 {
		t.Fatal("Friend notify tag changed")
	}
	encoded, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &friend.RequestMessage{}
	if err := proto.Unmarshal(encoded, decoded); err != nil || !proto.Equal(request, decoded) {
		t.Fatalf("Friend notify roundtrip: %v", err)
	}
}
