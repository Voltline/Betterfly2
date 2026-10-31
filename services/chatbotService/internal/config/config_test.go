package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Topic != "chatbot-service" || cfg.ReplyTopic != "data-forwarding-shared" || cfg.FriendTopic != "friend-service" || cfg.HTTPPort != "8088" ||
		cfg.AdminToken != "" || cfg.UpdateRetention != 24*time.Hour || cfg.MaxUploadBytes != 50<<20 || cfg.WebhookAllowPrivate || cfg.FilesEnabled() {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadOverridesAndValidation(t *testing.T) {
	t.Setenv("CHATBOT_ADMIN_TOKEN", "0123456789abcdef")
	t.Setenv("CHATBOT_UPDATE_RETENTION", "48h")
	t.Setenv("CHATBOT_WEBHOOK_ALLOW_PRIVATE", "true")
	t.Setenv("CHATBOT_MAX_UPLOAD_MB", "8")
	t.Setenv("RUSTFS_ENDPOINT_URL", "http://rustfs:9000/")
	t.Setenv("RUSTFS_ACCESS_KEY_ID", "key")
	t.Setenv("RUSTFS_SECRET_ACCESS_KEY", "secret")
	cfg, err := Load()
	if err != nil || cfg.UpdateRetention != 48*time.Hour || !cfg.WebhookAllowPrivate || cfg.MaxUploadBytes != 8<<20 ||
		cfg.RustFSEndpoint != "http://rustfs:9000" || !cfg.FilesEnabled() {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for key, value := range map[string]string{
		"CHATBOT_ADMIN_TOKEN":           "short",
		"CHATBOT_UPDATE_RETENTION":      "1m",
		"CHATBOT_MAX_POLL_TIMEOUT":      "abc",
		"CHATBOT_WEBHOOK_TIMEOUT":       "2h",
		"CHATBOT_WEBHOOK_ALLOW_PRIVATE": "maybe",
		"CHATBOT_MAX_UPLOAD_MB":         "0",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s accepted", key, value)
			}
		})
	}
}
