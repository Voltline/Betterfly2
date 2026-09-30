package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInboxIncomplete = errors.New("consumer inbox operation is not completed")

type PendingOutboxEvent struct {
	EventID string
	Topic   string
	Payload []byte
}

type InboxExecution struct {
	ResponsePayload []byte
	Replayed        bool
}

func StableEventID(service, operationKey, suffix string) string {
	digest := sha256.Sum256([]byte(service + "\x00" + operationKey + "\x00" + suffix))
	return strings.TrimSpace(service) + "-" + hex.EncodeToString(digest[:])
}

// ExecuteInboxOutbox commits the inbox marker, business writes performed by
// execute, serialized response and outbox events in one PostgreSQL transaction.
func ExecuteInboxOutbox(
	ctx context.Context,
	database *gorm.DB,
	service string,
	operationKey string,
	execute func(*gorm.DB) ([]byte, []PendingOutboxEvent, error),
) (InboxExecution, error) {
	service = strings.TrimSpace(service)
	operationKey = strings.TrimSpace(operationKey)
	if database == nil || service == "" || operationKey == "" || execute == nil {
		return InboxExecution{}, errors.New("invalid inbox execution configuration")
	}

	var result InboxExecution
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := FormatReliabilityTime(time.Now())
		candidate := ConsumerInbox{
			Service: service, OperationKey: operationKey,
			Status: "processing", CreatedAt: now,
		}
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
		if insert.Error != nil {
			return insert.Error
		}
		if insert.RowsAffected == 0 {
			var existing ConsumerInbox
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("service = ? AND operation_key = ?", service, operationKey).
				First(&existing).Error; err != nil {
				return err
			}
			if existing.Status != InboxStatusCompleted {
				return ErrInboxIncomplete
			}
			result = InboxExecution{ResponsePayload: append([]byte(nil), existing.ResponsePayload...), Replayed: true}
			return nil
		}

		response, events, err := execute(tx)
		if err != nil {
			return err
		}
		for _, event := range events {
			if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.Topic) == "" || len(event.Payload) == 0 {
				return errors.New("invalid outbox event")
			}
		}
		if events == nil {
			events = []PendingOutboxEvent{}
		}
		encodedEvents, err := json.Marshal(events)
		if err != nil {
			return err
		}
		// Persist events and complete the inbox in one round trip, still inside
		// the business transaction. JSON carries binary payloads as base64.
		updated := tx.Exec(`WITH persisted_events AS (
INSERT INTO outbox_events (
  event_id, service, operation_key, topic, payload, status,
  next_attempt_at, created_at, updated_at, attempt,
  claim_token, lease_until, last_error, published_at
)
SELECT event->>'EventID', ?, ?, event->>'Topic',
  decode(event->>'Payload', 'base64'), ?, ?, ?, ?, 0, '', '', '', ''
FROM jsonb_array_elements(CAST(? AS jsonb)) AS event
RETURNING event_id
)
UPDATE consumer_inboxes
SET status = ?, response_payload = ?, completed_at = ?
WHERE service = ? AND operation_key = ? AND status = ?
  AND (SELECT COUNT(*) FROM persisted_events) = ?`,
			service, operationKey, OutboxStatusPending, now, now, now, string(encodedEvents),
			InboxStatusCompleted, append([]byte(nil), response...), now, service, operationKey, "processing", len(events))
		if updated.Error != nil {
			return fmt.Errorf("persist inbox response and outbox events: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrInboxIncomplete
		}
		result = InboxExecution{ResponsePayload: append([]byte(nil), response...)}
		return nil
	})
	return result, err
}
