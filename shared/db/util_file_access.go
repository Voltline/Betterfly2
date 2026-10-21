package db

import (
	"encoding/hex"
	"strings"

	"gorm.io/gorm"
)

const fileHashHexLength = 128

// fileMessageTypes 的 content 引用对象存储中的文件，格式为 <hash> 或 <hash>.<suffix>。
// 修改时需要同步 fileMessageTypesSQL，并新增迁移重建 v12 的 idx_messages_file_hash。
var fileMessageTypes = map[string]struct{}{
	"image": {}, "gif": {}, "video": {}, "audio": {}, "voice": {}, "file": {},
}

const fileMessageTypesSQL = `('image', 'gif', 'video', 'audio', 'voice', 'file')`

func IsFileMessageType(messageType string) bool {
	_, ok := fileMessageTypes[messageType]
	return ok
}

// ValidFileHash 只接受客户端生成的小写 SHA-512 十六进制，与下载索引的精确匹配保持一致。
func ValidFileHash(value string) bool {
	if len(value) != fileHashHexLength || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// FileHashFromContent 从文件消息 content 中取出文件哈希。
func FileHashFromContent(content string) (string, bool) {
	fileHash, _, _ := strings.Cut(content, ".")
	return fileHash, ValidFileHash(fileHash)
}

// CanUseFileWithDB 判断用户能否在消息或头像中引用该文件：文件已校验且用户已证明持有。
func CanUseFileWithDB(database *gorm.DB, fileHash string, userID int64) (bool, error) {
	if userID <= 0 || !ValidFileHash(fileHash) {
		return false, nil
	}
	var count int64
	err := database.Table("file_access_grants AS g").
		Joins("JOIN file_metadata AS f ON f.file_hash = g.file_hash AND f.is_verified = TRUE").
		Where("g.file_hash = ? AND g.user_id = ?", fileHash, userID).
		Count(&count).Error
	return count > 0, err
}

// fileMessageReadableSQL 与 CanUserReadMessageWithDB 的规则一致：私聊只限收发双方；
// 频道公开或已订阅；讨论区沿用 DiscussionReadPredicate；普通群要求入群时间不晚于消息，
// 发送者本人始终可见。除首个 hash 外，其余 7 个占位符都是请求者ID。
const fileMessageReadableSQL = `SELECT EXISTS (
 SELECT 1 FROM messages AS m
 LEFT JOIN channel_settings AS channel ON channel.group_id = m.to_user_id AND m.is_group = TRUE
 WHERE split_part(m.content, '.', 1) = ? AND m.message_type IN ` + fileMessageTypesSQL + ` AND m.is_recalled = FALSE
   AND (
     (m.is_group = FALSE AND (m.from_user_id = ? OR m.to_user_id = ?))
     OR (m.is_group = TRUE AND channel.group_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM groups AS channel_group WHERE channel_group.group_id = channel.group_id AND channel_group.is_delete = FALSE
         AND (channel.is_public = TRUE OR EXISTS (SELECT 1 FROM group_members AS viewer WHERE viewer.group_id = channel.group_id AND viewer.user_id = ?))))
     OR (m.is_group = TRUE AND channel.group_id IS NULL AND m.discussion_root_message_id > 0
       AND EXISTS (SELECT 1 FROM group_members AS gm WHERE gm.group_id = m.to_user_id AND gm.user_id = ?)
       AND ` + DiscussionReadPredicate + `)
     OR (m.is_group = TRUE AND channel.group_id IS NULL AND m.discussion_root_message_id = 0 AND (m.from_user_id = ?
       OR EXISTS (SELECT 1 FROM group_members AS gm WHERE gm.group_id = m.to_user_id AND gm.user_id = ?
         AND COALESCE(NULLIF(gm.joined_at, ''), gm.update_time) <= m.timestamp)))
   )
)`

// CanDownloadFileWithDB 判断用户能否下载文件：持有者、任意用户/群/频道头像，
// 或者用户当前能看到一条引用该文件且未撤回的文件消息。撤回或退群后最后一条规则自然失效。
func CanDownloadFileWithDB(database *gorm.DB, fileHash string, userID int64) (bool, error) {
	if userID <= 0 || !ValidFileHash(fileHash) {
		return false, nil
	}
	var allowed bool
	err := database.Raw(`SELECT EXISTS (SELECT 1 FROM file_access_grants WHERE file_hash = ? AND user_id = ?)
 OR EXISTS (SELECT 1 FROM users WHERE avatar = ?)
 OR EXISTS (SELECT 1 FROM groups WHERE avatar = ?)`, fileHash, userID, fileHash, fileHash).Scan(&allowed).Error
	if err != nil || allowed {
		return allowed, err
	}
	err = database.Raw(fileMessageReadableSQL, fileHash, userID, userID, userID, userID, userID, userID, userID).Scan(&allowed).Error
	return allowed, err
}

func CanDownloadFile(fileHash string, userID int64) (bool, error) {
	return CanDownloadFileWithDB(DB(), fileHash, userID)
}
