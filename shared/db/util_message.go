package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"gorm.io/gorm"
)

// StoreNewMessage 存储一条新消息
func StoreNewMessage(fromUserID, toUserID int64, content, messageType string, isGroup bool) error {
	return DB().Create(&Message{
		FromUserID:  fromUserID,
		ToUserID:    toUserID,
		Content:     content,
		Timestamp:   utils.NowTime(),
		MessageType: messageType,
		IsGroup:     isGroup,
	}).Error
}

// GetMessageByID 基于消息ID直接查询消息
func GetMessageByID(messageID int64) (*Message, error) {
	var message *Message
	err := DB().First(message, messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return message, err
}

// GetSyncMessagesByTimestamp 获取同步信息
// TODO: 因为还没实现群聊，所以暂时还没查群聊的消息
func GetSyncMessagesByTimestamp(toUserID int64, timestamp string) ([]Message, error) {
	var messages []Message
	err := DB().Where("to_user_id = ? AND timestamp >= ?", toUserID, timestamp).Find(&messages).Error
	return messages, err
}
