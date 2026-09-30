package db

import (
	"Betterfly2/shared/kafkaconsumer"
	"bytes"
	"context"
	"errors"
	"github.com/IBM/sarama"
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
	mock.ExpectExec(`WITH persisted_events AS`).WillReturnResult(sqlmock.NewResult(0, 1))
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

func TestExecuteInboxOutboxCompletionFailuresRollBack(t *testing.T) {
	for _, failure := range []string{"write", "fencing", "commit", "invalid_event"} {
		t.Run(failure, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO test_side_effects`).WillReturnResult(sqlmock.NewResult(0, 1))
			injected := errors.New("injected completion failure")
			switch failure {
			case "write":
				mock.ExpectExec(`WITH persisted_events AS`).WillReturnError(injected)
				mock.ExpectRollback()
			case "fencing":
				mock.ExpectExec(`WITH persisted_events AS`).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectRollback()
			case "commit":
				mock.ExpectExec(`WITH persisted_events AS`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit().WillReturnError(injected)
			case "invalid_event":
				mock.ExpectRollback()
			}
			_, err := ExecuteInboxOutbox(context.Background(), database, "storage", "completion-failure", func(tx *gorm.DB) ([]byte, []PendingOutboxEvent, error) {
				if err := tx.Exec(`INSERT INTO test_side_effects (id) VALUES (1)`).Error; err != nil {
					return nil, nil, err
				}
				event := PendingOutboxEvent{EventID: "event-failure", Topic: "df-pod", Payload: []byte{0, 255, 42}}
				if failure == "invalid_event" {
					event.Topic = ""
				}
				return []byte("response"), []PendingOutboxEvent{event}, nil
			})
			if err == nil {
				t.Fatal("failed completion was reported successful")
			}
			if failure == "fencing" && !errors.Is(err, ErrInboxIncomplete) {
				t.Fatalf("expected fencing failure, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExecuteInboxOutboxZeroAndMultipleEventsUseOneCompletionStatement(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(map[int]string{0: "zero", 2: "multiple"}[count], func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`WITH persisted_events AS`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			_, err := ExecuteInboxOutbox(context.Background(), database, "push", "event-count", func(*gorm.DB) ([]byte, []PendingOutboxEvent, error) {
				var events []PendingOutboxEvent
				for i := 0; i < count; i++ {
					events = append(events, PendingOutboxEvent{EventID: StableEventID("push", "event-count", strings.Repeat("x", i+1)), Topic: "df", Payload: []byte{0, 255}})
				}
				return []byte("response"), events, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplayedKafkaOffsetUsesCompletedInboxWithoutBusinessWrite(t *testing.T) {
	for _, key := range []string{"friend-service/1/7", "event/event-42"} {
		t.Run(key, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT \* FROM "consumer_inboxes"`).WithArgs("friend", key, 1).WillReturnRows(sqlmock.NewRows([]string{"service", "operation_key", "status", "response_payload"}).AddRow("friend", key, InboxStatusCompleted, []byte("original-response")))
			mock.ExpectCommit()
			message := &sarama.ConsumerMessage{Topic: "friend-service", Partition: 3, Offset: 999,
				Headers: []*sarama.RecordHeader{{Key: []byte("operation_key"), Value: []byte(key)}}}
			execution, err := ExecuteInboxOutbox(context.Background(), database, "friend", kafkaconsumer.OperationKey(message), func(*gorm.DB) ([]byte, []PendingOutboxEvent, error) {
				t.Fatal("DLQ replay executed relationship mutation again")
				return nil, nil, nil
			})
			if err != nil || !execution.Replayed || string(execution.ResponsePayload) != "original-response" {
				t.Fatalf("replay did not use completed inbox: %+v err=%v", execution, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
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
