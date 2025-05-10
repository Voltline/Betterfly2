package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"gorm.io/gorm"
)

func GetUserById(id int64) (*User, error) {
	var user User
	err := DB().First(&user, id).Error
	return getUser(&user, err)
}

func GetUserByAccount(account string) (*User, error) {
	var user User
	err := DB().Where("account = ?", account).First(&user).Error
	return getUser(&user, err)
}

func UpdateUserJwtKeyById(id int64, jwtKey []byte) error {
	return DB().Model(&User{}).
		Where("id = ?", id).
		Update("jwt_key", jwtKey).Error
}

func AddUser(user *User) error {
	user.UpdateTime = utils.NowTime()
	return DB().Create(user).Error
}

func getUser(user *User, err error) (*User, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return user, err
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
