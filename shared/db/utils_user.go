package db

import (
	"Betterfly2/shared/utils"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrAccountExists  = errors.New("用户账号已存在")
	ErrUserIDConflict = errors.New("用户ID冲突")
)

func GetUserById(id int64) (*User, error) {
	return GetUserByIDWithDB(DB(), id)
}

func GetUserByIDWithDB(database *gorm.DB, id int64) (*User, error) {
	var user User
	err := database.First(&user, id).Error
	return getUser(&user, err)
}

func GetUserByAccount(account string) (*User, error) {
	return GetUserByAccountWithDB(DB(), account)
}

func GetUserByAccountWithDB(database *gorm.DB, account string) (*User, error) {
	var user User
	err := database.Where("account = ?", account).First(&user).Error
	return getUser(&user, err)
}

func UpdateUserJwtKeyById(id int64, jwtKey []byte) error {
	return DB().Model(&User{}).
		Where("id = ?", id).
		Update("jwt_key", jwtKey).Error
}

func UpdateUserCredentialsCAS(id int64, oldPasswordHash string, oldJwtKey []byte, newPasswordHash string, newJwtKey []byte) (bool, error) {
	result := DB().Model(&User{}).
		Where("id = ? AND password_hash = ? AND jwt_key = ?", id, oldPasswordHash, oldJwtKey).
		Updates(map[string]interface{}{
			"password_hash": newPasswordHash,
			"jwt_key":       newJwtKey,
		})
	return result.RowsAffected == 1, result.Error
}

func RotateUserJwtKeyCAS(id int64, oldJwtKey, newJwtKey []byte) (bool, error) {
	result := DB().Model(&User{}).
		Where("id = ? AND jwt_key = ?", id, oldJwtKey).
		Update("jwt_key", newJwtKey)
	return result.RowsAffected == 1, result.Error
}

func AddUser(user *User) error {
	user.UpdateTime = utils.NowTime()
	database := DB()
	explicitID := user.ID > 0
	var err error
	if explicitID && database.Dialector.Name() == "postgres" {
		err = database.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(user).Error; err != nil {
				return err
			}
			return tx.Exec(`SELECT setval('users_id_seq', GREATEST((SELECT last_value FROM users_id_seq), ?), TRUE)`, user.ID).Error
		})
	} else {
		err = database.Create(user).Error
	}
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "users_account_key", "idx_users_account":
			return fmt.Errorf("%w: %v", ErrAccountExists, err)
		case "users_pkey":
			return fmt.Errorf("%w: %v", ErrUserIDConflict, err)
		}
	}
	return err
}

func UpdateUserNameByIDWithDB(database *gorm.DB, id int64, newName string) error {
	nowTime := utils.NowTime()
	return database.Model(&User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"name":        newName,
			"update_time": nowTime,
		}).Error
}

func UpdateUserAvatarByIDWithDB(database *gorm.DB, id int64, newAvatarURL string) error {
	nowTime := utils.NowTime()
	return database.Model(&User{}).
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
