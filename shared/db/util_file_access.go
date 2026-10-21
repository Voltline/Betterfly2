package db

import (
	"encoding/hex"
	"strings"

	"gorm.io/gorm"
)

const fileHashHexLength = 128

// fileMessageTypes 的 content 引用对象存储中的文件，格式为 <hash> 或 <hash>.<suffix>。
var fileMessageTypes = map[string]struct{}{
	"image": {}, "gif": {}, "video": {}, "audio": {}, "voice": {}, "file": {},
}

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
