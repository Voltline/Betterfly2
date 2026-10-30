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
}

func TestLoadOverridesAndValidation(t *testing.T) {
	t.Setenv("CHATBOT_API_BASE_URL", "https://api.example.com/v1/")
	t.Setenv("CHATBOT_MODEL", "m")
	t.Setenv("CHATBOT_TEMPERATURE", "0.3")
	t.Setenv("CHATBOT_NAME", "小蝶")
	cfg, err := Load()
	if err != nil || !cfg.LLMConfigured() || cfg.APIBaseURL != "https://api.example.com/v1" || *cfg.Temperature != 0.3 || cfg.Name != "小蝶" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}

	for key, value := range map[string]string{
		"CHATBOT_USER_ID":          "900000000000000001",
		"CHATBOT_API_BASE_URL":     "ftp://x",
		"CHATBOT_MAX_REPLY_PARTS":  "0",
		"CHATBOT_REQUEST_TIMEOUT":  "1ms",
		"CHATBOT_TIMEZONE":         "Mars/Base",
		"CHATBOT_MAX_TRIGGER_AGE":  "1s",
		"CHATBOT_TEMPERATURE":      "5",
		"CHATBOT_CONTEXT_MESSAGES": "abc",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s accepted", key, value)
			}
		})
	}
}
