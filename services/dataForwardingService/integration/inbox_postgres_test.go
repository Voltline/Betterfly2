package integration

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestInboxPostgresCombinedCompletion(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("set BETTERFLY_ACCEPTANCE=1 to verify inbox transactions against existing PostgreSQL")
	}
	database := acceptanceDatabase(t)
	service := randomAccount(t, "inbox")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A private, deleted fixture has no members and is never sent to Kafka.
	group := db.Group{GroupID: -time.Now().UnixNano(), Name: "before", IsDelete: true}
	if err := database.WithContext(ctx).Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, model := range []any{&db.OutboxEvent{}, &db.ConsumerInbox{}} {
			if err := database.WithContext(cleanup).Where("service = ?", service).Delete(model).Error; err != nil {
				t.Errorf("private inbox fixture cleanup: %v", err)
			}
		}
		if err := database.WithContext(cleanup).Where("group_id = ?", group.GroupID).Delete(&db.Group{}).Error; err != nil {
			t.Errorf("private group fixture cleanup: %v", err)
		}
	})
	response := []byte{0, 255, 42, '\n'}
	events := []db.PendingOutboxEvent{
		{EventID: service + "-one", Topic: "not-published-to-kafka", Payload: []byte{0, 255, 1, '\n'}},
		{EventID: service + "-two", Topic: "not-published-to-kafka", Payload: []byte("binary \"response\" \\ escaped")},
	}
	for _, count := range []int{0, 2} {
		key := fmt.Sprintf("test/%d", count)
		var calls atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				execution, err := db.ExecuteInboxOutbox(ctx, database, service, key, func(tx *gorm.DB) ([]byte, []db.PendingOutboxEvent, error) {
					calls.Add(1)
					if err := tx.Model(&db.Group{}).Where("group_id = ?", group.GroupID).Update("name", key).Error; err != nil {
						return nil, nil, err
					}
					if count == 0 {
						return response, nil, nil
					}
					return response, events, nil
				})
				if err != nil || !bytes.Equal(execution.ResponsePayload, response) {
					t.Errorf("concurrent operation %s: response mismatch=%t err=%v", key, !bytes.Equal(execution.ResponsePayload, response), err)
				}
			}()
		}
		wg.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent operation %s executed business %d times", key, calls.Load())
		}
		var inbox db.ConsumerInbox
		if err := database.WithContext(ctx).Where("service = ? AND operation_key = ?", service, key).First(&inbox).Error; err != nil || inbox.Status != db.InboxStatusCompleted || !bytes.Equal(inbox.ResponsePayload, response) {
			t.Fatalf("inbox did not preserve response: err=%v", err)
		}
		var persisted []db.OutboxEvent
		if err := database.WithContext(ctx).Where("service = ? AND operation_key = ?", service, key).Order("event_id").Find(&persisted).Error; err != nil || len(persisted) != count {
			t.Fatalf("event count=%d want=%d err=%v", len(persisted), count, err)
		}
		for i, event := range persisted {
			if !bytes.Equal(event.Payload, events[i].Payload) || event.Status != db.OutboxStatusPending || event.Attempt != 0 || event.ClaimToken != "" {
				t.Fatal("combined statement changed payload or initial outbox state")
			}
		}
	}

	// Conflict in the outbox statement must roll back the earlier business write
	// and inbox insert, not merely fail after changing the group.
	if _, err := db.ExecuteInboxOutbox(ctx, database, service, "test/conflict", func(tx *gorm.DB) ([]byte, []db.PendingOutboxEvent, error) {
		if err := tx.Model(&db.Group{}).Where("group_id = ?", group.GroupID).Update("name", "must-roll-back").Error; err != nil {
			return nil, nil, err
		}
		return response, events[:1], nil
	}); err == nil {
		t.Fatal("duplicate event ID did not fail the transaction")
	}
	var remaining int64
	if err := database.WithContext(ctx).Model(&db.ConsumerInbox{}).Where("service = ? AND operation_key = ?", service, "test/conflict").Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("failed operation cached a completed inbox: count=%d err=%v", remaining, err)
	}
	var unchanged db.Group
	if err := database.WithContext(ctx).Where("group_id = ?", group.GroupID).First(&unchanged).Error; err != nil || unchanged.Name != "test/2" {
		t.Fatalf("business write survived rollback: err=%v", err)
	}
	t.Log("PASS actual PostgreSQL: zero/multiple binary events, concurrent replay executes once, outbox conflict rolls back business and inbox")
}
