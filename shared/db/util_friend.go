package db

import "Betterfly2/shared/utils"

func MakeFriend(userID, friendID int64, alias string) error {
	return DB().Create(&Friend{
		UserID:     userID,
		FriendID:   friendID,
		IsNotify:   true,
		Alias:      alias,
		IsDelete:   false,
		UpdateTime: utils.NowTime(),
	}).Error
}

func Unfriend(userID, friendID int64) error {
	return DB().Model(&Friend{}).
		Where("user_id = ? AND friend_id = ?", userID, friendID).
		Updates(map[string]interface{}{
			"is_delete":   true,
			"update_time": utils.NowTime()}).Error
}
