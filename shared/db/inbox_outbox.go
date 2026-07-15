package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
			row := OutboxEvent{
				EventID: event.EventID, Service: service, OperationKey: operationKey,
				Topic: event.Topic, Payload: append([]byte(nil), event.Payload...),
				Status: OutboxStatusPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("persist outbox event %s: %w", event.EventID, err)
			}
		}
		updated := tx.Model(&ConsumerInbox{}).
			Where("service = ? AND operation_key = ? AND status = ?", service, operationKey, "processing").
			Updates(map[string]any{
				"status": InboxStatusCompleted, "response_payload": append([]byte(nil), response...), "completed_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrInboxIncomplete
		}
		result = InboxExecution{ResponsePayload: append([]byte(nil), response...)}
		return nil
	})
	return result, err
}

func LoadConsumerInbox(database *gorm.DB, service, operationKey string) (*ConsumerInbox, error) {
	if database == nil {
		return nil, errors.New("database is nil")
	}
	var inbox ConsumerInbox
	err := database.Where("service = ? AND operation_key = ?", service, operationKey).First(&inbox).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inbox, nil
}
