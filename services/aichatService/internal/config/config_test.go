package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotUserID != DefaultBotUserID || cfg.LLMConfigured() || cfg.Location.String() != "Asia/Shanghai" ||
		cfg.MaxTriggerAge != 15*time.Minute || !strings.Contains(cfg.SystemPrompt, "Betterfly 助手") {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Topic != "aichat-service" || cfg.ReplyTopic != "data-forwarding-shared" || cfg.HTTPPort != "8089" ||
		!cfg.Vision || cfg.ContextImages != 3 || cfg.ImageMaxBytes != 20<<20 || cfg.ImageMaxSide != 1568 || cfg.VisionEnabled() {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadOverridesAndValidation(t *testing.T) {
	t.Setenv("AICHAT_API_BASE_URL", "https://api.example.com/v1/")
	t.Setenv("AICHAT_MODEL", "m")
	t.Setenv("AICHAT_VISION_MODEL", "vm")
	t.Setenv("AICHAT_TEMPERATURE", "0.3")
	t.Setenv("AICHAT_NAME", "小蝶")
	t.Setenv("AICHAT_IMAGE_MAX_MB", "5")
	t.Setenv("DF_SHARED_TOPIC", "df-shared")
	t.Setenv("RUSTFS_ENDPOINT_URL", "http://rustfs:9000/")
	t.Setenv("RUSTFS_ACCESS_KEY_ID", "key")
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", "secret")
	cfg, err := Load()
	if err != nil || !cfg.LLMConfigured() || cfg.APIBaseURL != "https://api.example.com/v1" || *cfg.Temperature != 0.3 || cfg.Name != "小蝶" ||
		cfg.VisionModel != "vm" || cfg.ImageMaxBytes != 5<<20 || cfg.ReplyTopic != "df-shared" || cfg.RustFSEndpoint != "http://rustfs:9000" || !cfg.VisionEnabled() {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	t.Setenv("AICHAT_VISION", "false")
	if cfg, err := Load(); err != nil || cfg.VisionEnabled() {
		t.Fatalf("vision disabled: cfg=%+v err=%v", cfg, err)
	}

	for key, value := range map[string]string{
		"AICHAT_USER_ID":          "900000000000000001",
		"AICHAT_API_BASE_URL":     "ftp://x",
		"AICHAT_MAX_REPLY_PARTS":  "0",
		"AICHAT_REQUEST_TIMEOUT":  "1ms",
		"AICHAT_TIMEZONE":         "Mars/Base",
		"AICHAT_MAX_TRIGGER_AGE":  "1s",
		"AICHAT_TEMPERATURE":      "5",
		"AICHAT_CONTEXT_MESSAGES": "abc",
		"AICHAT_VISION":           "maybe",
		"AICHAT_IMAGE_MAX_MB":     "0",
		"AICHAT_CONTEXT_IMAGES":   "11",
		"AICHAT_IMAGE_MAX_SIDE":   "10",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s accepted", key, value)
			}
		})
	}
}
