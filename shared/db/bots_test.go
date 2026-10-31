package db

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestIsBotUserID(t *testing.T) {
	for id, want := range map[int64]bool{
		1: false, 900000000000000001: false, BotUserIDMin: true, BotUserIDMin + 1: true, BotUserIDMax: true, BotUserIDMax + 1: false,
	} {
		if IsBotUserID(id) != want {
			t.Fatalf("IsBotUserID(%d) = %v", id, !want)
		}
	}
}

func TestBotPlatformMigrationIsExplicitAdditiveAndRetryable(t *testing.T) {
	plan := migrationPlan()
	if last := plan[len(plan)-1]; last.Version != 13 || last.Name != "bot platform" || CurrentSchemaVersion != 13 {
		t.Fatalf("unexpected plan tail %+v", last)
	}
	database, mock := newInboxDatabase(t)
	failure := errors.New("DDL interrupted")
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS bots`).WillReturnError(failure)
	if err := migrateBotPlatformSchema(database); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for range 2 {
		for _, statement := range []string{"CREATE TABLE IF NOT EXISTS bots", "CREATE TABLE IF NOT EXISTS bot_updates",
			"CREATE INDEX IF NOT EXISTS idx_bot_updates_pending", "CREATE INDEX IF NOT EXISTS idx_bot_updates_created"} {
			mock.ExpectExec(statement).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := migrateBotPlatformSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
