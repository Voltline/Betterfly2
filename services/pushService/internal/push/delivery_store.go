package push

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const claimMessageDeliverySQL = `WITH candidates AS (
  SELECT delivery.message_id, delivery.token_id
  FROM push_message_deliveries AS delivery
  JOIN push_jobs AS job ON job.job_id = delivery.job_id
  WHERE job.status = ? AND delivery.attempt < ? AND (
    delivery.status = ? OR
    (delivery.status = ? AND (delivery.next_retry_at = '' OR delivery.next_retry_at <= ?)) OR
    (delivery.status = ? AND delivery.lease_until <= ?)
  )
  ORDER BY delivery.created_at ASC, delivery.message_id ASC, delivery.token_id ASC
  FOR UPDATE OF delivery SKIP LOCKED
  LIMIT ?
), updated AS (
  UPDATE push_message_deliveries AS delivery SET
    status = ?, attempt = delivery.attempt + 1, claim_token = ?, lease_until = ?, updated_at = ?
  FROM candidates
  WHERE delivery.message_id = candidates.message_id AND delivery.token_id = candidates.token_id
  RETURNING delivery.message_id, delivery.token_id, delivery.job_id, delivery.attempt,
    delivery.claim_token, delivery.created_at AS delivery_created_at
)
SELECT updated.message_id, updated.token_id, updated.job_id, updated.attempt, updated.claim_token,
  updated.delivery_created_at,
  token.user_id, token.device_id, token.token, token.environment, token.push_type, token.bundle_id,
  token.is_active, token.created_at AS token_created_at, token.updated_at AS token_updated_at,
  job.request_payload
FROM updated
LEFT JOIN push_device_tokens AS token ON token.id = updated.token_id
JOIN push_jobs AS job ON job.job_id = updated.job_id
ORDER BY updated.message_id ASC, updated.token_id ASC`

const claimVoIPDeliverySQL = `WITH candidates AS (
  SELECT delivery.call_id, delivery.token_id
  FROM push_vo_ip_deliveries AS delivery
  JOIN push_jobs AS job ON job.job_id = delivery.job_id
  WHERE job.status = ? AND delivery.attempt < ? AND (
    delivery.status = ? OR
    (delivery.status = ? AND (delivery.next_retry_at = '' OR delivery.next_retry_at <= ?)) OR
    (delivery.status = ? AND delivery.lease_until <= ?)
  )
  ORDER BY delivery.created_at ASC, delivery.call_id ASC, delivery.token_id ASC
  FOR UPDATE OF delivery SKIP LOCKED
  LIMIT ?
), updated AS (
  UPDATE push_vo_ip_deliveries AS delivery SET
    status = ?, attempt = delivery.attempt + 1, claim_token = ?, lease_until = ?, updated_at = ?
  FROM candidates
  WHERE delivery.call_id = candidates.call_id AND delivery.token_id = candidates.token_id
  RETURNING delivery.call_id, delivery.token_id, delivery.job_id, delivery.attempt,
    delivery.claim_token, delivery.created_at AS delivery_created_at
)
SELECT updated.call_id, updated.token_id, updated.job_id, updated.attempt, updated.claim_token,
  updated.delivery_created_at,
  token.user_id, token.device_id, token.token, token.environment, token.push_type, token.bundle_id,
  token.is_active, token.created_at AS token_created_at, token.updated_at AS token_updated_at,
  job.request_payload
FROM updated
LEFT JOIN push_device_tokens AS token ON token.id = updated.token_id
JOIN push_jobs AS job ON job.job_id = updated.job_id
ORDER BY updated.call_id ASC, updated.token_id ASC`

type durableClaimRow struct {
	JobID             string `gorm:"column:job_id"`
	MessageID         int64  `gorm:"column:message_id"`
	CallID            string `gorm:"column:call_id"`
	TokenID           int64  `gorm:"column:token_id"`
	Attempt           int    `gorm:"column:attempt"`
	ClaimToken        string `gorm:"column:claim_token"`
	DeliveryCreatedAt string `gorm:"column:delivery_created_at"`
	UserID            int64  `gorm:"column:user_id"`
	DeviceID          string `gorm:"column:device_id"`
	TokenValue        string `gorm:"column:token"`
	Environment       string `gorm:"column:environment"`
	PushType          string `gorm:"column:push_type"`
	BundleID          string `gorm:"column:bundle_id"`
	IsActive          bool   `gorm:"column:is_active"`
	TokenCreatedAt    string `gorm:"column:token_created_at"`
	TokenUpdatedAt    string `gorm:"column:token_updated_at"`
	RequestPayload    []byte `gorm:"column:request_payload"`
}

func (s *GormStore) ClaimMessageDeliveryBatch(ctx context.Context, limit int, now time.Time, lease time.Duration, maxAttempts int) ([]DurableDeliveryClaim, error) {
	return s.claimDeliveryBatch(ctx, deliveryKindMessage, claimMessageDeliverySQL, limit, now, lease, maxAttempts)
}

func (s *GormStore) ClaimVoIPDeliveryBatch(ctx context.Context, limit int, now time.Time, lease time.Duration, maxAttempts int) ([]DurableDeliveryClaim, error) {
	return s.claimDeliveryBatch(ctx, deliveryKindVoIP, claimVoIPDeliverySQL, limit, now, lease, maxAttempts)
}

func (s *GormStore) claimDeliveryBatch(ctx context.Context, kind deliveryKind, query string, limit int, now time.Time, lease time.Duration, maxAttempts int) ([]DurableDeliveryClaim, error) {
	if limit <= 0 || limit > 1000 {
		limit = 256
	}
	if maxAttempts <= 0 {
		maxAttempts = 10
	}
	claimToken, err := newClaimToken()
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	nowValue := db.FormatReliabilityTime(now)
	leaseUntil := db.FormatReliabilityTime(now.Add(lease))
	var rows []durableClaimRow
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		exhaustedJobs, expireErr := expireExhaustedDeliveries(tx, kind, nowValue, maxAttempts)
		if expireErr != nil {
			return expireErr
		}
		for _, jobID := range exhaustedJobs {
			if kind == deliveryKindVoIP {
				if err := s.completeVoIPJobIfTerminal(tx, jobID); err != nil {
					return err
				}
				continue
			}
			if err := completeMessageJobIfTerminal(tx, jobID); err != nil {
				return err
			}
		}
		return tx.Raw(query,
			PushJobPending, maxAttempts,
			DeliveryPending, DeliveryRetryable, nowValue, DeliveryClaimed, nowValue,
			limit, DeliveryClaimed, claimToken, leaseUntil, nowValue,
		).Scan(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	claims := make([]DurableDeliveryClaim, 0, len(rows))
	for _, row := range rows {
		queuedAt, _ := time.Parse(time.RFC3339Nano, row.DeliveryCreatedAt)
		claims = append(claims, DurableDeliveryClaim{
			JobID: row.JobID, MessageID: row.MessageID, CallID: row.CallID,
			Token: db.PushDeviceToken{
				ID: row.TokenID, UserID: row.UserID, DeviceID: row.DeviceID, Token: row.TokenValue,
				Environment: row.Environment, PushType: row.PushType, BundleID: row.BundleID,
				IsActive: row.IsActive, CreatedAt: row.TokenCreatedAt, UpdatedAt: row.TokenUpdatedAt,
			},
			QueuedAt: queuedAt, Attempt: row.Attempt, ClaimToken: row.ClaimToken,
			RequestPayload: append([]byte(nil), row.RequestPayload...),
		})
	}
	return claims, nil
}

func expireExhaustedDeliveries(tx *gorm.DB, kind deliveryKind, nowValue string, maxAttempts int) ([]string, error) {
	table := "push_message_deliveries"
	if kind == deliveryKindVoIP {
		table = "push_vo_ip_deliveries"
	}
	query := fmt.Sprintf(`UPDATE %s SET
  status = ?, claim_token = '', lease_until = '', next_retry_at = '',
  last_error = ?, updated_at = ?
WHERE attempt >= ? AND (
  (status = ? AND lease_until <= ?) OR
  (status = ? AND (next_retry_at = '' OR next_retry_at <= ?))
)
RETURNING job_id`, table)
	var rows []struct {
		JobID string `gorm:"column:job_id"`
	}
	if err := tx.Raw(query,
		DeliveryFailed, "delivery_attempts_exhausted", nowValue, maxAttempts,
		DeliveryClaimed, nowValue, DeliveryRetryable, nowValue,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	unique := make(map[string]struct{}, len(rows))
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.JobID == "" {
			continue
		}
		if _, exists := unique[row.JobID]; exists {
			continue
		}
		unique[row.JobID] = struct{}{}
		result = append(result, row.JobID)
	}
	return result, nil
}

func completeMessageJobIfTerminal(tx *gorm.DB, jobID string) error {
	job, err := lockPushJob(tx, jobID)
	if err != nil {
		return err
	}
	return completeMessageJobIfTerminalLocked(tx, job)
}

func completeMessageJobIfTerminalLocked(tx *gorm.DB, job db.PushJob) error {
	var outstanding int64
	if err := tx.Model(&db.PushMessageDelivery{}).Where("job_id = ? AND status IN ?", job.JobID, []string{DeliveryPending, DeliveryClaimed, DeliveryRetryable}).Count(&outstanding).Error; err != nil {
		return err
	}
	if outstanding > 0 {
		return nil
	}
	now := db.FormatReliabilityTime(time.Now())
	return tx.Model(&db.PushJob{}).Where("job_id = ? AND status = ?", job.JobID, PushJobPending).
		Updates(map[string]any{"status": PushJobCompleted, "completed_at": now, "updated_at": now}).Error
}

func (s *GormStore) FinalizeMessageDelivery(ctx context.Context, update DurableDeliveryUpdate) error {
	if strings.TrimSpace(update.JobID) == "" || update.MessageID == 0 || update.Token.ID <= 0 || update.Attempt <= 0 || update.ClaimToken == "" {
		return ErrInvalidRequest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockPushJob(tx, update.JobID)
		if err != nil {
			return err
		}
		if err := finalizeClaimedDelivery(tx.Model(&db.PushMessageDelivery{}), update,
			"message_id = ? AND token_id = ? AND job_id = ?", update.MessageID, update.Token.ID, update.JobID); err != nil {
			return err
		}
		if update.DeactivateToken {
			if err := deactivateTokenTx(tx, update.Token.ID); err != nil {
				return err
			}
		}
		return completeMessageJobIfTerminalLocked(tx, job)
	})
}

func (s *GormStore) FinalizeVoIPDelivery(ctx context.Context, update DurableDeliveryUpdate) error {
	if strings.TrimSpace(update.JobID) == "" || strings.TrimSpace(update.CallID) == "" || update.Token.ID <= 0 || update.Attempt <= 0 || update.ClaimToken == "" {
		return ErrInvalidRequest
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockPushJob(tx, update.JobID)
		if err != nil {
			return err
		}
		if err := finalizeClaimedDelivery(tx.Model(&db.PushVoIPDelivery{}), update,
			"call_id = ? AND token_id = ? AND job_id = ?", update.CallID, update.Token.ID, update.JobID); err != nil {
			return err
		}
		if update.DeactivateToken {
			if err := deactivateTokenTx(tx, update.Token.ID); err != nil {
				return err
			}
		}
		return s.completeVoIPJobIfTerminalLocked(tx, job)
	})
}

func finalizeClaimedDelivery(query *gorm.DB, update DurableDeliveryUpdate, identity string, identityArgs ...any) error {
	nextRetryAt := ""
	if !update.NextRetryAt.IsZero() {
		nextRetryAt = db.FormatReliabilityTime(update.NextRetryAt)
	}
	where := identity + " AND status = ? AND claim_token = ? AND attempt = ?"
	args := append(identityArgs, DeliveryClaimed, update.ClaimToken, update.Attempt)
	result := query.Where(where, args...).Updates(map[string]any{
		"status": update.Status, "claim_token": "", "lease_until": "", "next_retry_at": nextRetryAt,
		"last_error": update.LastError, "apns_id": update.APNSID, "updated_at": db.FormatReliabilityTime(time.Now()),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDeliveryFenced
	}
	return nil
}

func deactivateTokenTx(tx *gorm.DB, tokenID int64) error {
	return tx.Model(&db.PushDeviceToken{}).Where("id = ?", tokenID).Updates(map[string]any{
		"is_active": false, "updated_at": db.FormatReliabilityTime(time.Now()),
	}).Error
}

func (s *GormStore) completeVoIPJobIfTerminal(tx *gorm.DB, jobID string) error {
	job, err := lockPushJob(tx, jobID)
	if err != nil {
		return err
	}
	return s.completeVoIPJobIfTerminalLocked(tx, job)
}

func (s *GormStore) completeVoIPJobIfTerminalLocked(tx *gorm.DB, job db.PushJob) error {
	var outstanding int64
	if err := tx.Model(&db.PushVoIPDelivery{}).Where("job_id = ? AND status IN ?", job.JobID, []string{DeliveryPending, DeliveryClaimed, DeliveryRetryable}).Count(&outstanding).Error; err != nil {
		return err
	}
	if outstanding > 0 {
		return nil
	}
	if job.Status == PushJobCompleted {
		return nil
	}
	request, err := validateRequestPayload(job.RequestPayload)
	if err != nil {
		return err
	}
	call := request.GetVoipCall()
	if call == nil {
		return errors.New("durable VoIP job has no call request")
	}
	var sent int64
	if err := tx.Model(&db.PushVoIPDelivery{}).Where("job_id = ? AND status = ?", job.JobID, DeliverySent).Count(&sent).Error; err != nil {
		return err
	}
	accepted := sent > 0
	reason := "apns_delivery_failed"
	if accepted {
		reason = "accepted_by_apns"
	} else {
		var failed db.PushVoIPDelivery
		if err := tx.Where("job_id = ?", job.JobID).Order("updated_at DESC").First(&failed).Error; err == nil && failed.LastError != "" {
			reason = failed.LastError
		}
	}
	now := time.Now().UTC()
	_, payload, err := voipResultPayload(call, accepted, reason, now)
	if err != nil {
		return err
	}
	outboxEvent := db.OutboxEvent{
		EventID: "push:" + job.JobID + ":voip-result", Service: "push", OperationKey: job.OperationKey,
		Topic: call.GetResultKafkaTopic(), Payload: payload, Status: db.OutboxStatusPending,
		NextAttemptAt: db.FormatReliabilityTime(now), CreatedAt: db.FormatReliabilityTime(now), UpdatedAt: db.FormatReliabilityTime(now),
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&outboxEvent).Error; err != nil {
		return err
	}
	return tx.Model(&db.PushJob{}).Where("job_id = ? AND status = ?", job.JobID, PushJobPending).Updates(map[string]any{
		"status": PushJobCompleted, "completed_at": db.FormatReliabilityTime(now), "updated_at": db.FormatReliabilityTime(now),
	}).Error
}

func lockPushJob(tx *gorm.DB, jobID string) (db.PushJob, error) {
	var job db.PushJob
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("job_id = ?", jobID).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.PushJob{}, ErrDeliveryFenced
	}
	return job, err
}

func newClaimToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate push claim token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func decodeClaimRequest(claim DurableDeliveryClaim) (*pushpb.RequestMessage, error) {
	request := &pushpb.RequestMessage{}
	if err := proto.Unmarshal(claim.RequestPayload, request); err != nil {
		return nil, err
	}
	return request, nil
}
