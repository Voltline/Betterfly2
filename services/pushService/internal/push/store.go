package push

import (
	"context"
	"time"

	"Betterfly2/shared/db"
	"gorm.io/gorm"
)

type GormStore struct {
	db *gorm.DB
}

func NewGormStore() *GormStore {
	database := db.DB(&db.PushDeviceToken{})
	return &GormStore{db: database}
}

func (s *GormStore) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *GormStore) RegisterVoIPToken(ctx context.Context, userID int64, deviceID, token, environment, bundleID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"(environment = ? AND push_type = ? AND token = ?) OR (user_id = ? AND device_id = ? AND environment = ? AND push_type = ?)",
			environment, PushTypeVoIP, token, userID, deviceID, environment, PushTypeVoIP,
		).Delete(&db.PushDeviceToken{}).Error; err != nil {
			return err
		}
		return tx.Create(&db.PushDeviceToken{
			UserID: userID, DeviceID: deviceID, Token: token, Environment: environment,
			PushType: PushTypeVoIP, BundleID: bundleID, IsActive: true,
			CreatedAt: now, UpdatedAt: now,
		}).Error
	})
}

func (s *GormStore) UnregisterVoIPToken(ctx context.Context, userID int64, deviceID, environment string) (bool, error) {
	result := s.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ? AND environment = ? AND push_type = ?", userID, deviceID, environment, PushTypeVoIP).
		Delete(&db.PushDeviceToken{})
	return result.RowsAffected > 0, result.Error
}

func (s *GormStore) ListActiveVoIPTokens(ctx context.Context, userID int64) ([]db.PushDeviceToken, error) {
	var tokens []db.PushDeviceToken
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND push_type = ? AND is_active = ?", userID, PushTypeVoIP, true).
		Order("updated_at DESC").
		Find(&tokens).Error
	return tokens, err
}

func (s *GormStore) DeactivateToken(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"is_active":  false,
			"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}).Error
}
