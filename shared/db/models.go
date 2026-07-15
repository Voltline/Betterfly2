package db

const MaxNameLen = 50 // 账户、用户名、备注等的长度限制

type SchemaMigration struct {
	Version   int    `gorm:"primaryKey;comment:数据库schema版本"`
	AppliedAt string `gorm:"type:varchar(35);comment:迁移完成时间RFC3339"`
}

type ConsumerOperationResult struct {
	Service         string `gorm:"primaryKey;type:varchar(40);comment:消费服务名"`
	OperationKey    string `gorm:"primaryKey;type:varchar(255);comment:source topic/partition/offset"`
	ResponsePayload []byte `gorm:"type:bytea;comment:已完成操作的protobuf响应"`
	CreatedAt       string `gorm:"type:varchar(35);index:idx_consumer_operation_created;comment:完成时间RFC3339"`
}

type User struct {
	ID           int64  `gorm:"primaryKey;autoIncrement;comment:用户id，唯一"`
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
	JoinedAt   string `gorm:"type:varchar(25);comment:加入群组时间，角色变化不得修改"`
	UpdateTime string `gorm:"type:varchar(25);comment:上次更新时间"`
}

type RelationshipRequest struct {
	ID              int64   `gorm:"primaryKey;autoIncrement;comment:关系申请ID"`
	RequestType     string  `gorm:"type:varchar(20);index:idx_relationship_requests_target;comment:friend/group_join/group_invite"`
	RequesterUserID int64   `gorm:"index;comment:发起人用户ID"`
	TargetUserID    int64   `gorm:"index:idx_relationship_requests_target;comment:目标用户ID，入群申请时为0"`
	GroupID         int64   `gorm:"index:idx_relationship_requests_target;comment:相关群ID，好友申请时为0"`
	Message         string  `gorm:"type:varchar(255);comment:验证消息"`
	Status          string  `gorm:"type:varchar(20);index:idx_relationship_requests_target;comment:pending/accepted/rejected/cancelled/expired"`
	ActiveKey       *string `gorm:"type:varchar(160);uniqueIndex;comment:仅待处理申请持有的幂等键"`
	CreatedAt       string  `gorm:"type:varchar(35);index;comment:创建时间RFC3339"`
	ExpiresAt       string  `gorm:"type:varchar(35);index;comment:过期时间RFC3339"`
	ResolvedAt      string  `gorm:"type:varchar(35);comment:处理时间RFC3339"`
	ResolvedBy      int64   `gorm:"comment:处理人用户ID"`
}

type Message struct {
	MessageID       int64   `gorm:"primaryKey;autoIncrement:true;index:idx_messages_sync_target_time_id,priority:4;comment:消息唯一ID"`
	ClientMessageID *string `gorm:"type:varchar(128);uniqueIndex:uidx_messages_sender_client_id,priority:2;comment:客户端幂等消息ID，旧消息为空"`
	FromUserID      int64   `gorm:"type:int8;uniqueIndex:uidx_messages_sender_client_id,priority:1;comment:消息来源用户ID"`
	ToUserID        int64   `gorm:"type:int8;index:idx_messages_sync_target_time_id,priority:2;comment:消息去向用户ID"`
	Content         string  `gorm:"type:varchar(700);comment:消息内容"`
	Timestamp       string  `gorm:"type:varchar(25);index:idx_messages_sync_target_time_id,priority:3;comment:消息产生时间"`
	MessageType     string  `gorm:"type:varchar(10);comment:消息类型"`
	RealFileName    string  `gorm:"type:varchar(255);comment:文件消息的原始文件名，非文件消息为空"`
	IsGroup         bool    `gorm:"type:bool;index:idx_messages_sync_target_time_id,priority:1;comment:消息是否来自于群聊"`
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
	RolloutGroupKey string `gorm:"type:varchar(100);comment:推全后固定返回的分组key"`
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

type PushDeviceToken struct {
	ID          int64  `gorm:"primaryKey;autoIncrement:true;comment:推送设备记录ID"`
	UserID      int64  `gorm:"uniqueIndex:uidx_push_user_device_env_type,priority:1;index:idx_push_user_active,priority:1;comment:所属用户ID"`
	DeviceID    string `gorm:"type:varchar(128);uniqueIndex:uidx_push_user_device_env_type,priority:2;comment:客户端稳定设备ID"`
	Token       string `gorm:"type:varchar(256);uniqueIndex:uidx_push_token_env_type,priority:1;comment:APNs设备token"`
	Environment string `gorm:"type:varchar(20);uniqueIndex:uidx_push_user_device_env_type,priority:3;uniqueIndex:uidx_push_token_env_type,priority:2;comment:sandbox或production"`
	PushType    string `gorm:"type:varchar(20);uniqueIndex:uidx_push_user_device_env_type,priority:4;uniqueIndex:uidx_push_token_env_type,priority:3;comment:推送类型，例如voip或apns"`
	BundleID    string `gorm:"type:varchar(255);comment:应用Bundle ID"`
	IsActive    bool   `gorm:"default:true;index:idx_push_user_active,priority:2;comment:token是否有效"`
	CreatedAt   string `gorm:"type:varchar(35);comment:创建时间"`
	UpdatedAt   string `gorm:"type:varchar(35);comment:更新时间"`
}

type PushMessageDelivery struct {
	MessageID   int64  `gorm:"primaryKey;comment:消息ID"`
	TokenID     int64  `gorm:"primaryKey;comment:APNs token记录ID"`
	Status      string `gorm:"type:varchar(20);default:sent;index:idx_push_delivery_retry;comment:claimed/sent/retryable/permanent"`
	Attempt     int    `gorm:"default:0;comment:投递尝试次数"`
	LeaseUntil  string `gorm:"type:varchar(35);index:idx_push_delivery_retry;comment:claimed租约截止时间"`
	NextRetryAt string `gorm:"type:varchar(35);index:idx_push_delivery_retry;comment:下次允许重试时间"`
	LastError   string `gorm:"type:varchar(255);comment:脱敏后的最后错误"`
	APNSID      string `gorm:"type:varchar(128);comment:Apple返回的APNs ID"`
	CreatedAt   string `gorm:"type:varchar(35);index:idx_push_message_delivery_created;comment:首次申请投递时间"`
	UpdatedAt   string `gorm:"type:varchar(35);comment:最后状态更新时间"`
}

type PushDebugAudit struct {
	ID            int64  `gorm:"primaryKey;autoIncrement;comment:调试推送审计ID"`
	Kind          string `gorm:"type:varchar(20);index;comment:推送类型message或voip"`
	Operator      string `gorm:"type:varchar(100);comment:调试操作人标识"`
	TargetSummary string `gorm:"type:varchar(500);comment:脱敏后的目标摘要"`
	AcceptedCount int    `gorm:"comment:APNs接受数量"`
	FailedCount   int    `gorm:"comment:失败数量"`
	Status        string `gorm:"type:varchar(20);index;comment:success、partial或failed"`
	DetailsJSON   string `gorm:"type:text;comment:脱敏投递详情JSON"`
	CreatedAt     string `gorm:"type:varchar(35);index;comment:创建时间"`
}
