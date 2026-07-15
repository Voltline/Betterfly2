package db

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"gorm.io/gorm"
)

type RetentionConfig struct {
	Retention    time.Duration
	ReplayWindow time.Duration
	Interval     time.Duration
	BatchSize    int
}

func LoadRetentionConfig() RetentionConfig {
	config := RetentionConfig{
		Retention:    envRetentionDuration("CONSUMER_STATE_RETENTION", 30*24*time.Hour),
		ReplayWindow: envRetentionDuration("KAFKA_MAX_REPLAY_WINDOW", 7*24*time.Hour),
		Interval:     envRetentionDuration("RELIABILITY_CLEANUP_INTERVAL", time.Hour),
		BatchSize:    envRetentionInt("RELIABILITY_CLEANUP_BATCH_SIZE", 1000),
	}
	if config.Retention <= config.ReplayWindow {
		config.Retention = config.ReplayWindow + 24*time.Hour
	}
	if config.Interval <= 0 {
		config.Interval = time.Hour
	}
	if config.BatchSize <= 0 || config.BatchSize > 10000 {
		config.BatchSize = 1000
	}
	return config
}

func RunReliabilityCleanup(ctx context.Context, database *gorm.DB, config RetentionConfig) {
	if database == nil {
		return
	}
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := CleanupReliabilityState(ctx, database, config, time.Now().UTC()); err != nil {
				logger.Sugar().Warnw("可靠性状态清理失败", "error", err)
			}
		}
	}
}

func CleanupReliabilityState(ctx context.Context, database *gorm.DB, config RetentionConfig, now time.Time) error {
	cutoff := FormatReliabilityTime(now.Add(-config.Retention))
	queries := []struct {
		kind string
		sql  string
	}{
		{kind: "consumer_result", sql: `DELETE FROM consumer_operation_results WHERE (service, operation_key) IN (SELECT service, operation_key FROM consumer_operation_results WHERE created_at < ? ORDER BY created_at LIMIT ?)`},
		{kind: "consumer_inbox", sql: `DELETE FROM consumer_inboxes WHERE (service, operation_key) IN (SELECT service, operation_key FROM consumer_inboxes WHERE status = 'completed' AND completed_at < ? ORDER BY completed_at LIMIT ?)`},
		{kind: "outbox_event", sql: `DELETE FROM outbox_events WHERE event_id IN (SELECT event_id FROM outbox_events WHERE status = 'published' AND published_at < ? ORDER BY published_at LIMIT ?)`},
	}
	for _, query := range queries {
		result := database.WithContext(ctx).Exec(query.sql, cutoff, config.BatchSize)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			metrics.RecordReliabilityCleanup("shared", query.kind, result.RowsAffected)
			logger.Sugar().Debugw("可靠性状态限量清理完成", "kind", query.kind, "rows", result.RowsAffected)
		}
	}
	return nil
}

func envRetentionDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envRetentionInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}
