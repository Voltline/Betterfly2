package db_op

func GetUserById(id int64) (*User, error) {
	var u User
	err := DB().First(&u, id).Error
	return &u, err
}

func GetUserByAccount(account string) (*User, error) {
	var u User
	err := db.Where("account = ?", account).First(&u).Error
	return &u, err
}
