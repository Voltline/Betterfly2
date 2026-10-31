package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 来自环境变量。CHATBOT_ADMIN_TOKEN为空时不开放管理接口；RUSTFS_*未配置时不开放文件接口。
type Config struct {
	Brokers       []string
	Topic         string
	ConsumerGroup string
	StorageTopic  string
	FriendTopic   string
	ReplyTopic    string // 机器人发出消息的入库响应发往DF共享topic，由任意DF投递
	HTTPPort      string

	AdminToken string

	UpdateRetention     time.Duration // 未取走的更新保留多久
	MaxPollTimeout      time.Duration // getUpdates长轮询的最长等待
	SendWait            time.Duration // 发消息后等待入库结果的时间
	WebhookTimeout      time.Duration
	WebhookAllowPrivate bool // 允许webhook指向内网地址；只在可信的内网部署中开启
	MaxUploadBytes      int64

	RustFSEndpoint  string
	RustFSRegion    string
	RustFSAccessKey string
	RustFSSecretKey string
	RustFSBucket    string
}

// FilesEnabled 判断能否收发文件：需要RustFS地址与凭据。
func (c Config) FilesEnabled() bool {
	return c.RustFSEndpoint != "" && c.RustFSAccessKey != "" && c.RustFSSecretKey != ""
}

func Load() (Config, error) {
	cfg := Config{
		Brokers:         strings.Split(env("KAFKA_BROKER", "localhost:9092"), ","),
		Topic:           env("CHATBOT_TOPIC", "chatbot-service"),
		ConsumerGroup:   env("CHATBOT_CONSUMER_GROUP", "chatbot-service-group"),
		StorageTopic:    env("KAFKA_STORAGE_TOPIC", "storage-service"),
		FriendTopic:     env("KAFKA_FRIEND_TOPIC", "friend-service"),
		ReplyTopic:      env("DF_SHARED_TOPIC", "data-forwarding-shared"),
		HTTPPort:        env("CHATBOT_HTTP_PORT", "8088"),
		AdminToken:      env("CHATBOT_ADMIN_TOKEN", ""),
		UpdateRetention: 24 * time.Hour,
		MaxPollTimeout:  50 * time.Second,
		SendWait:        5 * time.Second,
		WebhookTimeout:  10 * time.Second,
		MaxUploadBytes:  50 << 20,
		RustFSEndpoint:  strings.TrimRight(env("RUSTFS_ENDPOINT_URL", ""), "/"),
		RustFSRegion:    env("RUSTFS_REGION", "us-east-1"),
		RustFSAccessKey: env("RUSTFS_ACCESS_KEY_ID", ""),
		RustFSSecretKey: env("RUSTFS_SECRET_ACCESS_KEY", ""),
		RustFSBucket:    env("RUSTFS_BUCKET", "betterfly-files"),
	}
	var errs []error
	if cfg.AdminToken != "" && len(cfg.AdminToken) < 16 {
		errs = append(errs, errors.New("CHATBOT_ADMIN_TOKEN至少16个字符"))
	}
	for _, item := range []struct {
		key     string
		target  *time.Duration
		minimum time.Duration
		maximum time.Duration
	}{
		{"CHATBOT_UPDATE_RETENTION", &cfg.UpdateRetention, time.Hour, 30 * 24 * time.Hour},
		{"CHATBOT_MAX_POLL_TIMEOUT", &cfg.MaxPollTimeout, time.Second, 5 * time.Minute},
		{"CHATBOT_SEND_WAIT", &cfg.SendWait, 0, time.Minute},
		{"CHATBOT_WEBHOOK_TIMEOUT", &cfg.WebhookTimeout, time.Second, time.Minute},
	} {
		raw := env(item.key, "")
		if raw == "" {
			continue
		}
		value, err := time.ParseDuration(raw)
		if err != nil || value < item.minimum || value > item.maximum {
			errs = append(errs, fmt.Errorf("%s必须在%s到%s之间", item.key, item.minimum, item.maximum))
			continue
		}
		*item.target = value
	}
	if raw := env("CHATBOT_WEBHOOK_ALLOW_PRIVATE", ""); raw != "" {
		allow, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("CHATBOT_WEBHOOK_ALLOW_PRIVATE必须是true或false"))
		}
		cfg.WebhookAllowPrivate = allow
	}
	if raw := env("CHATBOT_MAX_UPLOAD_MB", ""); raw != "" {
		megabytes, err := strconv.Atoi(raw)
		if err != nil || megabytes < 1 || megabytes > 1024 {
			errs = append(errs, errors.New("CHATBOT_MAX_UPLOAD_MB必须是1到1024之间的整数"))
		} else {
			cfg.MaxUploadBytes = int64(megabytes) << 20
		}
	}
	return cfg, errors.Join(errs...)
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
