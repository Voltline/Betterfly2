package push

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPushCleanupRetentionExceedsReplayWindow(t *testing.T) {
	t.Setenv("PUSH_DELIVERY_RETENTION", "24h")
	t.Setenv("KAFKA_MAX_REPLAY_WINDOW", "72h")
	t.Setenv("PUSH_CLEANUP_BATCH_SIZE", "20000")
	config := LoadCleanupConfig()
	if config.Retention != 96*time.Hour {
		t.Fatalf("retention=%s want=96h", config.Retention)
	}
	if config.BatchSize != 1000 {
		t.Fatalf("unbounded push cleanup batch was accepted: %d", config.BatchSize)
	}
}

func TestPushCleanupDeletesDeliveriesBeforeCompletedJobsInBoundedBatches(t *testing.T) {
	store, mock := newStoreMock(t)
	for index := 0; index < 3; index++ {
		mock.ExpectExec(`DELETE FROM`).WithArgs(sqlmock.AnyArg(), 25).WillReturnResult(sqlmock.NewResult(0, int64(index+1)))
	}
	counts, err := store.CleanupCompletedDeliveries(context.Background(), CleanupConfig{Retention: 30 * 24 * time.Hour, BatchSize: 25}, time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if counts["message_delivery"] != 1 || counts["voip_delivery"] != 2 || counts["job"] != 3 {
		t.Fatalf("unexpected cleanup counts: %+v", counts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
