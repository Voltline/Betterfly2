package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"gorm.io/gorm"
)

// StoreNewMessage 存储一条新消息，返回消息ID
func StoreNewMessage(fromUserID, toUserID int64, content, messageType, realFileName string, isGroup bool) (int64, error) {
	message := &Message{
		FromUserID:   fromUserID,
		ToUserID:     toUserID,
		Content:      content,
		Timestamp:    utils.NowTime(),
		MessageType:  messageType,
		RealFileName: realFileName,
		IsGroup:      isGroup,
	}
	err := DB().Create(message).Error
	if err != nil {
		return 0, err
	}
	return message.MessageID, nil
}

// GetMessageByID 基于消息ID直接查询消息
func GetMessageByID(messageID int64) (*Message, error) {
	var message Message
	err := DB().First(&message, "message_id = ?", messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &message, nil
}

// GetSyncMessagesByTimestamp 获取同步消息。
// 当前会返回：
// 1. 发给该用户的单聊消息
// 2. 该用户当前已加入群组中的群聊消息
// 群聊消息会额外要求消息时间晚于该成员记录的 update_time，
// 避免把用户入群前的旧消息同步回来。
func GetSyncMessagesByTimestamp(toUserID int64, timestamp string) ([]Message, error) {
	var messages []Message
	err := DB().Raw(`
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
    m.is_group
  FROM messages AS m
  WHERE m.is_group = FALSE
    AND m.to_user_id = ?
    AND m.timestamp > ?

  UNION ALL

  SELECT
    m.message_id,
    m.from_user_id,
    m.to_user_id,
    m.content,
    m.timestamp,
    m.message_type,
    m.real_file_name,
    m.is_group
  FROM group_members AS gm
  JOIN messages AS m
    ON m.to_user_id = gm.group_id
   AND m.is_group = TRUE
   AND m.timestamp > gm.update_time
   AND m.timestamp > ?
  WHERE gm.user_id = ?
) AS sync_messages
ORDER BY timestamp ASC
`, toUserID, timestamp, timestamp, toUserID).Scan(&messages).Error
	return messages, err
}
