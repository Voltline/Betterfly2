package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // 容器镜像里没有时区库，AICHAT_TIMEZONE依赖内嵌数据

	"Betterfly2/shared/db"
)

const (
	DefaultBotUserID = db.BotUserIDMin

	defaultSystemPrompt = "你是Betterfly聊天应用里的AI助手，名字叫“{name}”。用和用户相同的语言回答，简洁、友好、准确；" +
		"不知道就直说，不要编造事实。回复会显示在聊天气泡里，少用Markdown标题和表格。"
)

// Config 来自环境变量。模型接口地址与模型名都配置后才会调用模型，否则机器人只回复未配置提示。
type Config struct {
	BotUserID int64
	Account   string
	Name      string
	Avatar    string

	Brokers       []string
	Topic         string
	ConsumerGroup string
	StorageTopic  string
	ReplyTopic    string // 回复的入库响应发往DF共享topic，由任意DF投递
	HTTPPort      string

	APIBaseURL     string
	APIKey         string
	Model          string
	VisionModel    string // 带图片的请求改用这个模型；为空时沿用Model
	MaxTokens      int
	Temperature    *float64
	RequestTimeout time.Duration
	MaxRetries     int
	MaxTriggerAge  time.Duration

	SystemPrompt    string
	Location        *time.Location
	ContextMessages int
	ContextChars    int
	MaxReplyParts   int

	UserRatePerMinute int
	UserDailyLimit    int

	Vision        bool  // 是否把图片交给模型
	ContextImages int   // 上下文里最多带几张图片，从新到旧
	ImageMaxBytes int64 // 超过这个大小的原图不读取
	ImageMaxSide  int   // 长边超过时缩小后再发给模型

	// 读取图片用的RustFS，与StorageService同一组RUSTFS_*变量
	RustFSEndpoint  string
	RustFSRegion    string
	RustFSAccessKey string
	RustFSSecretKey string
	RustFSBucket    string
}

// LLMConfigured 判断是否可以调用模型。
func (c Config) LLMConfigured() bool { return c.APIBaseURL != "" && c.Model != "" }

// VisionEnabled 判断能否把图片交给模型：开启了AICHAT_VISION且配置了RustFS读取凭据。
func (c Config) VisionEnabled() bool {
	return c.Vision && c.ContextImages > 0 && c.RustFSEndpoint != "" && c.RustFSAccessKey != "" && c.RustFSSecretKey != ""
}

func Load() (Config, error) {
	cfg := Config{
		Account:           env("AICHAT_ACCOUNT", "betterfly_assistant"),
		Name:              env("AICHAT_NAME", "Betterfly 助手"),
		Avatar:            env("AICHAT_AVATAR", ""),
		Brokers:           strings.Split(env("KAFKA_BROKER", "localhost:9092"), ","),
		Topic:             env("AICHAT_TOPIC", "aichat-service"),
		ConsumerGroup:     env("AICHAT_CONSUMER_GROUP", "aichat-service-group"),
		StorageTopic:      env("KAFKA_STORAGE_TOPIC", "storage-service"),
		ReplyTopic:        env("DF_SHARED_TOPIC", "data-forwarding-shared"),
		HTTPPort:          env("AICHAT_HTTP_PORT", "8089"),
		APIBaseURL:        strings.TrimRight(env("AICHAT_API_BASE_URL", ""), "/"),
		APIKey:            env("AICHAT_API_KEY", ""),
		Model:             env("AICHAT_MODEL", ""),
		VisionModel:       env("AICHAT_VISION_MODEL", ""),
		SystemPrompt:      env("AICHAT_SYSTEM_PROMPT", defaultSystemPrompt),
		ContextMessages:   20,
		ContextChars:      6000,
		MaxReplyParts:     4,
		MaxTokens:         1024,
		RequestTimeout:    60 * time.Second,
		MaxTriggerAge:     15 * time.Minute,
		MaxRetries:        2,
		UserRatePerMinute: 6,
		UserDailyLimit:    200,
		Vision:            true,
		ContextImages:     3,
		ImageMaxBytes:     20 << 20,
		ImageMaxSide:      1568,
		RustFSEndpoint:    strings.TrimRight(env("RUSTFS_ENDPOINT_URL", ""), "/"),
		RustFSRegion:      env("RUSTFS_REGION", "us-east-1"),
		RustFSAccessKey:   env("RUSTFS_ACCESS_KEY_ID", ""),
		RustFSSecretKey:   env("RUSTFS_SECRET_ACCESS_KEY", ""),
		RustFSBucket:      env("RUSTFS_BUCKET", "betterfly-files"),
	}
	var errs []error
	cfg.BotUserID = DefaultBotUserID
	if raw := env("AICHAT_USER_ID", ""); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("AICHAT_USER_ID无效: %w", err))
		}
		cfg.BotUserID = id
	}
	if !db.IsBotUserID(cfg.BotUserID) {
		errs = append(errs, fmt.Errorf("AICHAT_USER_ID必须在机器人ID段%d到%d之间", db.BotUserIDMin, db.BotUserIDMax))
	}
	if !validLen(cfg.Account, 50) || !validLen(cfg.Name, 50) || len(cfg.Avatar) > 255 {
		errs = append(errs, errors.New("AICHAT_ACCOUNT、AICHAT_NAME不超过50字，AICHAT_AVATAR不超过255字节"))
	}
	if cfg.APIBaseURL != "" && !strings.HasPrefix(cfg.APIBaseURL, "http://") && !strings.HasPrefix(cfg.APIBaseURL, "https://") {
		errs = append(errs, errors.New("AICHAT_API_BASE_URL必须以http://或https://开头"))
	}
	cfg.SystemPrompt = strings.ReplaceAll(cfg.SystemPrompt, "{name}", cfg.Name)

	intVars := []struct {
		key      string
		target   *int
		min, max int
	}{
		{"AICHAT_CONTEXT_MESSAGES", &cfg.ContextMessages, 1, 100},
		{"AICHAT_CONTEXT_CHARS", &cfg.ContextChars, 200, 100000},
		{"AICHAT_MAX_REPLY_PARTS", &cfg.MaxReplyParts, 1, 10},
		{"AICHAT_MAX_TOKENS", &cfg.MaxTokens, 16, 32768},
		{"AICHAT_LLM_MAX_RETRIES", &cfg.MaxRetries, 0, 5},
		{"AICHAT_USER_RATE_PER_MINUTE", &cfg.UserRatePerMinute, 0, 1000},
		{"AICHAT_USER_DAILY_LIMIT", &cfg.UserDailyLimit, 0, 100000},
		{"AICHAT_CONTEXT_IMAGES", &cfg.ContextImages, 0, 10},
		{"AICHAT_IMAGE_MAX_SIDE", &cfg.ImageMaxSide, 256, 4096},
	}
	for _, item := range intVars {
		if err := envInt(item.key, item.target, item.min, item.max); err != nil {
			errs = append(errs, err)
		}
	}
	if raw := env("AICHAT_VISION", ""); raw != "" {
		vision, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("AICHAT_VISION必须是true或false"))
		}
		cfg.Vision = vision
	}
	if raw := env("AICHAT_IMAGE_MAX_MB", ""); raw != "" {
		megabytes, err := strconv.Atoi(raw)
		if err != nil || megabytes < 1 || megabytes > 100 {
			errs = append(errs, errors.New("AICHAT_IMAGE_MAX_MB必须是1到100之间的整数"))
		} else {
			cfg.ImageMaxBytes = int64(megabytes) << 20
		}
	}
	if raw := env("AICHAT_REQUEST_TIMEOUT", ""); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout < time.Second || timeout > 10*time.Minute {
			errs = append(errs, errors.New("AICHAT_REQUEST_TIMEOUT必须在1s到10m之间"))
		} else {
			cfg.RequestTimeout = timeout
		}
	}
	if raw := env("AICHAT_MAX_TRIGGER_AGE", ""); raw != "" {
		age, err := time.ParseDuration(raw)
		if err != nil || age < time.Minute {
			errs = append(errs, errors.New("AICHAT_MAX_TRIGGER_AGE不能短于1m"))
		} else {
			cfg.MaxTriggerAge = age
		}
	}
	if raw := env("AICHAT_TEMPERATURE", ""); raw != "" {
		temperature, err := strconv.ParseFloat(raw, 64)
		if err != nil || temperature < 0 || temperature > 2 {
			errs = append(errs, errors.New("AICHAT_TEMPERATURE必须在0到2之间"))
		} else {
			cfg.Temperature = &temperature
		}
	}
	location, err := time.LoadLocation(env("AICHAT_TIMEZONE", "Asia/Shanghai"))
	if err != nil {
		errs = append(errs, fmt.Errorf("AICHAT_TIMEZONE无效: %w", err))
		location = time.UTC
	}
	cfg.Location = location
	return cfg, errors.Join(errs...)
}

func validLen(value string, maxRunes int) bool {
	return value != "" && len([]rune(value)) <= maxRunes && db.ValidMessageFields(value, "text", "", "")
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, target *int, minValue, maxValue int) error {
	raw := env(key, "")
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return fmt.Errorf("%s必须是%d到%d之间的整数", key, minValue, maxValue)
	}
	*target = value
	return nil
}
