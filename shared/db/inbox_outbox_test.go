package db

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newInboxDatabase(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
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

func expectSuccessfulInboxTransaction(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO test_side_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestExecuteInboxOutboxCommitsBusinessResponseAndEventAtomically(t *testing.T) {
	database, mock := newInboxDatabase(t)
	expectSuccessfulInboxTransaction(mock)

	wantResponse := []byte("serialized-response")
	execution, err := ExecuteInboxOutbox(context.Background(), database, "friend", "topic/1/7", func(tx *gorm.DB) ([]byte, []PendingOutboxEvent, error) {
		if err := tx.Exec(`INSERT INTO test_side_effects (id) VALUES (?)`, 7).Error; err != nil {
			return nil, nil, err
		}
		return wantResponse, []PendingOutboxEvent{{EventID: "event-7", Topic: "df-pod", Payload: []byte("envelope")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Replayed || !bytes.Equal(execution.ResponsePayload, wantResponse) {
		t.Fatalf("unexpected inbox execution: %+v", execution)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteInboxOutboxTransientFailureRollsBackAndIsNotCached(t *testing.T) {
	database, mock := newInboxDatabase(t)
	injected := errors.New("database temporarily unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO test_side_effects`).WillReturnError(injected)
	mock.ExpectRollback()

	callbackCalls := 0
	execute := func(tx *gorm.DB) ([]byte, []PendingOutboxEvent, error) {
		callbackCalls++
		if err := tx.Exec(`INSERT INTO test_side_effects (id) VALUES (?)`, 8).Error; err != nil {
			return nil, nil, err
		}
		return []byte("ok"), []PendingOutboxEvent{{EventID: "event-8", Topic: "df-pod", Payload: []byte("envelope")}}, nil
	}
	if _, err := ExecuteInboxOutbox(context.Background(), database, "storage", "topic/0/8", execute); !errors.Is(err, injected) {
		t.Fatalf("expected injected transient failure, got %v", err)
	}

	expectSuccessfulInboxTransaction(mock)
	if _, err := ExecuteInboxOutbox(context.Background(), database, "storage", "topic/0/8", execute); err != nil {
		t.Fatalf("retry after rollback failed: %v", err)
	}
	if callbackCalls != 2 {
		t.Fatalf("failed operation was cached as completed: callback calls=%d", callbackCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteInboxOutboxReplaysCompletedOperationWithoutBusinessWrite(t *testing.T) {
	database, mock := newInboxDatabase(t)
	want := []byte("existing-response")
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT \* FROM "consumer_inboxes"`).WillReturnRows(sqlmock.NewRows([]string{
		"service", "operation_key", "status", "response_payload", "created_at", "completed_at",
	}).AddRow("push", "topic/2/9", InboxStatusCompleted, want, "2026-07-15T00:00:00Z", "2026-07-15T00:00:01Z"))
	mock.ExpectCommit()

	callbackCalls := 0
	execution, err := ExecuteInboxOutbox(context.Background(), database, "push", "topic/2/9", func(*gorm.DB) ([]byte, []PendingOutboxEvent, error) {
		callbackCalls++
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !execution.Replayed || callbackCalls != 0 || !bytes.Equal(execution.ResponsePayload, want) {
		t.Fatalf("completed operation was executed again: execution=%+v calls=%d", execution, callbackCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStableEventIDIsBoundedAndDeterministicForLongKafkaTopic(t *testing.T) {
	operationKey := strings.Repeat("long-kafka-topic", 24) + "/2147483647/9223372036854775807"
	first := StableEventID("storage", operationKey, "response")
	second := StableEventID("storage", operationKey, "response")
	if first != second {
		t.Fatalf("stable event id changed: %q != %q", first, second)
	}
	if len(first) > 255 {
		t.Fatalf("stable event id exceeds database and Kafka header bounds: %d", len(first))
	}
	if first == StableEventID("storage", operationKey, "different-response") {
		t.Fatal("different logical events received the same stable id")
	}
}
