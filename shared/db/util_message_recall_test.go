package db

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var recallMessageColumns = []string{
	"message_id", "client_message_id", "from_user_id", "to_user_id", "content",
	"timestamp", "message_type", "real_file_name", "is_group", "is_recalled",
	"recalled_at", "recalled_by",
}

func expectRecallMessage(mock sqlmock.Sqlmock, messageID, fromUserID, toUserID int64, sentAt string, isGroup, isRecalled bool, recalledAt string, recalledBy int64) {
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id = \$1 ORDER BY "messages"\."message_id" LIMIT \$2 FOR UPDATE`).
		WithArgs(messageID, int64(1)).
		WillReturnRows(sqlmock.NewRows(recallMessageColumns).AddRow(
			messageID, nil, fromUserID, toUserID, "secret", sentAt, "text", "", isGroup,
			isRecalled, recalledAt, recalledBy,
		))
}

func TestRecallMessageOwnRecentMessageUpdatesTombstone(t *testing.T) {
	database, mock := newInboxDatabase(t)
	now := time.Date(2026, 7, 21, 4, 0, 30, 0, time.UTC)
	expectRecallMessage(mock, 41, 1001, 1002, now.Add(-30*time.Second).Format(time.RFC3339), false, false, "", 0)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "messages" SET "is_recalled"=\$1,"recalled_at"=\$2,"recalled_by"=\$3 WHERE message_id = \$4 AND is_recalled = \$5`).
		WithArgs(true, now.Format(time.RFC3339), int64(1001), int64(41), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	outcome, err := RecallMessageWithDB(database, 1001, 41, now)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != MessageRecallOK || !outcome.Message.IsRecalled || outcome.Message.RecalledBy != 1001 || outcome.Message.RecalledAt != now.Format(time.RFC3339) {
		t.Fatalf("unexpected recall outcome: %+v", outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecallMessageRejectsExpiredAlreadyRecalledAndOtherUsers(t *testing.T) {
	now := time.Date(2026, 7, 21, 4, 5, 0, 0, time.UTC)
	tests := []struct {
		name       string
		operator   int64
		fromUserID int64
		toUserID   int64
		sentAt     string
		isRecalled bool
		recalledAt string
		recalledBy int64
		wantStatus MessageRecallStatus
	}{
		{name: "expired", operator: 1001, fromUserID: 1001, toUserID: 1002, sentAt: now.Add(-MessageRecallWindow - time.Second).Format(time.RFC3339), wantStatus: MessageRecallExpired},
		{name: "already recalled", operator: 1001, fromUserID: 1001, toUserID: 1002, sentAt: now.Add(-time.Second).Format(time.RFC3339), isRecalled: true, recalledAt: now.Add(-time.Second).Format(time.RFC3339), recalledBy: 1001, wantStatus: MessageRecallAlreadyRecalled},
		{name: "recipient cannot recall", operator: 1002, fromUserID: 1001, toUserID: 1002, sentAt: now.Add(-time.Second).Format(time.RFC3339), wantStatus: MessageRecallForbidden},
		{name: "unrelated user sees not found", operator: 1003, fromUserID: 1001, toUserID: 1002, sentAt: now.Add(-time.Second).Format(time.RFC3339), wantStatus: MessageRecallNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			expectRecallMessage(mock, 42, test.fromUserID, test.toUserID, test.sentAt, false, test.isRecalled, test.recalledAt, test.recalledBy)
			outcome, err := RecallMessageWithDB(database, test.operator, 42, now)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != test.wantStatus {
				t.Fatalf("status=%v want=%v", outcome.Status, test.wantStatus)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecallMessageDatabaseFailureRemainsRetryable(t *testing.T) {
	database, mock := newInboxDatabase(t)
	injected := errors.New("database temporarily unavailable")
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnError(injected)

	outcome, err := RecallMessageWithDB(database, 1001, 43, time.Now())
	if outcome != nil || !errors.Is(err, injected) {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
