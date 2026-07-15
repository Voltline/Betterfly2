package db

import (
	"Betterfly2/shared/utils"

	"gorm.io/gorm"
)

type FriendContact struct {
	UserID     int64  `gorm:"column:user_id"`
	Account    string `gorm:"column:account"`
	Name       string `gorm:"column:name"`
	Avatar     string `gorm:"column:avatar"`
	Alias      string `gorm:"column:alias"`
	IsNotify   bool   `gorm:"column:is_notify"`
	UpdateTime string `gorm:"column:update_time"`
}

func RemoveDirectFriendPair(userID, friendID int64) (bool, string, error) {
	return RemoveDirectFriendPairWithDB(DB(), userID, friendID)
}

func RemoveDirectFriendPairWithDB(database *gorm.DB, userID, friendID int64) (bool, string, error) {
	now := utils.NowTime()
	affected := int64(0)

	err := database.Transaction(func(tx *gorm.DB) error {
		for _, pair := range [][2]int64{{userID, friendID}, {friendID, userID}} {
			result := tx.Model(&Friend{}).
				Where("user_id = ? AND friend_id = ? AND is_delete = ?", pair[0], pair[1], false).
				Updates(map[string]interface{}{
					"is_delete":   true,
					"update_time": now,
				})
			if result.Error != nil {
				return result.Error
			}
			affected += result.RowsAffected
		}
		return nil
	})
	if err != nil {
		return false, "", err
	}
	return affected > 0, now, nil
}

func GetFriendList(userID int64) ([]FriendContact, error) {
	return GetFriendListWithDB(DB(), userID)
}

func GetFriendListWithDB(database *gorm.DB, userID int64) ([]FriendContact, error) {
	var contacts []FriendContact
	err := database.
		Table("friends").
		Select("friends.friend_id AS user_id, users.account, users.name, users.avatar, friends.alias, friends.is_notify, friends.update_time").
		Joins("JOIN users ON users.id = friends.friend_id").
		Where("friends.user_id = ? AND friends.is_delete = ?", userID, false).
		Order("friends.update_time DESC").
		Scan(&contacts).Error
	return contacts, err
}

func UpdateFriendAlias(userID, friendID int64, alias string) (bool, string, error) {
	return UpdateFriendAliasWithDB(DB(), userID, friendID, alias)
}

func UpdateFriendAliasWithDB(database *gorm.DB, userID, friendID int64, alias string) (bool, string, error) {
	now := utils.NowTime()
	result := database.Model(&Friend{}).
		Where("user_id = ? AND friend_id = ? AND is_delete = ?", userID, friendID, false).
		Updates(map[string]interface{}{
			"alias":       alias,
			"update_time": now,
		})
	if result.Error != nil {
		return false, "", result.Error
	}
	return result.RowsAffected > 0, now, nil
}

func UpdateFriendNotify(userID, friendID int64, isNotify bool) (bool, string, error) {
	return UpdateFriendNotifyWithDB(DB(), userID, friendID, isNotify)
}

func UpdateFriendNotifyWithDB(database *gorm.DB, userID, friendID int64, isNotify bool) (bool, string, error) {
	now := utils.NowTime()
	result := database.Model(&Friend{}).
		Where("user_id = ? AND friend_id = ? AND is_delete = ?", userID, friendID, false).
		Updates(map[string]interface{}{
			"is_notify":   isNotify,
			"update_time": now,
		})
	if result.Error != nil {
		return false, "", result.Error
	}
	return result.RowsAffected > 0, now, nil
}
