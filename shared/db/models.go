package db

type User struct {
	ID           int64  `gorm:"primaryKey;comment:用户id，唯一"`
	Account      string `gorm:"uniqueIndex;comment:用户账号，唯一"`
	Name         string `gorm:"comment:用户昵称，不唯一"`
	UpdateTime   string `gorm:"comment:上次更新个人资料时间，用于用户间的个人资料同步"`
	Avatar       string `gorm:"comment:用户头像的url，图片存在COS或别的http服务器上"`
	PasswordHash string `gorm:"comment:加密后的用户密码哈希值"`
	JwtKey       []byte `gorm:"comment:jwt的key"`
}
