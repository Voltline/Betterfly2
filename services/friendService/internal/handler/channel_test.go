package handler

import (
	channel "Betterfly2/proto/channel"
	friend "Betterfly2/proto/friend"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func expectOrdinaryGroup(t *testing.T, mock sqlmock.Sqlmock, id int64) {
	t.Helper()
	mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WithArgs(id, 1).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
}

func TestChannelInvalidRequestsNeverQueryDatabase(t *testing.T) {
	empty := " "
	username := "valid_handle"
	for _, request := range []*channel.ChannelRequest{
		nil, {}, {RequestId: strings.Repeat("a", 129), Payload: &channel.ChannelRequest_Get{Get: &channel.GetChannel{ChannelId: 1}}},
		{Payload: &channel.ChannelRequest_Create{Create: &channel.CreateChannel{ChannelId: 1, Name: "x", Visibility: channel.Visibility(55)}}},
		{Payload: &channel.ChannelRequest_Update{Update: &channel.UpdateChannel{ChannelId: 1, Name: &empty, Username: &username}}},
		{Payload: &channel.ChannelRequest_Subscribe{Subscribe: &channel.SubscribeChannel{ChannelId: -1}}},
		{Payload: &channel.ChannelRequest_Update{Update: &channel.UpdateChannel{ChannelId: 1}}},
	} {
		database, mock := setupMockDB(t)
		response, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, request)
		if err != nil || response.GetChannelResponse().GetResult() != channel.ChannelResult_CHANNEL_INVALID_ARGUMENT {
			t.Fatalf("invalid request accepted: response=%v err=%v", response, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelDatabaseFailureNotConvertedToCompletedDomainResponse(t *testing.T) {
	database, mock := setupMockDB(t)
	injected := errors.New("database unavailable")
	mock.ExpectQuery(`(?s)SELECT groups.group_id.*JOIN groups`).WillReturnError(injected)
	response, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, &channel.ChannelRequest{RequestId: "r", Payload: &channel.ChannelRequest_Get{Get: &channel.GetChannel{ChannelId: 1}}})
	if !errors.Is(err, injected) || response != nil {
		t.Fatalf("transient failure cached: %v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelPrivateHiddenAndPublicResponseMapping(t *testing.T) {
	for _, visible := range []bool{false, true} {
		database, mock := setupMockDB(t)
		rows := sqlmock.NewRows([]string{"group_id", "name", "description", "avatar", "is_public", "owner_user_id", "subscriber_count", "subscribed", "my_role"})
		if visible {
			rows.AddRow(91, "频道", "描述", "avatar-hash", true, 2, 5, true, "owner")
		}
		mock.ExpectQuery(`(?s)channel_settings.is_public = TRUE OR viewer.user_id IS NOT NULL`).WithArgs(int64(2), int64(91), 1).WillReturnRows(rows)
		response, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, &channel.ChannelRequest{RequestId: "correlation", Payload: &channel.ChannelRequest_Get{Get: &channel.GetChannel{ChannelId: 91}}})
		if err != nil {
			t.Fatal(err)
		}
		data := response.GetChannelResponse()
		if data.RequestId != "correlation" || data.Operation != "get_channel" || response.TargetUserId != 2 {
			t.Fatalf("lost routing: %v", response)
		}
		if visible {
			if data.Result != channel.ChannelResult_CHANNEL_OK || data.Channel.AvatarHash != "avatar-hash" || data.Channel.MyRole != "owner" || data.Channel.SubscriberCount != 5 {
				t.Fatal(data)
			}
		} else if data.Result != channel.ChannelResult_CHANNEL_NOT_FOUND || data.Channel != nil {
			t.Fatalf("private channel leaked: %v", data)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelProtocolOptionalUpdateAndOldGroupBytes(t *testing.T) {
	raw := []byte{0x08, 0x01, 0x10, 0x02, 0x18, 0x01} // Existing QueryGroup: requester=1, group=2, client_need_save=true.
	var old friend.QueryGroup
	if err := proto.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if old.RequestUserId != 1 || old.GroupId != 2 || !old.ClientNeedSave {
		t.Fatal("legacy fields changed")
	}
	empty := ""
	message := &channel.UpdateChannel{ChannelId: 1, Description: &empty}
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded channel.UpdateChannel
	if err := proto.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Description == nil || decoded.Name != nil || decoded.Visibility != nil {
		t.Fatalf("PATCH presence lost: %v", &decoded)
	}
}
