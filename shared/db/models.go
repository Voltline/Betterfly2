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
	UserID     int64  `gorm:"primaryKey;index:idx_friends_user_active_update,priority:1;comment:当前用户ID"`
	FriendID   int64  `gorm:"primaryKey;comment:对方用户ID"`
	IsNotify   bool   `gorm:"comment:是否开启好友消息通知"`
	Alias      string `gorm:"type:varchar(50);comment:对好友的备注名"`
	IsDelete   bool   `gorm:"index:idx_friends_user_active_update,priority:2;comment:是否为已删除好友"`
	UpdateTime string `gorm:"type:varchar(25);index:idx_friends_user_active_update,priority:3;comment:上次更新时间，用于同步"`
}

type Group struct {
	GroupID     int64  `gorm:"primaryKey;comment:群组ID，唯一"`
	Name        string `gorm:"type:varchar(100);comment:群组名称"`
	Avatar      string `gorm:"type:varchar(255);comment:群头像URL"`
	OwnerUserID int64  `gorm:"type:int8;comment:群主用户ID"`
	IsDelete    bool   `gorm:"type:bool;default:false;comment:群组是否已删除"`
	UpdateTime  string `gorm:"type:varchar(25);comment:上次更新时间"`
}

type GroupMember struct {
	GroupID    int64  `gorm:"primaryKey;index:idx_group_members_user_group,priority:2;comment:群组ID"`
	UserID     int64  `gorm:"primaryKey;index:idx_group_members_user_group,priority:1;comment:成员用户ID"`
	Role       string `gorm:"type:varchar(20);comment:成员角色，例如owner/member"`
	UpdateTime string `gorm:"type:varchar(25);comment:上次更新时间"`
}

type Message struct {
	MessageID    int64  `gorm:"primaryKey;autoIncrement:true;comment:消息唯一ID"`
	FromUserID   int64  `gorm:"type:int8;comment:消息来源用户ID"`
	ToUserID     int64  `gorm:"type:int8;index:idx_messages_sync_target_time,priority:2;comment:消息去向用户ID"`
	Content      string `gorm:"type:varchar(700);comment:消息内容"`
	Timestamp    string `gorm:"type:varchar(25);index:idx_messages_sync_target_time,priority:3;comment:消息产生时间"`
	MessageType  string `gorm:"type:varchar(10);comment:消息类型"`
	RealFileName string `gorm:"type:varchar(255);comment:文件消息的原始文件名，非文件消息为空"`
	IsGroup      bool   `gorm:"type:bool;index:idx_messages_sync_target_time,priority:1;comment:消息是否来自于群聊"`
}

type FileMetadata struct {
	FileHash    string `gorm:"primaryKey;type:varchar(128);comment:文件SHA512哈希值，作为主键"`
	FileSize    int64  `gorm:"comment:文件大小（字节）"`
	StoragePath string `gorm:"type:varchar(512);comment:文件在对象存储中的路径"`
	IsVerified  bool   `gorm:"type:bool;default:false;comment:文件是否已经完成哈希校验并可对外提供下载"`
	CreatedAt   string `gorm:"type:varchar(25);comment:文件创建时间"`
	UpdatedAt   string `gorm:"type:varchar(25);comment:文件更新时间"`
}

type ABExperiment struct {
	ID              int64  `gorm:"primaryKey;autoIncrement:true;comment:实验ID"`
	ExperimentKey   string `gorm:"uniqueIndex;type:varchar(100);comment:实验唯一key"`
	Name            string `gorm:"type:varchar(100);comment:实验名称"`
	Description     string `gorm:"type:text;comment:实验描述"`
	ExperimentType  string `gorm:"type:varchar(20);index;comment:实验类型，例如client/server/all"`
	Status          string `gorm:"type:varchar(20);index;comment:实验状态，例如draft/running/paused/stopped"`
	StartTime       string `gorm:"type:varchar(35);index;comment:实验绝对开始时间，RFC3339格式"`
	DurationSeconds int64  `gorm:"comment:实验持续时间，单位秒"`
	EndTime         string `gorm:"type:varchar(35);index;comment:实验绝对结束时间，RFC3339格式"`
	Salt            string `gorm:"type:varchar(100);comment:稳定分流盐值"`
	TargetingJSON   string `gorm:"type:text;comment:实验命中规则JSON，预留客户端版本/系统版本等扩展条件"`
	Version         int64  `gorm:"default:1;comment:实验配置版本"`
	CreatedAt       string `gorm:"type:varchar(35);comment:创建时间"`
	UpdatedAt       string `gorm:"type:varchar(35);comment:更新时间"`
}

type ABExperimentGroup struct {
	ID                 int64  `gorm:"primaryKey;autoIncrement:true;comment:实验分组ID"`
	ExperimentID       int64  `gorm:"index:idx_ab_groups_experiment;comment:实验ID"`
	GroupKey           string `gorm:"type:varchar(100);comment:分组key"`
	TrafficBasisPoints int    `gorm:"comment:分流比例，万分比，10000代表100%"`
	ConfigJSON         string `gorm:"type:text;comment:该组下发配置JSON"`
	CreatedAt          string `gorm:"type:varchar(35);comment:创建时间"`
	UpdatedAt          string `gorm:"type:varchar(35);comment:更新时间"`
}

type ABExperimentOverride struct {
	ID           int64  `gorm:"primaryKey;autoIncrement:true;comment:实验例外规则ID"`
	ExperimentID int64  `gorm:"index:idx_ab_overrides_experiment_subject,priority:1;comment:实验ID"`
	SubjectType  string `gorm:"type:varchar(30);index:idx_ab_overrides_experiment_subject,priority:2;comment:主体类型，例如device/user/server"`
	SubjectID    string `gorm:"type:varchar(128);index:idx_ab_overrides_experiment_subject,priority:3;comment:主体ID，例如设备号"`
	Action       string `gorm:"type:varchar(30);comment:例外动作，例如force_group/exclude/merge_config"`
	GroupKey     string `gorm:"type:varchar(100);comment:强制命中的分组key"`
	ConfigJSON   string `gorm:"type:text;comment:额外覆盖配置JSON"`
	CreatedAt    string `gorm:"type:varchar(35);comment:创建时间"`
	UpdatedAt    string `gorm:"type:varchar(35);comment:更新时间"`
}
