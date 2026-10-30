package bot

import (
	"context"
	"errors"
	"fmt"

	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"

	"gorm.io/gorm"
)

// Store 是机器人读取消息与会话上下文所需的查询；写消息一律经StorageService完成。
type Store interface {
	Message(ctx context.Context, messageID int64) (*db.Message, error)
	CanPublishGroup(ctx context.Context, groupID, botID int64) (bool, error)
	ReplyExists(ctx context.Context, botID int64, clientMessageID string) (bool, error)
	// DirectHistory 与GroupHistory按message_id从新到旧返回不晚于beforeOrAt的消息。
	DirectHistory(ctx context.Context, userID, botID, beforeOrAt int64, limit int) ([]db.Message, error)
	GroupHistory(ctx context.Context, groupID, beforeOrAt int64, limit int) ([]db.Message, error)
	UserNames(ctx context.Context, userIDs []int64) (map[int64]string, error)
	GroupName(ctx context.Context, groupID int64) (string, error)
}

type GormStore struct{ database *gorm.DB }

func NewGormStore(database *gorm.DB) *GormStore { return &GormStore{database: database} }

func (s *GormStore) Message(ctx context.Context, messageID int64) (*db.Message, error) {
	return db.GetMessageByIDWithDB(s.database.WithContext(ctx), messageID)
}

func (s *GormStore) CanPublishGroup(ctx context.Context, groupID, botID int64) (bool, error) {
	return db.CanPublishGroupMessageWithDB(s.database.WithContext(ctx), groupID, botID)
}

func (s *GormStore) ReplyExists(ctx context.Context, botID int64, clientMessageID string) (bool, error) {
	var count int64
	err := s.database.WithContext(ctx).Model(&db.Message{}).
		Where("from_user_id = ? AND client_message_id = ?", botID, clientMessageID).Limit(1).Count(&count).Error
	return count > 0, err
}

const historyColumns = "message_id, from_user_id, to_user_id, content, caption, message_type, is_group, is_recalled"

func (s *GormStore) DirectHistory(ctx context.Context, userID, botID, beforeOrAt int64, limit int) ([]db.Message, error) {
	var messages []db.Message
	err := s.database.WithContext(ctx).Select(historyColumns).
		Where("is_group = FALSE AND message_id <= ?", beforeOrAt).
		Where("(from_user_id = ? AND to_user_id = ?) OR (from_user_id = ? AND to_user_id = ?)", userID, botID, botID, userID).
		Order("message_id DESC").Limit(limit).Find(&messages).Error
	return messages, err
}

// GroupHistory 只取群主时间线，不含讨论串回复和频道转发卡片。
func (s *GormStore) GroupHistory(ctx context.Context, groupID, beforeOrAt int64, limit int) ([]db.Message, error) {
	var messages []db.Message
	err := s.database.WithContext(ctx).Select(historyColumns).
		Where("is_group = TRUE AND to_user_id = ? AND message_id <= ?", groupID, beforeOrAt).
		Where("discussion_root_message_id = 0 AND source_channel_message_id = 0").
		Order("message_id DESC").Limit(limit).Find(&messages).Error
	return messages, err
}

func (s *GormStore) UserNames(ctx context.Context, userIDs []int64) (map[int64]string, error) {
	names := make(map[int64]string, len(userIDs))
	if len(userIDs) == 0 {
		return names, nil
	}
	var users []db.User
	if err := s.database.WithContext(ctx).Select("id, name").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		names[user.ID] = user.Name
	}
	return names, nil
}

func (s *GormStore) GroupName(ctx context.Context, groupID int64) (string, error) {
	var group db.Group
	err := s.database.WithContext(ctx).Select("group_id, name").Where("group_id = ?", groupID).Take(&group).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return group.Name, err
}

// Identity 是机器人在users表中的资料。
type Identity struct {
	UserID  int64
	Account string
	Name    string
	Avatar  string
}

// EnsureBotUser 确保users表里有机器人账号。用显式ID插入且不推进users_id_seq，
// 否则序列会跳到机器人ID附近；密码哈希为空，机器人无法登录。
// ID已被其他账号占用或账号名已被其他用户使用时返回错误，避免冒用真实用户。
func EnsureBotUser(ctx context.Context, database *gorm.DB, identity Identity) error {
	database = database.WithContext(ctx)
	if err := database.Exec(
		"INSERT INTO users (id, account, name, update_time, avatar, password_hash) VALUES (?, ?, ?, ?, ?, '') ON CONFLICT DO NOTHING",
		identity.UserID, identity.Account, identity.Name, utils.NowTime(), identity.Avatar,
	).Error; err != nil {
		return fmt.Errorf("创建机器人账号失败: %w", err)
	}
	var user db.User
	err := database.Select("id, account, name, avatar, password_hash").Where("id = ?", identity.UserID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("机器人账号名%q已被其他用户使用", identity.Account)
	}
	if err != nil {
		return err
	}
	if user.Account != identity.Account || user.PasswordHash != "" {
		return fmt.Errorf("用户ID %d 已被账号%q占用，不是机器人账号", identity.UserID, user.Account)
	}
	if user.Name == identity.Name && user.Avatar == identity.Avatar {
		return nil
	}
	// update_time变化后客户端会按资料同步刷新机器人名字和头像。
	return database.Model(&db.User{}).Where("id = ?", identity.UserID).
		Updates(map[string]any{"name": identity.Name, "avatar": identity.Avatar, "update_time": utils.NowTime()}).Error
}
