package db

import (
	"bytes"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestConsumerOperationResultPersistsExactResponseForReplay(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previous := DB
	DB = func(...interface{}) *gorm.DB { return database }
	t.Cleanup(func() {
		DB = previous
		_ = sqlDB.Close()
	})

	payload := []byte{0x01, 0x02, 0x03}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_operation_results"`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := SaveConsumerOperationResult("friend", "friend-service/2/41", payload); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT \* FROM "consumer_operation_results"`).
		WillReturnRows(sqlmock.NewRows([]string{"service", "operation_key", "response_payload", "created_at"}).
			AddRow("friend", "friend-service/2/41", payload, "2026-07-15T00:00:00Z"))
	replayed, err := LoadConsumerOperationResult("friend", "friend-service/2/41")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replayed, payload) {
		t.Fatalf("replayed payload changed: got=%x want=%x", replayed, payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
