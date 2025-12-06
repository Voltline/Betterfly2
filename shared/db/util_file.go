package db

import (
	"Betterfly2/shared/utils"
	"errors"

	"gorm.io/gorm"
)

// StoreFileMetadata 存储文件元数据
func StoreFileMetadata(fileHash string, fileSize int64, storagePath string) error {
	nowTime := utils.NowTime()
	fileMetadata := &FileMetadata{
		FileHash:    fileHash,
		FileSize:    fileSize,
		StoragePath: storagePath,
		CreatedAt:   nowTime,
		UpdatedAt:   nowTime,
	}
	err := DB().Create(fileMetadata).Error
	return err
}

// GetFileMetadata 根据文件哈希获取文件元数据
func GetFileMetadata(fileHash string) (*FileMetadata, error) {
	var fileMetadata FileMetadata
	err := DB().First(&fileMetadata, "file_hash = ?", fileHash).Error
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
	err := DB().Model(&FileMetadata{}).Where("file_hash = ?", fileHash).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateFileMetadata 更新文件元数据（主要用于更新时间戳）
func UpdateFileMetadata(fileHash string, fileSize int64, storagePath string) error {
	nowTime := utils.NowTime()
	return DB().Model(&FileMetadata{}).
		Where("file_hash = ?", fileHash).
		Updates(map[string]interface{}{
			"file_size":    fileSize,
			"storage_path": storagePath,
			"updated_at":   nowTime,
		}).Error
}

// DeleteFileMetadata 删除文件元数据
func DeleteFileMetadata(fileHash string) error {
	return DB().Delete(&FileMetadata{}, "file_hash = ?", fileHash).Error
}
