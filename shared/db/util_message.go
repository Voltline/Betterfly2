package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"gorm.io/gorm"
)

// StoreNewMessage 存储一条新消息，返回消息ID
func StoreNewMessage(fromUserID, toUserID int64, content, messageType string, isGroup bool) (int64, error) {
	message := &Message{
		FromUserID:  fromUserID,
		ToUserID:    toUserID,
		Content:     content,
		Timestamp:   utils.NowTime(),
		MessageType: messageType,
		IsGroup:     isGroup,
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

// GetSyncMessagesByTimestamp 获取同步信息
// TODO: 因为还没实现群聊，所以暂时还没查群聊的消息
func GetSyncMessagesByTimestamp(toUserID int64, timestamp string) ([]Message, error) {
	var messages []Message
	err := DB().Where("to_user_id = ? AND timestamp > ?", toUserID, timestamp).
		Order("timestamp ASC").
		Find(&messages).Error
	return messages, err
}
