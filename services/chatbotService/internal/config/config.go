package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // 容器镜像里没有时区库，CHATBOT_TIMEZONE依赖内嵌数据

	"Betterfly2/shared/db"
)

const (
	DefaultBotUserID = int64(900000000000000002)
	monitorUserID    = int64(900000000000000001)

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
	HTTPPort      string

	APIBaseURL     string
	APIKey         string
	Model          string
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
}

// LLMConfigured 判断是否可以调用模型。
func (c Config) LLMConfigured() bool { return c.APIBaseURL != "" && c.Model != "" }

func Load() (Config, error) {
	cfg := Config{
		Account:           env("CHATBOT_ACCOUNT", "betterfly_assistant"),
		Name:              env("CHATBOT_NAME", "Betterfly 助手"),
		Avatar:            env("CHATBOT_AVATAR", ""),
		Brokers:           strings.Split(env("KAFKA_BROKER", "localhost:9092"), ","),
		Topic:             env("CHATBOT_TOPIC", "chatbot-service"),
		ConsumerGroup:     env("CHATBOT_CONSUMER_GROUP", "chatbot-service-group"),
		StorageTopic:      env("KAFKA_STORAGE_TOPIC", "storage-service"),
		HTTPPort:          env("CHATBOT_HTTP_PORT", "8088"),
		APIBaseURL:        strings.TrimRight(env("CHATBOT_API_BASE_URL", ""), "/"),
		APIKey:            env("CHATBOT_API_KEY", ""),
		Model:             env("CHATBOT_MODEL", ""),
		SystemPrompt:      env("CHATBOT_SYSTEM_PROMPT", defaultSystemPrompt),
		ContextMessages:   20,
		ContextChars:      6000,
		MaxReplyParts:     4,
		MaxTokens:         1024,
		RequestTimeout:    60 * time.Second,
		MaxTriggerAge:     15 * time.Minute,
		MaxRetries:        2,
		UserRatePerMinute: 6,
		UserDailyLimit:    200,
	}
	var errs []error
	cfg.BotUserID = DefaultBotUserID
	if raw := env("CHATBOT_USER_ID", ""); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("CHATBOT_USER_ID无效: %w", err))
		}
		cfg.BotUserID = id
	}
	if cfg.BotUserID <= 1 || cfg.BotUserID == monitorUserID {
		errs = append(errs, errors.New("CHATBOT_USER_ID必须大于1且不能与Monitor账号相同"))
	}
	if !validLen(cfg.Account, 50) || !validLen(cfg.Name, 50) || len(cfg.Avatar) > 255 {
		errs = append(errs, errors.New("CHATBOT_ACCOUNT、CHATBOT_NAME不超过50字，CHATBOT_AVATAR不超过255字节"))
	}
	if cfg.APIBaseURL != "" && !strings.HasPrefix(cfg.APIBaseURL, "http://") && !strings.HasPrefix(cfg.APIBaseURL, "https://") {
		errs = append(errs, errors.New("CHATBOT_API_BASE_URL必须以http://或https://开头"))
	}
	cfg.SystemPrompt = strings.ReplaceAll(cfg.SystemPrompt, "{name}", cfg.Name)

	intVars := []struct {
		key      string
		target   *int
		min, max int
	}{
		{"CHATBOT_CONTEXT_MESSAGES", &cfg.ContextMessages, 1, 100},
		{"CHATBOT_CONTEXT_CHARS", &cfg.ContextChars, 200, 100000},
		{"CHATBOT_MAX_REPLY_PARTS", &cfg.MaxReplyParts, 1, 10},
		{"CHATBOT_MAX_TOKENS", &cfg.MaxTokens, 16, 32768},
		{"CHATBOT_LLM_MAX_RETRIES", &cfg.MaxRetries, 0, 5},
		{"CHATBOT_USER_RATE_PER_MINUTE", &cfg.UserRatePerMinute, 0, 1000},
		{"CHATBOT_USER_DAILY_LIMIT", &cfg.UserDailyLimit, 0, 100000},
	}
	for _, item := range intVars {
		if err := envInt(item.key, item.target, item.min, item.max); err != nil {
			errs = append(errs, err)
		}
	}
	if raw := env("CHATBOT_REQUEST_TIMEOUT", ""); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout < time.Second || timeout > 10*time.Minute {
			errs = append(errs, errors.New("CHATBOT_REQUEST_TIMEOUT必须在1s到10m之间"))
		} else {
			cfg.RequestTimeout = timeout
		}
	}
	if raw := env("CHATBOT_MAX_TRIGGER_AGE", ""); raw != "" {
		age, err := time.ParseDuration(raw)
		if err != nil || age < time.Minute {
			errs = append(errs, errors.New("CHATBOT_MAX_TRIGGER_AGE不能短于1m"))
		} else {
			cfg.MaxTriggerAge = age
		}
	}
	if raw := env("CHATBOT_TEMPERATURE", ""); raw != "" {
		temperature, err := strconv.ParseFloat(raw, 64)
		if err != nil || temperature < 0 || temperature > 2 {
			errs = append(errs, errors.New("CHATBOT_TEMPERATURE必须在0到2之间"))
		} else {
			cfg.Temperature = &temperature
		}
	}
	location, err := time.LoadLocation(env("CHATBOT_TIMEZONE", "Asia/Shanghai"))
	if err != nil {
		errs = append(errs, fmt.Errorf("CHATBOT_TIMEZONE无效: %w", err))
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
