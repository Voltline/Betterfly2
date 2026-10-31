package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/utils"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrAccountTaken    = errors.New("账号已被使用")
	ErrBotIDsExhausted = errors.New("机器人ID段已用完")
	ErrNotFound        = errors.New("机器人不存在")
)

// BotInfo 是机器人在users与bots两张表里的资料。
type BotInfo struct {
	UserID           int64
	Account          string
	Name             string
	Avatar           string
	Description      string
	Delivery         string
	KafkaTopic       string
	HasToken         bool
	WebhookURL       string
	WebhookSecret    string
	AllowGroups      bool
	Disabled         bool
	WebhookFailures  int
	WebhookRetryAt   *time.Time
	WebhookLastError string
	CreatedAt        time.Time
}

// Store 封装机器人平台的数据库访问；消息写入一律经StorageService完成。
type Store struct{ database *gorm.DB }

func NewStore(database *gorm.DB) *Store { return &Store{database: database} }

const botColumns = `b.user_id, u.account, u.name, u.avatar, b.description, b.delivery, b.kafka_topic,
 b.token_hash IS NOT NULL AS has_token, b.webhook_url, b.webhook_secret, b.allow_groups, b.disabled,
 b.webhook_failures, b.webhook_retry_at, b.webhook_last_error, b.created_at`

func (s *Store) bots(ctx context.Context) *gorm.DB {
	return s.database.WithContext(ctx).Table("bots AS b").Select(botColumns).Joins("JOIN users AS u ON u.id = b.user_id")
}

func (s *Store) takeBot(query *gorm.DB) (*BotInfo, error) {
	var bot BotInfo
	result := query.Limit(1).Scan(&bot)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &bot, nil
}

// Bot 返回机器人资料，不存在时返回nil。
func (s *Store) Bot(ctx context.Context, userID int64) (*BotInfo, error) {
	return s.takeBot(s.bots(ctx).Where("b.user_id = ?", userID))
}

func (s *Store) BotByTokenHash(ctx context.Context, tokenHash string) (*BotInfo, error) {
	return s.takeBot(s.bots(ctx).Where("b.token_hash = ?", tokenHash))
}

func (s *Store) ListBots(ctx context.Context) ([]BotInfo, error) {
	var bots []BotInfo
	err := s.bots(ctx).Order("b.user_id").Scan(&bots).Error
	return bots, err
}

// NewBot 是管理员创建第三方机器人时填写的资料。
type NewBot struct {
	Account     string
	Name        string
	Avatar      string
	Description string
	AllowGroups bool
}

// CreateBot 在机器人ID段里分配新ID并写入users与bots。最小的ID留给AI助手。
// 用显式ID插入且不推进users_id_seq；密码哈希为空，机器人无法登录。
func (s *Store) CreateBot(ctx context.Context, bot NewBot, tokenHash string) (int64, error) {
	var userID int64
	err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 串行化分配，避免并发创建拿到同一个ID。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(0x62657474657262)).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT COALESCE(MAX(id), ?) + 1 FROM users WHERE id BETWEEN ? AND ?",
			db.BotUserIDMin, db.BotUserIDMin, db.BotUserIDMax).Scan(&userID).Error; err != nil {
			return err
		}
		if !db.IsBotUserID(userID) {
			return ErrBotIDsExhausted
		}
		if err := tx.Exec("INSERT INTO users (id, account, name, update_time, avatar, password_hash) VALUES (?, ?, ?, ?, ?, '')",
			userID, bot.Account, bot.Name, utils.NowTime(), bot.Avatar).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrAccountTaken
			}
			return err
		}
		return tx.Exec(`INSERT INTO bots (user_id, token_hash, description, delivery, allow_groups, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, now(), now())`, userID, tokenHash, bot.Description, db.BotDeliveryAPI, bot.AllowGroups).Error
	})
	return userID, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// BotPatch 中为nil的字段保持不变。
type BotPatch struct {
	Name        *string
	Avatar      *string
	Description *string
	AllowGroups *bool
	Disabled    *bool
}

// UpdateBot 修改资料。名字或头像变化时刷新update_time，客户端按资料同步拿到新值。
func (s *Store) UpdateBot(ctx context.Context, userID int64, patch BotPatch) error {
	return s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		botFields := map[string]any{"updated_at": gorm.Expr("now()")}
		if patch.Description != nil {
			botFields["description"] = *patch.Description
		}
		if patch.AllowGroups != nil {
			botFields["allow_groups"] = *patch.AllowGroups
		}
		if patch.Disabled != nil {
			botFields["disabled"] = *patch.Disabled
		}
		result := tx.Table("bots").Where("user_id = ?", userID).Updates(botFields)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		userFields := map[string]any{}
		if patch.Name != nil {
			userFields["name"] = *patch.Name
		}
		if patch.Avatar != nil {
			userFields["avatar"] = *patch.Avatar
		}
		if len(userFields) == 0 {
			return nil
		}
		userFields["update_time"] = utils.NowTime()
		return tx.Model(&db.User{}).Where("id = ?", userID).Updates(userFields).Error
	})
}

// SetToken 更换API机器人的令牌，旧令牌立即失效。内置机器人没有令牌。
func (s *Store) SetToken(ctx context.Context, userID int64, tokenHash string) error {
	result := s.database.WithContext(ctx).Table("bots").Where("user_id = ? AND delivery = ?", userID, db.BotDeliveryAPI).
		Updates(map[string]any{"token_hash": tokenHash, "updated_at": gorm.Expr("now()")})
	if result.Error == nil && result.RowsAffected == 0 {
		return ErrNotFound
	}
	return result.Error
}

// SetWebhook 设置或清除webhook；清除失败计数，立即开始投递。
func (s *Store) SetWebhook(ctx context.Context, userID int64, url, secret string) error {
	return s.database.WithContext(ctx).Table("bots").Where("user_id = ?", userID).Updates(map[string]any{
		"webhook_url": url, "webhook_secret": secret, "webhook_failures": 0, "webhook_retry_at": nil,
		"webhook_last_error": "", "updated_at": gorm.Expr("now()"),
	}).Error
}

// InsertUpdate 写入一条待取走的更新；同一dedup_key只写一次，Kafka重投不会重复。
func (s *Store) InsertUpdate(ctx context.Context, botID int64, dedupKey, payload string) (bool, error) {
	result := s.database.WithContext(ctx).Exec(`INSERT INTO bot_updates (bot_user_id, dedup_key, payload, created_at)
 VALUES (?, ?, ?, now()) ON CONFLICT (bot_user_id, dedup_key) DO NOTHING`, botID, dedupKey, payload)
	return result.RowsAffected > 0, result.Error
}

// PendingUpdates 确认offset之前的更新，再按顺序返回之后未确认的更新。
func (s *Store) PendingUpdates(ctx context.Context, botID, offset int64, limit int) ([]db.BotUpdate, error) {
	database := s.database.WithContext(ctx)
	if offset > 0 {
		if err := s.AckUpdates(ctx, botID, offset-1); err != nil {
			return nil, err
		}
	}
	var updates []db.BotUpdate
	err := database.Where("bot_user_id = ? AND acked_at IS NULL AND update_id >= ?", botID, offset).
		Order("update_id").Limit(limit).Find(&updates).Error
	return updates, err
}

// AckUpdates 确认update_id不大于upTo的更新。
func (s *Store) AckUpdates(ctx context.Context, botID, upTo int64) error {
	return s.database.WithContext(ctx).Model(&db.BotUpdate{}).
		Where("bot_user_id = ? AND update_id <= ? AND acked_at IS NULL", botID, upTo).
		Update("acked_at", gorm.Expr("now()")).Error
}

// DeleteUpdatesBefore 删除早于cutoff的更新，无论是否已取走。
func (s *Store) DeleteUpdatesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.database.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&db.BotUpdate{})
	return result.RowsAffected, result.Error
}

// ClaimWebhook 领取一个有待投递更新、到了重试时间的webhook机器人，并把webhook_retry_at推后lease作为租约，
// 多个实例不会同时给同一机器人投递，投递顺序得以保持。没有可领取的机器人时返回nil。
func (s *Store) ClaimWebhook(ctx context.Context, lease time.Duration) (*BotInfo, error) {
	var userID int64
	result := s.database.WithContext(ctx).Raw(`UPDATE bots SET webhook_retry_at = now() + make_interval(secs => ?)
 WHERE user_id = (
   SELECT b.user_id FROM bots AS b
   WHERE b.webhook_url <> '' AND b.disabled = FALSE AND b.delivery = ? AND (b.webhook_retry_at IS NULL OR b.webhook_retry_at <= now())
     AND EXISTS (SELECT 1 FROM bot_updates AS u WHERE u.bot_user_id = b.user_id AND u.acked_at IS NULL)
   ORDER BY b.webhook_retry_at NULLS FIRST, b.user_id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING user_id`, lease.Seconds(), db.BotDeliveryAPI).Scan(&userID)
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	return s.Bot(ctx, userID)
}

// FinishWebhook 记录一轮投递结果。成功时清除租约与失败计数；失败时按retryAt重试。
func (s *Store) FinishWebhook(ctx context.Context, botID int64, retryAt *time.Time, lastError string) error {
	fields := map[string]any{"webhook_retry_at": retryAt}
	if lastError == "" {
		fields["webhook_failures"], fields["webhook_last_error"] = 0, ""
	} else {
		fields["webhook_failures"] = gorm.Expr("webhook_failures + 1")
		fields["webhook_last_error"] = truncate(lastError, 255)
	}
	return s.database.WithContext(ctx).Table("bots").Where("user_id = ?", botID).Updates(fields).Error
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func (s *Store) Message(ctx context.Context, messageID int64) (*db.Message, error) {
	return db.GetMessageByIDWithDB(s.database.WithContext(ctx), messageID)
}

// MessageSender 返回消息的发送者，用于判断是否引用了机器人的消息；消息不存在时返回0。
func (s *Store) MessageSender(ctx context.Context, messageID int64) (int64, error) {
	var senders []int64
	err := s.database.WithContext(ctx).Model(&db.Message{}).Where("message_id = ?", messageID).Limit(1).Pluck("from_user_id", &senders).Error
	if err != nil || len(senders) == 0 {
		return 0, err
	}
	return senders[0], nil
}

// SentMessage 按机器人的client_message_id查找已入库的消息，尚未入库时返回nil。
func (s *Store) SentMessage(ctx context.Context, botID int64, clientMessageID string) (*db.Message, error) {
	var messages []db.Message
	err := s.database.WithContext(ctx).Select("message_id, timestamp").
		Where("from_user_id = ? AND client_message_id = ?", botID, clientMessageID).Limit(1).Find(&messages).Error
	if err != nil || len(messages) == 0 {
		return nil, err
	}
	return &messages[0], nil
}

type UserBrief struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Account string `json:"account"`
}

func (s *Store) User(ctx context.Context, userID int64) (*UserBrief, error) {
	var users []UserBrief
	err := s.database.WithContext(ctx).Model(&db.User{}).Select("id, name, account").Where("id = ?", userID).Limit(1).Scan(&users).Error
	if err != nil || len(users) == 0 {
		return nil, err
	}
	return &users[0], nil
}

func (s *Store) GroupName(ctx context.Context, groupID int64) (string, error) {
	var names []string
	err := s.database.WithContext(ctx).Model(&db.Group{}).Where("group_id = ?", groupID).Limit(1).Pluck("name", &names).Error
	if err != nil || len(names) == 0 {
		return "", err
	}
	return names[0], nil
}

// GroupBots 返回群里的机器人成员，供未带bot_user_ids的旧版触发使用。
func (s *Store) GroupBots(ctx context.Context, groupID int64) ([]int64, error) {
	var ids []int64
	err := s.database.WithContext(ctx).Table("group_members").Where("group_id = ? AND user_id BETWEEN ? AND ?", groupID, db.BotUserIDMin, db.BotUserIDMax).
		Order("user_id").Pluck("user_id", &ids).Error
	return ids, err
}

// IsFriend 判断用户与机器人是否互为好友；机器人只能给好友发私聊。
func (s *Store) IsFriend(ctx context.Context, botID, userID int64) (bool, error) {
	var count int64
	err := s.database.WithContext(ctx).Model(&db.Friend{}).
		Where("user_id = ? AND friend_id = ? AND is_delete = FALSE", botID, userID).Limit(1).Count(&count).Error
	return count > 0, err
}

func (s *Store) CanPublishGroup(ctx context.Context, groupID, botID int64) (bool, error) {
	return db.CanPublishGroupMessageWithDB(s.database.WithContext(ctx), groupID, botID)
}

func (s *Store) CanUseFile(ctx context.Context, fileHash string, botID int64) (bool, error) {
	return db.CanUseFileWithDB(s.database.WithContext(ctx), fileHash, botID)
}

func (s *Store) CanDownloadFile(ctx context.Context, fileHash string, botID int64) (bool, error) {
	return db.CanDownloadFileWithDB(s.database.WithContext(ctx), fileHash, botID)
}

// VerifiedFile 返回已校验文件的存储路径与大小；不存在或未校验时ok为false。
func (s *Store) VerifiedFile(ctx context.Context, fileHash string) (path string, size int64, ok bool, err error) {
	metadata, err := db.GetFileMetadataWithDB(s.database.WithContext(ctx), fileHash)
	if err != nil || metadata == nil || !metadata.IsVerified {
		return "", 0, false, err
	}
	path = metadata.StoragePath
	if path == "" {
		path = StoragePath(fileHash)
	}
	return path, metadata.FileSize, true, nil
}

// StoreUploadedFile 记录机器人上传并校验过的文件，同时授予它使用权。
func (s *Store) StoreUploadedFile(ctx context.Context, fileHash string, size int64, botID int64) error {
	return db.StoreVerifiedFileWithDB(s.database.WithContext(ctx), fileHash, size, StoragePath(fileHash), botID)
}

func (s *Store) GrantFile(ctx context.Context, fileHash string, botID int64) error {
	return db.GrantFileAccessWithDB(s.database.WithContext(ctx), fileHash, botID)
}

// StoragePath 与StorageService一致：哈希前两位作目录。
func StoragePath(fileHash string) string {
	return fmt.Sprintf("%s/%s", fileHash[:2], fileHash)
}

// ValidAccount 账号只用小写字母、数字和下划线，3到50个字符，避免与普通用户的展示混淆。
func ValidAccount(account string) bool {
	if len(account) < 3 || len(account) > 50 {
		return false
	}
	return strings.Trim(account, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
}
