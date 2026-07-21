package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MessageRecallWindow = 2 * time.Minute

type MessageRecallStatus int

const (
	MessageRecallOK MessageRecallStatus = iota
	MessageRecallNotFound
	MessageRecallForbidden
	MessageRecallAlreadyRecalled
	MessageRecallExpired
)

type MessageRecallOutcome struct {
	Message *Message
	Status  MessageRecallStatus
}

func StoreNewMessageWithDB(database *gorm.DB, fromUserID, toUserID int64, content, messageType, realFileName string, isGroup bool, clientMessageID string) (*Message, bool, error) {
	clientMessageID = strings.TrimSpace(clientMessageID)
	var clientMessageIDPtr *string
	if clientMessageID != "" {
		clientMessageIDPtr = &clientMessageID
	}
	message := &Message{
		ClientMessageID: clientMessageIDPtr,
		FromUserID:      fromUserID,
		ToUserID:        toUserID,
		Content:         content,
		Timestamp:       utils.NowTime(),
		MessageType:     messageType,
		RealFileName:    realFileName,
		IsGroup:         isGroup,
	}

	if clientMessageIDPtr == nil {
		if err := database.Create(message).Error; err != nil {
			return nil, false, err
		}
		return message, true, nil
	}

	result := database.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "from_user_id"}, {Name: "client_message_id"}},
		DoNothing: true,
	}).Create(message)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return message, true, nil
	}

	var existing Message
	if err := database.Where("from_user_id = ? AND client_message_id = ?", fromUserID, clientMessageID).First(&existing).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

func GetMessageByIDWithDB(database *gorm.DB, messageID int64) (*Message, error) {
	var message Message
	err := database.First(&message, "message_id = ?", messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func RecallMessageWithDB(database *gorm.DB, operatorUserID, messageID int64, now time.Time) (*MessageRecallOutcome, error) {
	if database == nil {
		return nil, errors.New("recall message database is nil")
	}
	if operatorUserID <= 0 || messageID <= 0 {
		return &MessageRecallOutcome{Status: MessageRecallNotFound}, nil
	}

	var message Message
	err := database.Clauses(clause.Locking{Strength: "UPDATE"}).First(&message, "message_id = ?", messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &MessageRecallOutcome{Status: MessageRecallNotFound}, nil
	}
	if err != nil {
		return nil, err
	}

	if message.FromUserID != operatorUserID {
		canRead, authErr := CanUserReadMessageWithDB(database, operatorUserID, &message)
		if authErr != nil {
			return nil, authErr
		}
		status := MessageRecallNotFound
		if canRead {
			status = MessageRecallForbidden
		}
		return &MessageRecallOutcome{Message: &message, Status: status}, nil
	}
	if message.IsRecalled {
		return &MessageRecallOutcome{Message: &message, Status: MessageRecallAlreadyRecalled}, nil
	}

	sentAt, err := time.Parse(time.RFC3339, message.Timestamp)
	if err != nil {
		return nil, err
	}
	if now.UTC().Sub(sentAt.UTC()) > MessageRecallWindow {
		return &MessageRecallOutcome{Message: &message, Status: MessageRecallExpired}, nil
	}

	recalledAt := now.UTC().Format(time.RFC3339)
	result := database.Model(&Message{}).
		Where("message_id = ? AND is_recalled = ?", messageID, false).
		Updates(map[string]any{
			"is_recalled": true,
			"recalled_at": recalledAt,
			"recalled_by": operatorUserID,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, errors.New("message recall update lost locked row")
	}
	message.IsRecalled = true
	message.RecalledAt = recalledAt
	message.RecalledBy = operatorUserID
	return &MessageRecallOutcome{Message: &message, Status: MessageRecallOK}, nil
}

const (
	DefaultSyncPageSize = 100
	MaxSyncPageSize     = 500
)

type SyncMessagesPage struct {
	Messages            []Message
	HasMore             bool
	NextCursorTimestamp string
	NextCursorMessageID int64
}

// GetSyncMessagesPage 获取稳定分页的同步消息。
// 当前会返回：
// 1. 发给该用户的单聊消息
// 2. 该用户当前已加入群组中的群聊消息
// 群聊消息会额外要求消息时间不早于该成员的入群时间，
// 避免把用户入群前的旧消息同步回来。
func GetSyncMessagesPageWithDB(database *gorm.DB, toUserID int64, cursorTimestamp string, cursorMessageID int64, pageSize int) (*SyncMessagesPage, error) {
	if pageSize <= 0 {
		pageSize = DefaultSyncPageSize
	}
	if pageSize > MaxSyncPageSize {
		pageSize = MaxSyncPageSize
	}
	var messages []Message
	err := database.Raw(`
SELECT *
FROM (
  SELECT
    m.message_id,
    m.from_user_id,
    m.to_user_id,
    m.content,
    m.timestamp,
    m.message_type,
    m.real_file_name,
    m.is_group,
    m.is_recalled,
    m.recalled_at,
    m.recalled_by
  FROM messages AS m
  WHERE m.is_group = FALSE
    AND m.to_user_id = ?
    AND (m.timestamp > ? OR (m.timestamp = ? AND m.message_id > ?))

  UNION ALL

  SELECT
    m.message_id,
    m.from_user_id,
    m.to_user_id,
    m.content,
    m.timestamp,
    m.message_type,
    m.real_file_name,
    m.is_group,
    m.is_recalled,
    m.recalled_at,
    m.recalled_by
  FROM group_members AS gm
  JOIN messages AS m
    ON m.to_user_id = gm.group_id
   AND m.is_group = TRUE
   AND m.timestamp >= COALESCE(NULLIF(gm.joined_at, ''), gm.update_time)
   AND (m.timestamp > ? OR (m.timestamp = ? AND m.message_id > ?))
  WHERE gm.user_id = ?
) AS sync_messages
ORDER BY timestamp ASC, message_id ASC
LIMIT ?
`, toUserID, cursorTimestamp, cursorTimestamp, cursorMessageID,
		cursorTimestamp, cursorTimestamp, cursorMessageID, toUserID, pageSize+1).Scan(&messages).Error
	if err != nil {
		return nil, err
	}

	return buildSyncMessagesPage(messages, pageSize), nil
}

func buildSyncMessagesPage(messages []Message, pageSize int) *SyncMessagesPage {
	page := &SyncMessagesPage{HasMore: len(messages) > pageSize}
	if page.HasMore {
		messages = messages[:pageSize]
	}
	page.Messages = messages
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		page.NextCursorTimestamp = last.Timestamp
		page.NextCursorMessageID = last.MessageID
	}
	return page
}

// CanUserReadMessage checks authorization against the current relationship
// state. Callers must invoke it even when the message entity came from cache.
func CanUserReadMessageWithDB(database *gorm.DB, requesterID int64, message *Message) (bool, error) {
	if requesterID <= 0 || message == nil {
		return false, nil
	}
	if !message.IsGroup {
		return requesterID == message.FromUserID || requesterID == message.ToUserID, nil
	}
	if requesterID == message.FromUserID {
		return true, nil
	}

	var count int64
	err := database.Model(&GroupMember{}).
		Where(
			"group_id = ? AND user_id = ? AND COALESCE(NULLIF(joined_at, ''), update_time) <= ?",
			message.ToUserID,
			requesterID,
			message.Timestamp,
		).
		Count(&count).Error
	return count > 0, err
}
