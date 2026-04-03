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
	if user.ID == 0 {
		var maxID int64
		if err := DB().Raw("SELECT COALESCE(MAX(id), 0) FROM users").Scan(&maxID).Error; err != nil {
			return err
		}
		user.ID = maxID + 1
	}
	return DB().Create(user).Error
}

// UpdateUserNameByID 用于更新用户昵称
func UpdateUserNameByID(id int64, newName string) error {
	nowTime := utils.NowTime()
	return DB().Model(&User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"name":        newName,
			"update_time": nowTime,
		}).Error
}

// UpdateUserAvatarByID 用于更新用户头像
func UpdateUserAvatarByID(id int64, newAvatarURL string) error {
	nowTime := utils.NowTime()
	return DB().Model(&User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"avatar":      newAvatarURL,
			"update_time": nowTime,
		}).Error
}

func getUser(user *User, err error) (*User, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return user, err
}
