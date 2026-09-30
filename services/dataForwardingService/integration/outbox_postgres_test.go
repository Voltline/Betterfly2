package integration

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/outbox"
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboxPostgresAtomicClaim(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("set BETTERFLY_ACCEPTANCE=1 to verify claims against existing PostgreSQL")
	}
	database := acceptanceDatabase(t)
	service := randomAccount(t, "claim")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := database.WithContext(cleanup).Where("service = ?", service).Delete(&db.OutboxEvent{}).Error; err != nil {
			t.Errorf("test-only outbox cleanup: %v", err)
		}
	})
	now := time.Now().UTC()
	create := func(id string) {
		t.Helper()
		row := db.OutboxEvent{Service: service, EventID: id, OperationKey: "test/" + id, Topic: "not-published-to-kafka", Payload: []byte("test-only"), Status: db.OutboxStatusPending, CreatedAt: db.FormatReliabilityTime(now), UpdatedAt: db.FormatReliabilityTime(now)}
		if err := database.WithContext(ctx).Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	config := outbox.Config{Service: service, Lease: 5 * time.Second, PublishTimeout: 4 * time.Second, Now: func() time.Time { return now }}
	create(service + "-single")
	var publishes atomic.Int32
	publish := func(_ context.Context, event db.OutboxEvent) error {
		if event.Status != db.OutboxStatusClaimed || event.ClaimToken == "" || event.Attempt != 1 {
			t.Error("incomplete atomic claim")
		}
		publishes.Add(1)
		return nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := outbox.New(database, publish, config).RunOnce(ctx); err != nil {
				t.Errorf("concurrent claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if publishes.Load() != 1 {
		t.Fatalf("concurrent claims published %d times", publishes.Load())
	}
	t.Log("PASS actual PostgreSQL: two relays atomically claim one event, only one publisher runs")

	create(service + "-fencing")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	result := make(chan error, 1)
	go func() {
		_, err := outbox.New(database, func(ctx context.Context, _ db.OutboxEvent) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, config).RunOnce(ctx)
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first worker did not claim")
	}
	later := config
	later.Now = func() time.Time { return now.Add(6 * time.Second) }
	if count, err := outbox.New(database, func(context.Context, db.OutboxEvent) error { return nil }, later).RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("expired claim not recovered: count=%d err=%v", count, err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "fencing failure") {
			t.Fatalf("stale worker finalized: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stale worker did not finish")
	}
	t.Log("PASS actual PostgreSQL: expired claim recovered; stale publisher cannot finalize new owner's event")
}
