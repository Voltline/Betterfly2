package db

import (
	"time"

	"gorm.io/gorm"
)

// 机器人账号的用户ID段。普通注册用户来自users_id_seq，不会进入这个区间；Monitor账号是900000000000000001。
// AI助手固定为BotUserIDMin，客户端以它作常量；机器人平台从区间内依次分配新的ID。
const (
	BotUserIDMin int64 = 900000000000000002
	BotUserIDMax int64 = 900000000999999999
)

// IsBotUserID 判断用户ID是否落在机器人ID段。
func IsBotUserID(userID int64) bool {
	return userID >= BotUserIDMin && userID <= BotUserIDMax
}

const (
	BotDeliveryAPI   = "api"   // 第三方机器人：长轮询或webhook
	BotDeliveryKafka = "kafka" // 内置服务：触发消息转发到kafka_topic
)

// Bot 是机器人平台的注册信息，user_id对应users表里的机器人账号。表结构由v13迁移显式创建。
type Bot struct {
	UserID           int64      `gorm:"primaryKey"`
	TokenHash        *string    `gorm:"type:varchar(64)"`
	Description      string     `gorm:"type:text;not null"`
	Delivery         string     `gorm:"type:varchar(16);not null"`
	KafkaTopic       string     `gorm:"type:varchar(128);not null"`
	WebhookURL       string     `gorm:"type:varchar(512);not null"`
	WebhookSecret    string     `gorm:"type:varchar(256);not null"`
	AllowGroups      bool       `gorm:"not null"`
	Disabled         bool       `gorm:"not null"`
	WebhookFailures  int        `gorm:"not null"`
	WebhookRetryAt   *time.Time `gorm:"type:timestamptz"`
	WebhookLastError string     `gorm:"type:varchar(255);not null"`
	CreatedAt        time.Time  `gorm:"type:timestamptz;not null"`
	UpdatedAt        time.Time  `gorm:"type:timestamptz;not null"`
}

// BotUpdate 是等待第三方机器人取走的更新；dedup_key保证Kafka重投不会产生重复更新。
type BotUpdate struct {
	UpdateID  int64      `gorm:"primaryKey;autoIncrement"`
	BotUserID int64      `gorm:"not null"`
	DedupKey  string     `gorm:"type:varchar(128);not null"`
	Payload   string     `gorm:"type:text;not null"`
	CreatedAt time.Time  `gorm:"type:timestamptz;not null"`
	AckedAt   *time.Time `gorm:"type:timestamptz"`
}

// Keep v13 independent of future model edits: released migrations are immutable.
func migrateBotPlatformSchema(tx *gorm.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS bots (
 user_id bigint PRIMARY KEY, token_hash varchar(64) UNIQUE, description text NOT NULL DEFAULT '',
 delivery varchar(16) NOT NULL CHECK (delivery IN ('api', 'kafka')), kafka_topic varchar(128) NOT NULL DEFAULT '',
 webhook_url varchar(512) NOT NULL DEFAULT '', webhook_secret varchar(256) NOT NULL DEFAULT '',
 allow_groups boolean NOT NULL DEFAULT TRUE, disabled boolean NOT NULL DEFAULT FALSE,
 webhook_failures integer NOT NULL DEFAULT 0, webhook_retry_at timestamptz, webhook_last_error varchar(255) NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS bot_updates (
 update_id bigserial PRIMARY KEY, bot_user_id bigint NOT NULL, dedup_key varchar(128) NOT NULL,
 payload text NOT NULL, created_at timestamptz NOT NULL, acked_at timestamptz,
 UNIQUE (bot_user_id, dedup_key))`,
		`CREATE INDEX IF NOT EXISTS idx_bot_updates_pending ON bot_updates (bot_user_id, update_id) WHERE acked_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_bot_updates_created ON bot_updates (created_at)`,
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
