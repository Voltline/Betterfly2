package consumer

import (
	friend "Betterfly2/proto/friend"
	"testing"
)

func TestChannelKindSurvivesLegacyGroupResponseMappings(t *testing.T) {
	info := buildGroupInfoResponse(&friend.GroupInfoRsp{GroupId: 9, GroupName: "channel", IsChannel: true}).GetGroupInfo()
	joined := buildJoinedGroupsResponse(&friend.JoinedGroupListRsp{Groups: []*friend.JoinedGroupContact{{GroupId: 9, GroupName: "channel", IsChannel: true}}}).GetJoinedGroupsRsp()
	if !info.GetIsChannel() || len(joined.Groups) != 1 || !joined.Groups[0].GetIsChannel() {
		t.Fatal("channel kind lost in compatibility mapping")
	}
	if buildGroupInfoResponse(&friend.GroupInfoRsp{GroupId: 10}).GetGroupInfo().GetIsChannel() {
		t.Fatal("ordinary group became channel")
	}
}
