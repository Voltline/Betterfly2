package db

const MaxNameLen = 50 // 账户、用户名、备注等的长度限制

type User struct {
	ID           int64  `gorm:"primaryKey;comment:用户id，唯一"`
	Account      string `gorm:"uniqueIndex;type:varchar(50);comment:用户账号，唯一"`
	Name         string `gorm:"type:varchar(50);comment:用户昵称，不唯一"`
	UpdateTime   string `gorm:"type:varchar(25);comment:上次更新个人资料时间，用于用户间的个人资料同步"`
	Avatar       string `gorm:"type:varchar(255);comment:用户头像的url，图片存在COS或别的http服务器上"`
	PasswordHash string `gorm:"type:varchar(60);comment:加密后的用户密码哈希值，bcrypt生成的带盐哈希固定长度60"`
	JwtKey       []byte `gorm:"comment:jwt的key"`
}

type Friend struct {
	UserID     int64  `gorm:"primaryKey;comment:当前用户ID"`
	FriendID   int64  `gorm:"primaryKey;comment:对方用户ID"`
	IsNotify   bool   `gorm:"comment:是否开启好友消息通知"`
	Alias      string `gorm:"type:varchar(50);comment:对好友的备注名"`
	IsDelete   bool   `gorm:"comment:是否为已删除好友"`
	UpdateTime string `gorm:"type:varchar(25);comment:上次更新时间，用于同步"`
}

type Message struct {
	MessageID   int64  `gorm:"primaryKey;autoIncrement:true;comment:消息唯一ID"`
	FromUserID  int64  `gorm:"type:int8;comment:消息来源用户ID"`
	ToUserID    int64  `gorm:"type:int8;comment:消息去向用户ID"`
	Content     string `gorm:"type:varchar(700);comment:消息内容"`
	Timestamp   string `gorm:"type:varchar(25);comment:消息产生时间"`
	MessageType string `gorm:"type:varchar(10);comment:消息类型"`
	IsGroup     bool   `gorm:"type:bool;comment:消息是否来自于群聊"`
}

type FileMetadata struct {
	FileHash    string `gorm:"primaryKey;type:varchar(128);comment:文件SHA512哈希值，作为主键"`
	FileSize    int64  `gorm:"comment:文件大小（字节）"`
	StoragePath string `gorm:"type:varchar(512);comment:文件在对象存储中的路径"`
	CreatedAt   string `gorm:"type:varchar(25);comment:文件创建时间"`
	UpdatedAt   string `gorm:"type:varchar(25);comment:文件更新时间"`
}
