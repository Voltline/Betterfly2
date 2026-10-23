package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const (
	maxGroupMemberPageSize = 500
	// 成员页按约768KiB截断，加上信封与跨Pod转发仍低于Kafka默认约1MB的单条上限。
	groupMemberPageByteBudget = 768 << 10
)

// groupMemberPage 取前limit条（limit<=0不限条数），并按字节预算截断；至少返回一条以保证游标前进。
func groupMemberPage(members []db.GroupMemberContact, limit int) ([]*friend.GroupMemberContact, bool) {
	page := make([]*friend.GroupMemberContact, 0, len(members))
	size := 0
	for _, member := range members {
		if limit > 0 && len(page) == limit {
			return page, true
		}
		contact := &friend.GroupMemberContact{
			UserId: member.UserID, Account: member.Account, Name: member.Name,
			Avatar: member.Avatar, Role: member.Role, UpdateTime: member.UpdateTime,
		}
		entrySize := protowire.SizeTag(2) + protowire.SizeBytes(proto.Size(contact))
		if len(page) > 0 && size+entrySize > groupMemberPageByteBudget {
			return page, true
		}
		size += entrySize
		page = append(page, contact)
	}
	return page, false
}

func encodeGroupMemberCursor(groupID, userID int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d", groupID, userID)))
}

// decodeGroupMemberCursor 空游标表示第一页；游标属于其他群或格式不对时无效。
func decodeGroupMemberCursor(cursor string, groupID int64) (int64, bool) {
	if cursor == "" {
		return 0, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}
	group, user, found := strings.Cut(string(raw), ":")
	if !found || group != strconv.FormatInt(groupID, 10) {
		return 0, false
	}
	userID, err := strconv.ParseInt(user, 10, 64)
	if err != nil || userID <= 0 {
		return 0, false
	}
	return userID, true
}
