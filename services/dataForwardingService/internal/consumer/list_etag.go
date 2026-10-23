package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"google.golang.org/protobuf/proto"
)

// listETag 对最终发给客户端的列表内容做确定性序列化后取哈希。Deterministic只保证同一份
// 二进制内稳定，DF版本不同时etag可能对不上，代价只是多回一次完整列表。
func listETag(canonical proto.Message) string {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return "v1:" + hex.EncodeToString(sum[:16])
}

// 哈希前按ID排序，数据库返回顺序变化不影响etag；回包本身保持原顺序。
func applyContactListETag(list *pb.ContactListRsp, requestETag string) {
	contacts := append([]*pb.ContactInfo(nil), list.GetContacts()...)
	sort.SliceStable(contacts, func(i, j int) bool { return contacts[i].GetUserId() < contacts[j].GetUserId() })
	list.Etag = listETag(&pb.ContactListRsp{Contacts: contacts})
	if requestETag != "" && requestETag == list.Etag {
		list.Contacts, list.NotModified = nil, true
	}
}

func applyJoinedGroupsETag(list *pb.JoinedGroupsRsp, requestETag string) {
	groups := append([]*pb.JoinedGroupInfo(nil), list.GetGroups()...)
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].GetGroupId() < groups[j].GetGroupId() })
	list.Etag = listETag(&pb.JoinedGroupsRsp{Groups: groups})
	if requestETag != "" && requestETag == list.Etag {
		list.Groups, list.NotModified = nil, true
	}
}

// 成员只在一页即全部（第一页且没有下一页）时给etag，分页的大群不缓存。
func applyGroupMembersETag(list *pb.GroupMembersRsp, requestETag string) {
	if list.GetCursor() != "" || list.GetHasMore() {
		return
	}
	members := append([]*pb.GroupMemberInfo(nil), list.GetMembers()...)
	sort.SliceStable(members, func(i, j int) bool { return members[i].GetUserId() < members[j].GetUserId() })
	list.Etag = listETag(&pb.GroupMembersRsp{GroupId: list.GetGroupId(), Members: members})
	if requestETag != "" && requestETag == list.Etag {
		list.Members, list.TotalCount, list.NotModified = nil, 0, true
	}
}
