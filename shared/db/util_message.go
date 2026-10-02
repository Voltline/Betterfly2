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

var ErrInvalidReply = errors.New("reply target is unavailable or outside the conversation")

type MessageRecallStatus int

const (
	MessageRecallOK MessageRecallStatus = iota
	MessageRecallNotFound
	MessageRecallForbidden
	MessageRecallAlreadyRecalled
	MessageRecallExpired
)

type MessageRecallOutcome struct {
	Message        *Message
	Status         MessageRecallStatus
	DiscussionRoot *Message
}

func StoreNewMessageWithDB(database *gorm.DB, fromUserID, toUserID int64, content, messageType, realFileName string, isGroup bool, clientMessageID, caption string, replyID int64, threadIDs ...int64) (*Message, bool, error) {
	var threadID int64
	if len(threadIDs) > 0 {
		threadID = threadIDs[0]
	}
	if err := utils.ValidateImageCaption(messageType, caption); err != nil {
		return nil, false, err
	}
	clientMessageID = strings.TrimSpace(clientMessageID)
	var clientMessageIDPtr *string
	if clientMessageID != "" {
		clientMessageIDPtr = &clientMessageID
	}
	if replyID < 0 || threadID < 0 || (threadID > 0 && !isGroup) {
		return nil, false, ErrInvalidReply
	}
	if replyID > 0 || threadID > 0 {
		// A replay returns the original canonical message, even if its reference
		// is no longer readable after leaving a group. Never rewrite its target.
		if clientMessageIDPtr != nil {
			var existing Message
			err := database.Where("from_user_id = ? AND client_message_id = ?", fromUserID, clientMessageID).First(&existing).Error
			if err == nil {
				return &existing, false, nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, err
			}
		}
		if threadID > 0 {
			if err := ValidateDiscussionCommentWithDB(database, fromUserID, toUserID, threadID, replyID); err != nil {
				return nil, false, err
			}
		} else if err := validateReplyWithDB(database, fromUserID, toUserID, isGroup, replyID); err != nil {
			return nil, false, err
		}
	}
	message := &Message{
		ClientMessageID:         clientMessageIDPtr,
		FromUserID:              fromUserID,
		ToUserID:                toUserID,
		Content:                 content,
		Caption:                 caption,
		ReplyToMessageID:        replyID,
		DiscussionRootMessageID: threadID,
		Timestamp:               utils.NowTime(),
		MessageType:             messageType,
		RealFileName:            realFileName,
		IsGroup:                 isGroup,
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

func validateReplyWithDB(database *gorm.DB, senderID, targetID int64, isGroup bool, replyID int64) error {
	message, err := GetMessageByIDWithDB(database, replyID)
	if err != nil {
		return err
	}
	if message == nil || message.IsGroup != isGroup {
		return ErrInvalidReply
	}
	if isGroup {
		if message.ToUserID != targetID {
			return ErrInvalidReply
		}
		if message.DiscussionRootMessageID > 0 {
			settings, err := GetChannelSettingsWithDB(database, targetID)
			if err != nil {
				return err
			}
			// Linked channel posts remain ordinary quotable announcements.
			// Only comments/cards in the discussion group require a thread ID.
			if settings == nil {
				return ErrInvalidReply
			}
		}
	} else if !((message.FromUserID == senderID && message.ToUserID == targetID) ||
		(message.FromUserID == targetID && message.ToUserID == senderID)) {
		return ErrInvalidReply
	}
	allowed, err := CanUserReadMessageWithDB(database, senderID, message)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrInvalidReply
	}
	return nil
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

	channelManager := false
	discussionManager := false
	if message.IsGroup {
		settings, settingsErr := GetChannelSettingsWithDB(database, message.ToUserID)
		if settingsErr != nil {
			return nil, settingsErr
		}
		if settings != nil {
			view, viewErr := GetChannelWithDB(database, operatorUserID, message.ToUserID, "")
			if errors.Is(viewErr, ErrChannelNotFound) {
				return &MessageRecallOutcome{Status: MessageRecallNotFound}, nil
			}
			if viewErr != nil {
				return nil, viewErr
			}
			channelManager = view.MyRole == "owner" || view.MyRole == "admin"
			if !channelManager {
				return &MessageRecallOutcome{Status: MessageRecallForbidden}, nil
			}
		}
		if settings == nil && message.DiscussionRootMessageID > 0 {
			allowed, err := CanReadDiscussionMessageWithDB(database, operatorUserID, &message)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return &MessageRecallOutcome{Status: MessageRecallNotFound}, nil
			}
			role, err := groupRoleTx(database, message.ToUserID, operatorUserID)
			if err != nil {
				return nil, err
			}
			discussionManager = canManageGroup(role)
			if message.SourceChannelMessageID > 0 && !discussionManager {
				return &MessageRecallOutcome{Status: MessageRecallForbidden}, nil
			}
		}
	}
	if !channelManager && !discussionManager && message.FromUserID != operatorUserID {
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
	if !channelManager && !discussionManager && now.UTC().Sub(sentAt.UTC()) > MessageRecallWindow {
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
	if channelManager {
		// The message row lock serializes pinning against recall. Clearing only
		// this ID cannot erase a concurrently selected different announcement.
		if err := database.Model(&ChannelSettings{}).Where("group_id = ? AND pinned_message_id = ?", message.ToUserID, message.MessageID).Update("pinned_message_id", 0).Error; err != nil {
			return nil, err
		}
	}
	message.IsRecalled = true
	message.RecalledAt = recalledAt
	message.RecalledBy = operatorUserID
	outcome := &MessageRecallOutcome{Message: &message, Status: MessageRecallOK}
	if channelManager && message.DiscussionRootMessageID > 0 {
		var root Message
		err := database.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, "message_id = ? AND source_channel_message_id = ?", message.DiscussionRootMessageID, message.MessageID).Error
		if err != nil {
			return nil, err
		}
		if !root.IsRecalled {
			if err := database.Model(&Message{}).Where("message_id = ?", root.MessageID).Updates(map[string]any{"is_recalled": true, "recalled_at": recalledAt, "recalled_by": operatorUserID}).Error; err != nil {
				return nil, err
			}
			root.IsRecalled, root.RecalledAt, root.RecalledBy = true, recalledAt, operatorUserID
		}
		outcome.DiscussionRoot = &root
	}
	return outcome, nil
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
// 1. 该用户发送或接收的单聊消息
// 2. 该用户当前已加入群组中的群聊消息
// 群聊消息会额外要求消息时间不早于该成员的入群时间，
// 避免把用户入群前的旧消息同步回来。频道订阅者可以读取完整历史。
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
    m.caption,
    m.reply_to_message_id,
    m.discussion_root_message_id,
    m.source_channel_message_id,
    m.timestamp,
    m.message_type,
    m.real_file_name,
    m.is_group,
    m.is_recalled,
    m.recalled_at,
    m.recalled_by
  FROM messages AS m
  WHERE m.is_group = FALSE
    AND (m.to_user_id = ? OR m.from_user_id = ?)
    AND (m.timestamp > ? OR (m.timestamp = ? AND m.message_id > ?))

  UNION ALL

  SELECT
    m.message_id,
    m.from_user_id,
    m.to_user_id,
    m.content,
    m.caption,
    m.reply_to_message_id,
    m.discussion_root_message_id,
    m.source_channel_message_id,
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
   AND (m.timestamp > ? OR (m.timestamp = ? AND m.message_id > ?))
  LEFT JOIN channel_settings AS channel ON channel.group_id = gm.group_id
  WHERE gm.user_id = ?
    AND (channel.group_id IS NOT NULL
      OR (m.discussion_root_message_id = 0 AND m.timestamp >= COALESCE(NULLIF(gm.joined_at, ''), gm.update_time))
      OR (m.discussion_root_message_id > 0 AND `+DiscussionReadPredicate+`))
) AS sync_messages
ORDER BY timestamp ASC, message_id ASC
LIMIT ?
`, toUserID, toUserID, cursorTimestamp, cursorTimestamp, cursorMessageID,
		cursorTimestamp, cursorTimestamp, cursorMessageID, toUserID, toUserID, pageSize+1).Scan(&messages).Error
	if err != nil {
		return nil, err
	}

	page := buildSyncMessagesPage(messages, pageSize)
	if len(page.Messages) == 0 {
		page.NextCursorTimestamp, page.NextCursorMessageID = cursorTimestamp, cursorMessageID
	}
	return page, nil
}

// Recall changes paginate on recalled_at, never on the original message time.
// Authorization still uses the original time and current membership.
func GetRecalledMessagesPageWithDB(database *gorm.DB, userID int64, cursorTimestamp string, cursorMessageID int64, pageSize int) (*SyncMessagesPage, error) {
	if pageSize <= 0 {
		pageSize = DefaultSyncPageSize
	}
	if pageSize > MaxSyncPageSize {
		pageSize = MaxSyncPageSize
	}
	var messages []Message
	err := database.Raw(`
SELECT m.* FROM messages AS m
WHERE m.is_recalled = TRUE
  AND (m.recalled_at > ? OR (m.recalled_at = ? AND m.message_id > ?))
  AND (
    (m.is_group = FALSE AND (m.from_user_id = ? OR m.to_user_id = ?))
    OR (m.is_group = TRUE AND ((m.from_user_id = ? AND m.discussion_root_message_id = 0 AND NOT EXISTS (
      SELECT 1 FROM channel_settings WHERE group_id = m.to_user_id
    )) OR EXISTS (
      SELECT 1 FROM group_members AS gm
      WHERE gm.group_id = m.to_user_id AND gm.user_id = ?
        AND (EXISTS (SELECT 1 FROM channel_settings WHERE group_id = gm.group_id)
          OR (m.discussion_root_message_id = 0 AND m.timestamp >= COALESCE(NULLIF(gm.joined_at, ''), gm.update_time))
          OR (m.discussion_root_message_id > 0 AND `+DiscussionReadPredicate+`))
    )))
  )
ORDER BY m.recalled_at ASC, m.message_id ASC LIMIT ?
`, cursorTimestamp, cursorTimestamp, cursorMessageID, userID, userID, userID, userID, userID, pageSize+1).Scan(&messages).Error
	if err != nil {
		return nil, err
	}
	page := buildSyncMessagesPage(messages, pageSize)
	page.NextCursorTimestamp, page.NextCursorMessageID = cursorTimestamp, cursorMessageID
	if len(page.Messages) > 0 {
		last := page.Messages[len(page.Messages)-1]
		page.NextCursorTimestamp, page.NextCursorMessageID = last.RecalledAt, last.MessageID
	}
	return page, nil
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
	settings, err := GetChannelSettingsWithDB(database, message.ToUserID)
	if err != nil {
		return false, err
	}
	if settings != nil {
		_, err := GetChannelWithDB(database, requesterID, message.ToUserID, "")
		if errors.Is(err, ErrChannelNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	if message.DiscussionRootMessageID > 0 {
		return CanReadDiscussionMessageWithDB(database, requesterID, message)
	}
	if requesterID == message.FromUserID {
		return true, nil
	}

	var count int64
	err = database.Model(&GroupMember{}).
		Where(
			"group_id = ? AND user_id = ? AND COALESCE(NULLIF(joined_at, ''), update_time) <= ?",
			message.ToUserID,
			requesterID,
			message.Timestamp,
		).
		Count(&count).Error
	return count > 0, err
}
