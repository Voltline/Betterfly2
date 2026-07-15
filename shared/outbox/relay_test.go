package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"Betterfly2/shared/db"
	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newRelayDatabase(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return database, mock
}

func expectRelayClaim(mock sqlmock.Sqlmock, status string, attempt int) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM outbox_events`).WillReturnRows(sqlmock.NewRows([]string{
		"event_id", "service", "operation_key", "topic", "payload", "status", "attempt", "claim_token", "lease_until", "next_attempt_at", "created_at", "updated_at",
	}).AddRow("event-1", "friend", "source/0/1", "df-pod", []byte("envelope"), status, attempt, "old-claim", "2026-07-15T00:00:00Z", "2026-07-15T00:00:00Z", "2026-07-14T00:00:00Z", "2026-07-14T00:00:00Z"))
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestRelayCrashAfterPublishReplaysStableEventAtLeastOnce(t *testing.T) {
	database, mock := newRelayDatabase(t)
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	var publishes atomic.Int32
	relay := New(database, func(_ context.Context, event db.OutboxEvent) error {
		if event.EventID != "event-1" || event.OperationKey != "source/0/1" {
			t.Fatalf("unstable outbox identity: %+v", event)
		}
		publishes.Add(1)
		return nil
	}, Config{Service: "friend", BatchSize: 1, Lease: time.Second, PublishTimeout: 500 * time.Millisecond, Now: func() time.Time { return now }})

	expectRelayClaim(mock, db.OutboxStatusPending, 0)
	injected := errors.New("database failed after Kafka accepted event")
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnError(injected)
	mock.ExpectRollback()
	if _, err := relay.RunOnce(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("expected post-publish persistence failure, got %v", err)
	}

	now = now.Add(2 * time.Second)
	expectRelayClaim(mock, db.OutboxStatusClaimed, 1)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if processed, err := relay.RunOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("lease recovery failed: processed=%d err=%v", processed, err)
	}
	if publishes.Load() != 2 {
		t.Fatalf("at-least-once crash boundary not exercised: publishes=%d", publishes.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayPublishFailurePersistsRetryableState(t *testing.T) {
	database, mock := newRelayDatabase(t)
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	relay := New(database, func(context.Context, db.OutboxEvent) error {
		return context.DeadlineExceeded
	}, Config{Service: "storage", BatchSize: 1, Lease: 10 * time.Second, PublishTimeout: time.Second, InitialBackoff: time.Second, MaxBackoff: time.Minute, AlertAfterAttempts: 3, Now: func() time.Time { return now }})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM outbox_events`).WillReturnRows(sqlmock.NewRows([]string{
		"event_id", "service", "operation_key", "topic", "payload", "status", "attempt", "next_attempt_at", "created_at",
	}).AddRow("event-2", "storage", "source/0/2", "df-pod", []byte("envelope"), db.OutboxStatusPending, 0, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if processed, err := relay.RunOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("retryable publish handling failed: processed=%d err=%v", processed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayReclaimsLegacyFailedEvent(t *testing.T) {
	database, mock := newRelayDatabase(t)
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	var publishes atomic.Int32
	relay := New(database, func(context.Context, db.OutboxEvent) error {
		publishes.Add(1)
		return nil
	}, Config{Service: "friend", BatchSize: 1, Lease: time.Second, PublishTimeout: 500 * time.Millisecond, Now: func() time.Time { return now }})

	expectRelayClaim(mock, db.OutboxStatusFailed, 20)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if processed, err := relay.RunOnce(context.Background()); err != nil || processed != 1 {
		t.Fatalf("legacy failed event was not recovered: processed=%d err=%v", processed, err)
	}
	if publishes.Load() != 1 {
		t.Fatalf("legacy failed event was not published: %d", publishes.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
