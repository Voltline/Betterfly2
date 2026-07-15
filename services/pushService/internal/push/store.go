package push

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"Betterfly2/shared/db"
	"gorm.io/gorm"
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
	database := db.DB()
	store := &GormStore{db: database}
	store.cleanupExpiredMessageDeliveries(context.Background())
	return store
}

func (s *GormStore) ListMessageTokens(ctx context.Context, targetUserIDs []int64, senderUserID int64, isGroup bool) ([]db.PushDeviceToken, error) {
	if len(targetUserIDs) == 0 {
		return nil, nil
	}
	query := s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Select("push_device_tokens.*").
		Where("push_device_tokens.user_id IN ? AND push_device_tokens.push_type = ? AND push_device_tokens.is_active = ?", targetUserIDs, PushTypeAPNs, true)
	if !isGroup {
		query = query.Joins(`LEFT JOIN friends ON friends.user_id = push_device_tokens.user_id
AND friends.friend_id = ? AND friends.is_delete = ?`, senderUserID, false).
			Where("friends.user_id IS NULL OR friends.is_notify = ?", true)
	}
	var tokens []db.PushDeviceToken
	err := query.Order("push_device_tokens.id ASC").Find(&tokens).Error
	return tokens, err
}

func (s *GormStore) ClaimMessageDeliveries(
	ctx context.Context,
	messageID int64,
	tokenIDs []int64,
	now time.Time,
	lease time.Duration,
) (map[int64]int, bool, error) {
	claims := make(map[int64]int, len(tokenIDs))
	if messageID <= 0 || len(tokenIDs) == 0 {
		return claims, false, nil
	}
	nowValue := now.UTC().Format(time.RFC3339Nano)
	leaseUntil := now.UTC().Add(lease).Format(time.RFC3339Nano)

	var placeholders []string
	args := make([]any, 0, len(tokenIDs)*8)
	for _, tokenID := range tokenIDs {
		placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, messageID, tokenID, DeliveryClaimed, 1, leaseUntil, nowValue, nowValue, nowValue)
	}
	insertSQL := `INSERT INTO push_message_deliveries
(message_id, token_id, status, attempt, lease_until, next_retry_at, created_at, updated_at)
VALUES ` + strings.Join(placeholders, ",") + `
ON CONFLICT (message_id, token_id) DO NOTHING RETURNING token_id, attempt`
	if err := scanDeliveryClaims(s.db.WithContext(ctx).Raw(insertSQL, args...), claims); err != nil {
		return nil, false, err
	}

	reclaimSQL := `UPDATE push_message_deliveries
SET status = ?, attempt = attempt + 1, lease_until = ?, updated_at = ?
WHERE message_id = ? AND token_id IN ? AND (
  (status = ? AND (next_retry_at = '' OR next_retry_at <= ?)) OR
  (status = ? AND lease_until <= ?)
)
RETURNING token_id, attempt`
	if err := scanDeliveryClaims(s.db.WithContext(ctx).Raw(
		reclaimSQL,
		DeliveryClaimed, leaseUntil, nowValue, messageID, tokenIDs,
		DeliveryRetryable, nowValue, DeliveryClaimed, nowValue,
	), claims); err != nil {
		return nil, false, err
	}

	var outstanding int64
	err := s.db.WithContext(ctx).Model(&db.PushMessageDelivery{}).
		Where("message_id = ? AND token_id IN ? AND status IN ?", messageID, tokenIDs, []string{DeliveryClaimed, DeliveryRetryable}).
		Count(&outstanding).Error
	if err != nil {
		return nil, false, err
	}
	if s.deliveryClaimCount.Add(uint64(len(tokenIDs)))%1000 < uint64(len(tokenIDs)) {
		s.cleanupExpiredMessageDeliveries(ctx)
	}
	return claims, outstanding > int64(len(claims)), nil
}

func scanDeliveryClaims(query *gorm.DB, claims map[int64]int) error {
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tokenID int64
		var attempt int
		if err := rows.Scan(&tokenID, &attempt); err != nil {
			return err
		}
		claims[tokenID] = attempt
	}
	return rows.Err()
}

func (s *GormStore) FinalizeMessageDeliveries(ctx context.Context, updates []DeliveryUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, 0, len(updates))
	args := make([]any, 0, len(updates)*7)
	invalidTokenIDs := make([]int64, 0)
	for _, update := range updates {
		placeholders = append(placeholders, "(CAST(? AS BIGINT), CAST(? AS BIGINT), CAST(? AS VARCHAR), CAST(? AS VARCHAR), CAST(? AS VARCHAR), CAST(? AS VARCHAR), CAST(? AS VARCHAR))")
		nextRetryAt := ""
		if !update.NextRetryAt.IsZero() {
			nextRetryAt = update.NextRetryAt.UTC().Format(time.RFC3339Nano)
		}
		args = append(args, update.MessageID, update.TokenID, update.Status, nextRetryAt, update.LastError, update.APNSID, now)
		if update.DeactivateToken {
			invalidTokenIDs = append(invalidTokenIDs, update.TokenID)
		}
	}
	query := `UPDATE push_message_deliveries AS delivery SET
status = update_values.status,
lease_until = '',
next_retry_at = update_values.next_retry_at,
last_error = update_values.last_error,
apns_id = update_values.apns_id,
updated_at = update_values.updated_at
FROM (VALUES ` + strings.Join(placeholders, ",") + `)
AS update_values(message_id, token_id, status, next_retry_at, last_error, apns_id, updated_at)
WHERE delivery.message_id = update_values.message_id AND delivery.token_id = update_values.token_id`
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(query, args...)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(updates)) {
			return fmt.Errorf("finalize push deliveries: updated %d of %d rows", result.RowsAffected, len(updates))
		}
		if len(invalidTokenIDs) == 0 {
			return nil
		}
		return tx.Model(&db.PushDeviceToken{}).
			Where("id IN ?", invalidTokenIDs).
			Updates(map[string]any{"is_active": false, "updated_at": now}).Error
	})
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

func (s *GormStore) DeactivateTokens(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&db.PushDeviceToken{}).
		Where("id IN ?", ids).
		Updates(map[string]any{
			"is_active":  false,
			"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}).Error
}
