package db

import (
	"Betterfly2/shared/utils"
	"errors"

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

func GetFriendship(userID, friendID int64) (*Friend, error) {
	var friendship Friend
	err := DB().Where("user_id = ? AND friend_id = ?", userID, friendID).First(&friendship).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &friendship, nil
}

func AddDirectFriendPair(userID, friendID int64) (bool, string, error) {
	now := utils.NowTime()
	alreadyFriends := true

	err := DB().Transaction(func(tx *gorm.DB) error {
		pairs := [][2]int64{
			{userID, friendID},
			{friendID, userID},
		}

		for _, pair := range pairs {
			var relation Friend
			err := tx.Where("user_id = ? AND friend_id = ?", pair[0], pair[1]).First(&relation).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				alreadyFriends = false
				if err := tx.Create(&Friend{
					UserID:     pair[0],
					FriendID:   pair[1],
					IsNotify:   true,
					Alias:      "",
					IsDelete:   false,
					UpdateTime: now,
				}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}

			if relation.IsDelete {
				alreadyFriends = false
				if err := tx.Model(&Friend{}).
					Where("user_id = ? AND friend_id = ?", pair[0], pair[1]).
					Updates(map[string]interface{}{
						"is_delete":   false,
						"update_time": now,
					}).Error; err != nil {
					return err
				}
				continue
			}
		}
		return nil
	})

	return alreadyFriends, now, err
}

func RemoveDirectFriendPair(userID, friendID int64) (bool, string, error) {
	now := utils.NowTime()
	affected := int64(0)

	err := DB().Transaction(func(tx *gorm.DB) error {
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
	var contacts []FriendContact
	err := DB().
		Table("friends").
		Select("friends.friend_id AS user_id, users.account, users.name, users.avatar, friends.alias, friends.is_notify, friends.update_time").
		Joins("JOIN users ON users.id = friends.friend_id").
		Where("friends.user_id = ? AND friends.is_delete = ?", userID, false).
		Order("friends.update_time DESC").
		Scan(&contacts).Error
	return contacts, err
}

func UpdateFriendAlias(userID, friendID int64, alias string) (bool, string, error) {
	now := utils.NowTime()
	result := DB().Model(&Friend{}).
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
	now := utils.NowTime()
	result := DB().Model(&Friend{}).
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
