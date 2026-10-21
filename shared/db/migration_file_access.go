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
