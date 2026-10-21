package db

import (
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestUpsertPendingFileMetadataNeverDowngradesVerifiedFile(t *testing.T) {
	var statements []string
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		statements = append(statements, actual)
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := UpsertPendingFileMetadataWithDB(database, "hash", 10, "ha/hash"); err != nil {
		t.Fatal(err)
	}
	if len(statements) != 1 {
		t.Fatalf("expected one upsert statement, got %q", statements)
	}
	sql := statements[0]
	conflict := sql[strings.Index(sql, "ON CONFLICT"):]
	if !strings.Contains(conflict, `WHERE "file_metadata"."is_verified" =`) {
		t.Fatalf("pending upsert must only touch unverified rows: %s", sql)
	}
	if strings.Contains(conflict, `"is_verified"=`) {
		t.Fatalf("pending upsert must not rewrite is_verified on conflict: %s", sql)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
