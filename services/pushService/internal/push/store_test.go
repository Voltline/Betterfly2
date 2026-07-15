package push

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newStoreMock(t *testing.T) (*GormStore, sqlmock.Sqlmock) {
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
	return &GormStore{db: database}, mock
}

func TestListMessageTokensUsesOneBatchQuery(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectQuery(`WITH targets AS .*SELECT token\.\*`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "token", "environment", "push_type", "is_active"}).
			AddRow(1, 2, "token-a", "production", PushTypeAPNs, true).
			AddRow(2, 3, "token-b", "production", PushTypeAPNs, true))
	targets := make([]int64, 20000)
	for index := range targets {
		targets[index] = int64(index + 2)
	}
	tokens, err := store.ListMessageTokens(context.Background(), targets, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 {
		t.Fatalf("unexpected token result: %+v", tokens)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("target count caused extra database queries: %v", err)
	}
	if placeholders := strings.Count(legacyListMessageTokensSQL, "?"); placeholders != 4 {
		t.Fatalf("legacy token query placeholders scale with targets: %d", placeholders)
	}
}

func TestClaimAndFinalizeMessageDeliveriesUseBatchStatements(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectQuery(`INSERT INTO push_message_deliveries`).
		WillReturnRows(sqlmock.NewRows([]string{"token_id", "attempt"}).AddRow(10, 1).AddRow(11, 1))
	mock.ExpectQuery(`UPDATE push_message_deliveries.*RETURNING delivery\.token_id, delivery\.attempt`).
		WillReturnRows(sqlmock.NewRows([]string{"token_id", "attempt"}))
	mock.ExpectQuery(`SELECT count\(\*\).*FROM push_message_deliveries`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	now := time.Now().UTC()
	claims, pending, err := store.ClaimMessageDeliveries(context.Background(), 99, []int64{10, 11}, now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pending || claims[10] != 1 || claims[11] != 1 {
		t.Fatalf("unexpected claims: claims=%+v pending=%v", claims, pending)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE push_message_deliveries AS delivery SET`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	if err := store.FinalizeMessageDeliveries(context.Background(), []DeliveryUpdate{
		{MessageID: 99, TokenID: 10, Status: DeliverySent, APNSID: "apns-a"},
		{MessageID: 99, TokenID: 11, Status: DeliveryRetryable, NextRetryAt: now, LastError: "network_or_sender_error"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeInvalidTokenIsAtomicWithPermanentDelivery(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE push_message_deliveries AS delivery SET`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE push_device_tokens AS token SET`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.FinalizeMessageDeliveries(context.Background(), []DeliveryUpdate{{
		MessageID: 100, TokenID: 12, Status: DeliveryPermanent,
		LastError: "apns_status=410 reason=Unregistered", DeactivateToken: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeInvalidTokenRollsBackWhenDeactivationFails(t *testing.T) {
	store, mock := newStoreMock(t)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE push_message_deliveries AS delivery SET`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE push_device_tokens AS token SET`).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()
	err := store.FinalizeMessageDeliveries(context.Background(), []DeliveryUpdate{{
		MessageID: 101, TokenID: 13, Status: DeliveryPermanent, DeactivateToken: true,
	}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected atomic deactivation failure, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
