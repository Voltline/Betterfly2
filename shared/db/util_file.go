package db

import (
	"Betterfly2/shared/utils"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StoreFileMetadata 存储文件元数据
func StoreFileMetadata(fileHash string, fileSize int64, storagePath string) error {
	return storeFileMetadataWithDB(DB(), fileHash, fileSize, storagePath)
}

// StoreVerifiedFile 在同一事务中标记文件已校验并授权上传者，避免出现无主的已校验文件。
func StoreVerifiedFile(fileHash string, fileSize int64, storagePath string, userID int64) error {
	return StoreVerifiedFileWithDB(DB(), fileHash, fileSize, storagePath, userID)
}

func StoreVerifiedFileWithDB(database *gorm.DB, fileHash string, fileSize int64, storagePath string, userID int64) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := storeFileMetadataWithDB(tx, fileHash, fileSize, storagePath); err != nil {
			return err
		}
		return GrantFileAccessWithDB(tx, fileHash, userID)
	})
}

// GrantFileAccess 记录用户持有该文件；重复授权是幂等的。
func GrantFileAccess(fileHash string, userID int64) error {
	return GrantFileAccessWithDB(DB(), fileHash, userID)
}

func GrantFileAccessWithDB(database *gorm.DB, fileHash string, userID int64) error {
	return database.Clauses(clause.OnConflict{DoNothing: true}).Create(&FileAccessGrant{
		FileHash: fileHash, UserID: userID, CreatedAt: utils.NowTime(),
	}).Error
}

// HasFileAccess 检查用户是否已证明持有该文件。
func HasFileAccess(fileHash string, userID int64) (bool, error) {
	return HasFileAccessWithDB(DB(), fileHash, userID)
}

func HasFileAccessWithDB(database *gorm.DB, fileHash string, userID int64) (bool, error) {
	var count int64
	err := database.Model(&FileAccessGrant{}).Where("file_hash = ? AND user_id = ?", fileHash, userID).Count(&count).Error
	return count > 0, err
}

func storeFileMetadataWithDB(database *gorm.DB, fileHash string, fileSize int64, storagePath string) error {
	nowTime := utils.NowTime()
	fileMetadata := &FileMetadata{
		FileHash:    fileHash,
		FileSize:    fileSize,
		StoragePath: storagePath,
		IsVerified:  true,
		CreatedAt:   nowTime,
		UpdatedAt:   nowTime,
	}
	return database.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_hash"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"file_size":    fileSize,
			"storage_path": storagePath,
			"is_verified":  true,
			"updated_at":   nowTime,
		}),
	}).Create(fileMetadata).Error
}

// UpsertPendingFileMetadata 记录待验证文件元数据；已校验的记录不会被降级为待验证。
func UpsertPendingFileMetadata(fileHash string, fileSize int64, storagePath string) error {
	return UpsertPendingFileMetadataWithDB(DB(), fileHash, fileSize, storagePath)
}

func UpsertPendingFileMetadataWithDB(database *gorm.DB, fileHash string, fileSize int64, storagePath string) error {
	nowTime := utils.NowTime()
	fileMetadata := &FileMetadata{
		FileHash:    fileHash,
		FileSize:    fileSize,
		StoragePath: storagePath,
		IsVerified:  false,
		CreatedAt:   nowTime,
		UpdatedAt:   nowTime,
	}

	return database.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_hash"}},
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: "file_metadata", Name: "is_verified"}, Value: false},
		}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"file_size":    fileSize,
			"storage_path": storagePath,
			"updated_at":   nowTime,
		}),
	}).Create(fileMetadata).Error
}

// GetFileMetadata 根据文件哈希获取文件元数据
func GetFileMetadata(fileHash string) (*FileMetadata, error) {
	return GetFileMetadataWithDB(DB(), fileHash)
}

func GetFileMetadataWithDB(database *gorm.DB, fileHash string) (*FileMetadata, error) {
	var fileMetadata FileMetadata
	err := database.First(&fileMetadata, "file_hash = ?", fileHash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &fileMetadata, nil
}

// FileExists 检查文件是否存在
func FileExists(fileHash string) (bool, error) {
	var count int64
	err := DB().Model(&FileMetadata{}).Where("file_hash = ? AND is_verified = ?", fileHash, true).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateFileMetadata 更新文件元数据（主要用于更新时间戳）
func UpdateFileMetadata(fileHash string, fileSize int64, storagePath string) error {
	nowTime := utils.NowTime()
	result := DB().Model(&FileMetadata{}).
		Where("file_hash = ?", fileHash).
		Updates(map[string]interface{}{
			"file_size":    fileSize,
			"storage_path": storagePath,
			"is_verified":  true,
			"updated_at":   nowTime,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteFileMetadata 删除文件元数据
func DeleteFileMetadata(fileHash string) error {
	return DB().Delete(&FileMetadata{}, "file_hash = ?", fileHash).Error
}
