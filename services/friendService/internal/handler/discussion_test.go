package handler

import (
	channel "Betterfly2/proto/channel"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"errors"
	"testing"
)

func TestDiscussionBindingUsesExistingFriendRoute(t *testing.T) {
	database, mock := setupMockDB(t)
	r := &channel.ChannelRequest{RequestId: "bind", Payload: &channel.ChannelRequest_SetDiscussionGroup{SetDiscussionGroup: &channel.SetDiscussionGroup{ChannelId: 9001, GroupId: -1}}}
	resp, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, r)
	if err != nil || resp.GetTargetUserId() != 2 || resp.GetChannelResponse().GetOperation() != "set_discussion_group" || resp.GetChannelResponse().GetResult() != channel.ChannelResult_CHANNEL_INVALID_ARGUMENT {
		t.Fatal(resp, err)
	}
	injected := errors.New("database unavailable")
	mock.ExpectBegin().WillReturnError(injected)
	r.GetSetDiscussionGroup().GroupId = 9002
	if resp, err := handleChannelRequest(friendRequestContext{database: database, request: &friend.RequestMessage{TargetUserId: 2}}, r); resp != nil || !errors.Is(err, injected) {
		t.Fatal("transient failure cached", resp, err)
	}
	if channelInfo(&db.ChannelView{GroupID: 9001, DiscussionGroupID: 9002}).GetDiscussionGroupId() != 9002 {
		t.Fatal("binding dropped from response")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
