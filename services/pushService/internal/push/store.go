package push

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"Betterfly2/shared/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GormStore struct {
	db                 *gorm.DB
	deliveryClaimCount atomic.Uint64
}

const messageDeliveryRetention = 30 * 24 * time.Hour

func (s *GormStore) MessagePresentation(ctx context.Context, senderUserID, conversationID int64, isGroup bool) (MessagePresentation, error) {
	var sender db.User
	senderErr := s.db.WithContext(ctx).Select("id", "name", "avatar").First(&sender, senderUserID).Error
	if senderErr != nil && !errors.Is(senderErr, gorm.ErrRecordNotFound) {
		return MessagePresentation{}, senderErr
	}
	senderName := strings.TrimSpace(sender.Name)
	if senderName == "" {
		senderName = "用户 " + strconv.FormatInt(senderUserID, 10)
	}
	presentation := MessagePresentation{
		Title: senderName, SenderName: senderName, SenderAvatar: sender.Avatar,
		Avatar: sender.Avatar, ConversationName: senderName, ConversationAvatar: sender.Avatar,
	}
	if !isGroup {
		return presentation, nil
	}
	var group db.Group
	groupErr := s.db.WithContext(ctx).
		Select("group_id", "name", "avatar").
		Where("group_id = ? AND is_delete = ?", conversationID, false).
		First(&group).Error
	if groupErr != nil && !errors.Is(groupErr, gorm.ErrRecordNotFound) {
		return MessagePresentation{}, groupErr
	}
	groupName := strings.TrimSpace(group.Name)
	if groupName == "" {
		groupName = "群聊 " + strconv.FormatInt(conversationID, 10)
	}
	presentation.Title = groupName
	presentation.GroupName = groupName
	presentation.Avatar = group.Avatar
	presentation.AvatarIsGroup = true
	presentation.ConversationName = groupName
	presentation.ConversationAvatar = group.Avatar
	return presentation, nil
}

func NewGormStore() *GormStore {
	database := db.DB(&db.PushDeviceToken{}, &db.PushDebugAudit{}, &db.PushMessageDelivery{})
	store := &GormStore{db: database}
	store.cleanupExpiredMessageDeliveries(context.Background())
	return store
}

func (s *GormStore) ClaimMessageDelivery(ctx context.Context, messageID, tokenID int64) (bool, error) {
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&db.PushMessageDelivery{
		MessageID: messageID,
		TokenID:   tokenID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if s.deliveryClaimCount.Add(1)%1000 == 0 {
		s.cleanupExpiredMessageDeliveries(ctx)
	}
	return result.RowsAffected == 1, result.Error
}

func (s *GormStore) ReleaseMessageDelivery(ctx context.Context, messageID, tokenID int64) error {
	return s.db.WithContext(ctx).
		Where("message_id = ? AND token_id = ?", messageID, tokenID).
		Delete(&db.PushMessageDelivery{}).Error
}

func (s *GormStore) cleanupExpiredMessageDeliveries(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-messageDeliveryRetention).Format(time.RFC3339Nano)
	_ = s.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&db.PushMessageDelivery{}).Error
}

func (s *GormStore) FindTokens(ctx context.Context, filter TokenFilter) ([]db.PushDeviceToken, error) {
	query := s.db.WithContext(ctx).Model(&db.PushDeviceToken{})
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.DeviceID != "" {
		query = query.Where("device_id ILIKE ?", "%"+filter.DeviceID+"%")
	}
	if filter.Environment != "" {
		query = query.Where("environment = ?", filter.Environment)
	}
	if filter.PushType != "" {
		query = query.Where("push_type = ?", filter.PushType)
	}
	if filter.ActiveOnly {
		query = query.Where("is_active = ?", true)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var tokens []db.PushDeviceToken
	err := query.Order("updated_at DESC").Limit(limit).Find(&tokens).Error
	return tokens, err
}

func (s *GormStore) BroadcastAudience(ctx context.Context, environment string) (int64, int64, error) {
	query := s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Where("push_type = ? AND is_active = ?", PushTypeAPNs, true)
	if environment = strings.ToLower(strings.TrimSpace(environment)); environment != "" {
		query = query.Where("environment = ?", environment)
	}
	var maxID int64
	if err := query.Select("COALESCE(MAX(id), 0)").Scan(&maxID).Error; err != nil {
		return 0, 0, err
	}
	if maxID == 0 {
		return 0, 0, nil
	}
	var count int64
	if err := query.Where("id <= ?", maxID).Count(&count).Error; err != nil {
		return 0, 0, err
	}
	return count, maxID, nil
}

func (s *GormStore) ListBroadcastTokens(ctx context.Context, environment string, afterID, throughID int64, limit int) ([]db.PushDeviceToken, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	query := s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Where("push_type = ? AND is_active = ? AND id > ? AND id <= ?", PushTypeAPNs, true, afterID, throughID)
	if environment = strings.ToLower(strings.TrimSpace(environment)); environment != "" {
		query = query.Where("environment = ?", environment)
	}
	var tokens []db.PushDeviceToken
	err := query.Order("id ASC").Limit(limit).Find(&tokens).Error
	return tokens, err
}

func (s *GormStore) GetToken(ctx context.Context, id int64) (db.PushDeviceToken, error) {
	var token db.PushDeviceToken
	err := s.db.WithContext(ctx).First(&token, id).Error
	return token, err
}

func (s *GormStore) CreateDebugAudit(ctx context.Context, audit *db.PushDebugAudit) error {
	return s.db.WithContext(ctx).Create(audit).Error
}

func (s *GormStore) ListDebugAudits(ctx context.Context, limit int) ([]db.PushDebugAudit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var audits []db.PushDebugAudit
	err := s.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&audits).Error
	return audits, err
}

func (s *GormStore) TokenSummary(ctx context.Context) (TokenSummary, error) {
	var summary TokenSummary
	queries := []struct {
		field *int64
		where string
		value any
	}{
		{&summary.Total, "", nil},
		{&summary.Active, "is_active = ?", true},
		{&summary.APNs, "push_type = ?", PushTypeAPNs},
		{&summary.VoIP, "push_type = ?", PushTypeVoIP},
		{&summary.Sandbox, "environment = ?", "sandbox"},
		{&summary.Production, "environment = ?", "production"},
	}
	for _, item := range queries {
		query := s.db.WithContext(ctx).Model(&db.PushDeviceToken{})
		if item.where != "" {
			query = query.Where(item.where, item.value)
		}
		if err := query.Count(item.field).Error; err != nil {
			return TokenSummary{}, err
		}
	}
	return summary, nil
}

func (s *GormStore) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *GormStore) RegisterToken(ctx context.Context, userID int64, deviceID, token, environment, pushType, bundleID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"(environment = ? AND push_type = ? AND token = ?) OR (user_id = ? AND device_id = ? AND environment = ? AND push_type = ?)",
			environment, pushType, token, userID, deviceID, environment, pushType,
		).Delete(&db.PushDeviceToken{}).Error; err != nil {
			return err
		}
		return tx.Create(&db.PushDeviceToken{
			UserID: userID, DeviceID: deviceID, Token: token, Environment: environment,
			PushType: pushType, BundleID: bundleID, IsActive: true,
			CreatedAt: now, UpdatedAt: now,
		}).Error
	})
}

func (s *GormStore) UnregisterToken(ctx context.Context, userID int64, deviceID, environment, pushType string) (bool, error) {
	result := s.db.WithContext(ctx).
		Where("user_id = ? AND device_id = ? AND environment = ? AND push_type = ?", userID, deviceID, environment, pushType).
		Delete(&db.PushDeviceToken{})
	return result.RowsAffected > 0, result.Error
}

func (s *GormStore) ListActiveTokens(ctx context.Context, userID int64, pushType string) ([]db.PushDeviceToken, error) {
	var tokens []db.PushDeviceToken
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND push_type = ? AND is_active = ?", userID, pushType, true).
		Order("updated_at DESC").
		Find(&tokens).Error
	return tokens, err
}

func (s *GormStore) MessageNotificationsEnabled(ctx context.Context, targetUserID, senderUserID int64, isGroup bool) (bool, error) {
	if isGroup {
		return true, nil
	}
	var friend db.Friend
	err := s.db.WithContext(ctx).
		Select("is_notify").
		Where("user_id = ? AND friend_id = ? AND is_delete = ?", targetUserID, senderUserID, false).
		First(&friend).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return friend.IsNotify, nil
}

func (s *GormStore) DeactivateToken(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"is_active":  false,
			"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}).Error
}
