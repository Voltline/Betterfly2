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

func expectRelayClaim(mock sqlmock.Sqlmock, service string, attempt int) {
	mock.ExpectQuery(`WITH candidate AS .*LIMIT 1 FOR UPDATE SKIP LOCKED.*RETURNING event\.\*`).
		WithArgs(service, db.OutboxStatusPending, db.OutboxStatusRetryable, db.OutboxStatusFailed, sqlmock.AnyArg(), db.OutboxStatusClaimed, sqlmock.AnyArg(), db.OutboxStatusClaimed, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"event_id", "service", "operation_key", "topic", "payload", "status", "attempt", "claim_token", "lease_until", "next_attempt_at", "created_at", "updated_at",
		}).AddRow("event-1", service, "source/0/1", "df-pod", []byte("envelope"), db.OutboxStatusClaimed, attempt+1, "new-claim", "2026-07-15T00:00:00Z", "2026-07-15T00:00:00Z", "2026-07-14T00:00:00Z", "2026-07-14T00:00:00Z"))
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
	}, Config{Service: "friend", Lease: time.Second, PublishTimeout: 500 * time.Millisecond, Now: func() time.Time { return now }})

	expectRelayClaim(mock, "friend", 0)
	injected := errors.New("database failed after Kafka accepted event")
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnError(injected)
	if _, err := relay.RunOnce(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("expected post-publish persistence failure, got %v", err)
	}

	now = now.Add(2 * time.Second)
	expectRelayClaim(mock, "friend", 1)
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
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
	}, Config{Service: "storage", Lease: 10 * time.Second, PublishTimeout: time.Second, InitialBackoff: time.Second, MaxBackoff: time.Minute, AlertAfterAttempts: 3, Now: func() time.Time { return now }})

	expectRelayClaim(mock, "storage", 0)
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
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
	}, Config{Service: "friend", Lease: time.Second, PublishTimeout: 500 * time.Millisecond, Now: func() time.Time { return now }})

	expectRelayClaim(mock, "friend", 20)
	mock.ExpectExec(`UPDATE "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
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

func TestRelayEmptyClaimDoesNotPublish(t *testing.T) {
	database, mock := newRelayDatabase(t)
	relay := New(database, func(context.Context, db.OutboxEvent) error {
		t.Fatal("empty claim must not publish")
		return nil
	}, Config{Service: "storage"})
	mock.ExpectQuery(`WITH candidate AS`).WillReturnRows(sqlmock.NewRows([]string{"event_id"}))
	if count, err := relay.RunOnce(context.Background()); err != nil || count != 0 {
		t.Fatalf("empty claim: count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayCommittedWakeupDoesNotWaitForPoll(t *testing.T) {
	database, mock := newRelayDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := make(chan struct{}, 1)
	committed <- struct{}{}
	var publishes atomic.Int32
	relay := New(database, func(context.Context, db.OutboxEvent) error {
		publishes.Add(1)
		cancel()
		return nil
	}, Config{Service: "storage", PollInterval: time.Hour, Committed: committed})
	mock.ExpectQuery(`WITH candidate AS`).WillReturnRows(sqlmock.NewRows([]string{"event_id"}))
	expectRelayClaim(mock, "storage", 0)
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil || publishes.Load() != 1 {
			t.Fatalf("commit wakeup failed: publishes=%d err=%v", publishes.Load(), err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("relay waited for poll despite a committed event")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayClaimFailureDoesNotPublish(t *testing.T) {
	database, mock := newRelayDatabase(t)
	relay := New(database, func(context.Context, db.OutboxEvent) error {
		t.Fatal("failed claim must not publish")
		return nil
	}, Config{Service: "storage"})
	injected := errors.New("claim database unavailable")
	mock.ExpectQuery(`WITH candidate AS`).WillReturnError(injected)
	if count, err := relay.RunOnce(context.Background()); !errors.Is(err, injected) || count != 0 {
		t.Fatalf("claim failure: count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayFinalizeRetainsClaimTokenFencing(t *testing.T) {
	for _, publishFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "retryable"}[publishFails], func(t *testing.T) {
			database, mock := newRelayDatabase(t)
			relay := New(database, func(context.Context, db.OutboxEvent) error {
				if publishFails {
					return context.DeadlineExceeded
				}
				return nil
			}, Config{Service: "friend"})
			expectRelayClaim(mock, "friend", 0)
			mock.ExpectExec(`UPDATE "outbox_events" .*WHERE event_id = \$[0-9]+ AND status = \$[0-9]+ AND claim_token = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 0))
			if count, err := relay.RunOnce(context.Background()); count != 1 || err == nil {
				t.Fatalf("stale finalize must fail: count=%d err=%v", count, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
