package chatbot

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type Store interface {
	Audit(record AuditRecord) error
	GetUser(userID int64) (UserInfo, error)
	GetGroup(groupID int64) (GroupInfo, error)
	GetGroupMembers(groupID int64) ([]db.GroupMemberContact, error)
	GetRecentDirectMessages(userID, peerUserID int64, limit int) ([]MessageInfo, error)
	GetRecentGroupMessages(groupID int64, limit int) ([]MessageInfo, error)
}

type GormStore struct{}

func NewGormStore() *GormStore {
	_ = db.DB(&db.ChatbotAuditLog{})
	return &GormStore{}
}

func (s *GormStore) Audit(record AuditRecord) error {
	if record.Status == "" {
		record.Status = AuditStatusOK
	}
	return db.DB().Create(&db.ChatbotAuditLog{
		BotID:      record.BotID,
		Action:     record.Action,
		TargetType: record.TargetType,
		TargetID:   record.TargetID,
		Status:     record.Status,
		Error:      record.Error,
		CreatedAt:  utils.NowTime(),
	}).Error
}

func (s *GormStore) GetUser(userID int64) (UserInfo, error) {
	user, err := db.GetUserById(userID)
	if err != nil {
		return UserInfo{}, err
	}
	if user == nil {
		return UserInfo{}, errors.New("user not found")
	}
	return userFromModel(user), nil
}

func (s *GormStore) GetGroup(groupID int64) (GroupInfo, error) {
	group, err := db.GetGroupByID(groupID)
	if err != nil {
		return GroupInfo{}, err
	}
	if group == nil {
		return GroupInfo{}, errors.New("group not found")
	}
	return groupFromModel(group), nil
}

func (s *GormStore) GetGroupMembers(groupID int64) ([]db.GroupMemberContact, error) {
	if group, err := db.GetGroupByID(groupID); err != nil {
		return nil, err
	} else if group == nil {
		return nil, errors.New("group not found")
	}
	return db.GetGroupMembers(groupID)
}

func (s *GormStore) GetRecentDirectMessages(userID, peerUserID int64, limit int) ([]MessageInfo, error) {
	limit = normalizeLimit(limit)
	var messages []db.Message
	err := db.DB().
		Where("is_group = ? AND ((from_user_id = ? AND to_user_id = ?) OR (from_user_id = ? AND to_user_id = ?))", false, userID, peerUserID, peerUserID, userID).
		Order("timestamp DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, err
	}
	return reverseMessages(messages), nil
}

func (s *GormStore) GetRecentGroupMessages(groupID int64, limit int) ([]MessageInfo, error) {
	limit = normalizeLimit(limit)
	var count int64
	if err := db.DB().Model(&db.Group{}).Where("group_id = ? AND is_delete = ?", groupID, false).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, errors.New("group not found")
	}

	var messages []db.Message
	err := db.DB().
		Where("is_group = ? AND to_user_id = ?", true, groupID).
		Order("timestamp DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, err
	}
	return reverseMessages(messages), nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func reverseMessages(messages []db.Message) []MessageInfo {
	result := make([]MessageInfo, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		result = append(result, messageFromModel(messages[i]))
	}
	return result
}

func notImplemented(action string) error {
	return fmt.Errorf("%s is not implemented in chatbotService phase 1", action)
}

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
