package push

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"gorm.io/gorm"
)

type CleanupConfig struct {
	Retention    time.Duration
	ReplayWindow time.Duration
	Interval     time.Duration
	BatchSize    int
}

func LoadCleanupConfig() CleanupConfig {
	config := CleanupConfig{
		Retention:    pushCleanupDuration("PUSH_DELIVERY_RETENTION", 30*24*time.Hour),
		ReplayWindow: pushCleanupDuration("KAFKA_MAX_REPLAY_WINDOW", 7*24*time.Hour),
		Interval:     pushCleanupDuration("PUSH_CLEANUP_INTERVAL", time.Hour),
		BatchSize:    pushCleanupInt("PUSH_CLEANUP_BATCH_SIZE", 1000),
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

func (s *GormStore) RunCleanup(ctx context.Context, config CleanupConfig) {
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			counts, err := s.CleanupCompletedDeliveries(ctx, config, time.Now().UTC())
			if err != nil {
				logger.Sugar().Warnw("Push持久投递清理失败", "error", err)
				continue
			}
			for kind, count := range counts {
				if count > 0 {
					metrics.RecordReliabilityCleanup("push", kind, count)
				}
			}
			logger.Sugar().Debugw("Push持久投递限量清理完成", "message_deliveries", counts["message_delivery"], "voip_deliveries", counts["voip_delivery"], "jobs", counts["job"])
		}
	}
}

func (s *GormStore) CleanupCompletedDeliveries(ctx context.Context, config CleanupConfig, now time.Time) (map[string]int64, error) {
	counts := map[string]int64{"message_delivery": 0, "voip_delivery": 0, "job": 0}
	if s == nil || s.db == nil {
		return counts, gorm.ErrInvalidDB
	}
	cutoff := db.FormatReliabilityTime(now.Add(-config.Retention))
	queries := []struct {
		kind string
		sql  string
	}{
		{
			kind: "message_delivery",
			sql: `DELETE FROM push_message_deliveries
WHERE (message_id, token_id) IN (
  SELECT delivery.message_id, delivery.token_id
  FROM push_message_deliveries AS delivery
  JOIN push_jobs AS job ON job.job_id = delivery.job_id
  WHERE job.status IN ('completed', 'failed') AND job.completed_at < ?
  ORDER BY job.completed_at, delivery.message_id, delivery.token_id
  LIMIT ?
)`,
		},
		{
			kind: "voip_delivery",
			sql: `DELETE FROM push_vo_ip_deliveries
WHERE (call_id, token_id) IN (
  SELECT delivery.call_id, delivery.token_id
  FROM push_vo_ip_deliveries AS delivery
  JOIN push_jobs AS job ON job.job_id = delivery.job_id
  WHERE job.status IN ('completed', 'failed') AND job.completed_at < ?
  ORDER BY job.completed_at, delivery.call_id, delivery.token_id
  LIMIT ?
)`,
		},
		{
			kind: "job",
			sql: `DELETE FROM push_jobs
WHERE job_id IN (
  SELECT job.job_id
  FROM push_jobs AS job
  WHERE job.status IN ('completed', 'failed') AND job.completed_at < ?
    AND NOT EXISTS (SELECT 1 FROM push_message_deliveries AS message WHERE message.job_id = job.job_id)
    AND NOT EXISTS (SELECT 1 FROM push_vo_ip_deliveries AS voip WHERE voip.job_id = job.job_id)
  ORDER BY job.completed_at, job.job_id
  LIMIT ?
)`,
		},
	}
	for _, query := range queries {
		result := s.db.WithContext(ctx).Exec(query.sql, cutoff, config.BatchSize)
		if result.Error != nil {
			return counts, result.Error
		}
		counts[query.kind] = result.RowsAffected
	}
	return counts, nil
}

func pushCleanupDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func pushCleanupInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}
