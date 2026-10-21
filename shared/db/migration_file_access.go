package db

import "gorm.io/gorm"

// Keep v11 independent of future model edits: released migrations are immutable.
// 授权只来自上传或持有证明，不回填历史数据：老用户再次发送同一文件时客户端会照常重新上传一次。
func migrateFileAccessSchema(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS file_access_grants (
 file_hash varchar(128) NOT NULL, user_id bigint NOT NULL, created_at varchar(35) NOT NULL,
 PRIMARY KEY (file_hash, user_id))`,
		`CREATE INDEX IF NOT EXISTS idx_file_access_grants_user ON file_access_grants (user_id)`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

// v12 支撑下载授权的反查：文件消息按 content 中的哈希前缀、头像按哈希精确匹配。
// 类型列表写死以保持迁移不可变；fileMessageTypesSQL 必须与之一致，查询才能命中该部分索引。
func migrateFileDownloadIndexes(tx *gorm.DB) error {
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_messages_file_hash ON messages (split_part(content, '.', 1)) WHERE message_type IN ('image', 'gif', 'video', 'audio', 'voice', 'file')`,
		`CREATE INDEX IF NOT EXISTS idx_users_avatar ON users (avatar) WHERE avatar <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_groups_avatar ON groups (avatar) WHERE avatar <> ''`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
