package db

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRetentionAlwaysExceedsKafkaReplayWindow(t *testing.T) {
	t.Setenv("CONSUMER_STATE_RETENTION", "24h")
	t.Setenv("KAFKA_MAX_REPLAY_WINDOW", "48h")
	t.Setenv("RELIABILITY_CLEANUP_BATCH_SIZE", "50000")
	config := LoadRetentionConfig()
	if config.Retention != 72*time.Hour {
		t.Fatalf("retention=%s want=72h", config.Retention)
	}
	if config.BatchSize != 1000 {
		t.Fatalf("unbounded cleanup batch was accepted: %d", config.BatchSize)
	}
}

func TestReliabilityCleanupUsesLimitedStatementsOutsideWritePath(t *testing.T) {
	database, mock := newInboxDatabase(t)
	for index := 0; index < 3; index++ {
		mock.ExpectExec(`DELETE FROM`).WithArgs(sqlmock.AnyArg(), 50).WillReturnResult(sqlmock.NewResult(0, int64(index+1)))
	}
	config := RetentionConfig{Retention: 30 * 24 * time.Hour, ReplayWindow: 7 * 24 * time.Hour, BatchSize: 50}
	if err := CleanupReliabilityState(context.Background(), database, config, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
