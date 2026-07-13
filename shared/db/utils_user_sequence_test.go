package db

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func useUserMockDB(t *testing.T) (sqlmock.Sqlmock, *gorm.DB) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previous := DB
	DB = func(...interface{}) *gorm.DB { return database }
	t.Cleanup(func() {
		DB = previous
		_ = sqlDB.Close()
	})
	return mock, database
}

func TestAddUserUsesDatabaseGeneratedIDAndSupportsConcurrentRegistration(t *testing.T) {
	mock, _ := useUserMockDB(t)
	mock.MatchExpectationsInOrder(false)
	const count = 32
	for i := 1; i <= count; i++ {
		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO "users".*RETURNING "id"`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(i))
		mock.ExpectCommit()
	}

	users := make([]*User, count)
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for i := range users {
		users[i] = &User{Account: fmt.Sprintf("account-%d", i), Name: "name"}
		wg.Add(1)
		go func(user *User) {
			defer wg.Done()
			errorsCh <- AddUser(user)
		}(users[i])
	}
	wg.Wait()
	close(errorsCh)
	ids := make(map[int64]struct{}, count)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range users {
		if user.ID <= 0 {
			t.Fatalf("database-generated ID was not populated: %+v", user)
		}
		ids[user.ID] = struct{}{}
	}
	if len(ids) != count {
		t.Fatalf("concurrent registrations produced duplicate IDs: %v", ids)
	}
}

func TestAddUserAllowsExplicitIDAndClassifiesUniqueConflicts(t *testing.T) {
	mock, _ := useUserMockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "users".*"id".*RETURNING "id"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9001))
	mock.ExpectExec(`SELECT setval\('users_id_seq'.*GREATEST`).WithArgs(int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := AddUser(&User{ID: 9001, Account: "imported"}); err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "users".*RETURNING "id"`).WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "users_account_key"})
	mock.ExpectRollback()
	if err := AddUser(&User{Account: "imported"}); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("account conflict was misclassified: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "users".*"id".*RETURNING "id"`).WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "users_pkey"})
	mock.ExpectRollback()
	if err := AddUser(&User{ID: 9001, Account: "other"}); !errors.Is(err, ErrUserIDConflict) {
		t.Fatalf("ID conflict was misclassified: %v", err)
	}
}

func TestPostgresSequenceMigrationIsIdempotentAndAlignsWithoutReset(t *testing.T) {
	mock, database := useUserMockDB(t)
	for i := 0; i < 2; i++ {
		mock.ExpectBegin()
		mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`(?s)DO \$\$.*CREATE SEQUENCE IF NOT EXISTS users_id_seq.*MAX\(id\).*sequence_last.*max_id > sequence_last.*setval.*ALTER TABLE users ALTER COLUMN id SET DEFAULT`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`(?s)to_regclass\('public.messages'\).*CREATE INDEX IF NOT EXISTS idx_messages_sync_target_time_id`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := MigratePostgresSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
