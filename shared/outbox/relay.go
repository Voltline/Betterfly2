package outbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"gorm.io/gorm"
)

type Publisher func(context.Context, db.OutboxEvent) error

type Config struct {
	Service            string
	BatchSize          int
	PollInterval       time.Duration
	Lease              time.Duration
	PublishTimeout     time.Duration
	InitialBackoff     time.Duration
	MaxBackoff         time.Duration
	AlertAfterAttempts int
	Now                func() time.Time
}

func LoadConfig(service, prefix string) Config {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}
	return normalize(Config{
		Service:            service,
		BatchSize:          envInt(prefix+"OUTBOX_BATCH_SIZE", 100),
		PollInterval:       envDuration(prefix+"OUTBOX_POLL_INTERVAL", 250*time.Millisecond),
		Lease:              envDuration(prefix+"OUTBOX_LEASE", 30*time.Second),
		PublishTimeout:     envDuration(prefix+"OUTBOX_PUBLISH_TIMEOUT", 10*time.Second),
		InitialBackoff:     envDuration(prefix+"OUTBOX_RETRY_INITIAL_BACKOFF", time.Second),
		MaxBackoff:         envDuration(prefix+"OUTBOX_RETRY_MAX_BACKOFF", time.Minute),
		AlertAfterAttempts: envInt(prefix+"OUTBOX_ALERT_AFTER_ATTEMPTS", envInt("OUTBOX_ALERT_AFTER_ATTEMPTS", envInt(prefix+"OUTBOX_MAX_ATTEMPTS", 20))),
		Now:                time.Now,
	})
}

func normalize(config Config) Config {
	if config.BatchSize <= 0 || config.BatchSize > 1000 {
		config.BatchSize = 100
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.Lease <= 0 {
		config.Lease = 30 * time.Second
	}
	if config.PublishTimeout <= 0 || config.PublishTimeout >= config.Lease {
		config.PublishTimeout = config.Lease / 2
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = time.Second
	}
	if config.MaxBackoff < config.InitialBackoff {
		config.MaxBackoff = time.Minute
	}
	if config.AlertAfterAttempts <= 0 {
		config.AlertAfterAttempts = 20
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return config
}

type Relay struct {
	database *gorm.DB
	publish  Publisher
	config   Config
}

func New(database *gorm.DB, publish Publisher, config Config) *Relay {
	return &Relay{database: database, publish: publish, config: normalize(config)}
}

func (r *Relay) Run(ctx context.Context) error {
	if r.database == nil || r.publish == nil || strings.TrimSpace(r.config.Service) == "" {
		return errors.New("outbox relay is not configured")
	}
	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()
	for {
		processed, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Sugar().Errorw("Outbox relay批次失败", "service", r.config.Service, "error", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if processed > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	events, err := r.claim(ctx)
	if err != nil {
		return 0, err
	}
	for i := range events {
		event := events[i]
		publishCtx, cancel := context.WithTimeout(ctx, r.config.PublishTimeout)
		err := r.publish(publishCtx, event)
		cancel()
		if err == nil {
			if finalizeErr := r.markPublished(ctx, event); finalizeErr != nil {
				return i, finalizeErr
			}
			continue
		}
		if finalizeErr := r.markFailure(ctx, event, err); finalizeErr != nil {
			return i, finalizeErr
		}
	}
	return len(events), nil
}

func (r *Relay) claim(ctx context.Context) ([]db.OutboxEvent, error) {
	var claimed []db.OutboxEvent
	err := r.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := r.config.Now().UTC()
		nowValue := db.FormatReliabilityTime(now)
		var candidates []db.OutboxEvent
		err := tx.Raw(`SELECT * FROM outbox_events
WHERE service = ? AND (
  (status IN (?, ?, ?) AND (next_attempt_at = '' OR next_attempt_at <= ?)) OR
  (status = ? AND lease_until <= ?)
)
ORDER BY created_at ASC, event_id ASC
FOR UPDATE SKIP LOCKED
LIMIT ?`, r.config.Service, db.OutboxStatusPending, db.OutboxStatusRetryable, db.OutboxStatusFailed, nowValue,
			db.OutboxStatusClaimed, nowValue, r.config.BatchSize).Scan(&candidates).Error
		if err != nil {
			return err
		}
		for i := range candidates {
			token, err := randomToken()
			if err != nil {
				return err
			}
			leaseUntil := db.FormatReliabilityTime(now.Add(r.config.Lease))
			result := tx.Model(&db.OutboxEvent{}).
				Where("event_id = ? AND status = ?", candidates[i].EventID, candidates[i].Status).
				Updates(map[string]any{"status": db.OutboxStatusClaimed, "claim_token": token, "lease_until": leaseUntil, "attempt": gorm.Expr("attempt + 1"), "updated_at": nowValue})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				continue
			}
			candidates[i].Status = db.OutboxStatusClaimed
			candidates[i].ClaimToken = token
			candidates[i].LeaseUntil = leaseUntil
			candidates[i].Attempt++
			claimed = append(claimed, candidates[i])
		}
		return nil
	})
	return claimed, err
}

func (r *Relay) markPublished(ctx context.Context, event db.OutboxEvent) error {
	now := db.FormatReliabilityTime(r.config.Now())
	result := r.database.WithContext(ctx).Model(&db.OutboxEvent{}).
		Where("event_id = ? AND status = ? AND claim_token = ?", event.EventID, db.OutboxStatusClaimed, event.ClaimToken).
		Updates(map[string]any{"status": db.OutboxStatusPublished, "claim_token": "", "lease_until": "", "published_at": now, "updated_at": now, "last_error": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("outbox publish fencing failure for %s", event.EventID)
	}
	return nil
}

func (r *Relay) markFailure(ctx context.Context, event db.OutboxEvent, publishErr error) error {
	now := r.config.Now().UTC()
	next := db.FormatReliabilityTime(now.Add(r.backoff(event.Attempt)))
	result := r.database.WithContext(ctx).Model(&db.OutboxEvent{}).
		Where("event_id = ? AND status = ? AND claim_token = ?", event.EventID, db.OutboxStatusClaimed, event.ClaimToken).
		Updates(map[string]any{"status": db.OutboxStatusRetryable, "claim_token": "", "lease_until": "", "next_attempt_at": next, "last_error": summarize(publishErr), "updated_at": db.FormatReliabilityTime(now)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("outbox failure fencing failure for %s", event.EventID)
	}
	metrics.RecordOutboxPublishFailure(r.config.Service)
	if event.Attempt >= r.config.AlertAfterAttempts && event.Attempt%r.config.AlertAfterAttempts == 0 {
		logger.Sugar().Errorw("Outbox事件持续发布失败，将继续退避重试",
			"service", r.config.Service,
			"event_id", event.EventID,
			"operation_key", event.OperationKey,
			"attempt", event.Attempt,
			"error", summarize(publishErr),
		)
	}
	return nil
}

func (r *Relay) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	factor := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(r.config.InitialBackoff) * factor)
	if delay > r.config.MaxBackoff || delay < 0 {
		return r.config.MaxBackoff
	}
	return delay
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func summarize(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(value) > 255 {
		return value[:255]
	}
	return value
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}
