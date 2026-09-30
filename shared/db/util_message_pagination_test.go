package db

import (
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestBuildSyncMessagesPageBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages []Message
		wantLen  int
		hasMore  bool
		cursorID int64
	}{
		{name: "empty"},
		{name: "less than page", messages: []Message{{MessageID: 1, Timestamp: "t1"}}, wantLen: 1, cursorID: 1},
		{name: "exact page", messages: []Message{{MessageID: 1, Timestamp: "t1"}, {MessageID: 2, Timestamp: "t2"}}, wantLen: 2, cursorID: 2},
		{name: "limit plus one", messages: []Message{{MessageID: 1, Timestamp: "t1"}, {MessageID: 2, Timestamp: "t2"}, {MessageID: 3, Timestamp: "t3"}}, wantLen: 2, hasMore: true, cursorID: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := buildSyncMessagesPage(test.messages, 2)
			if len(page.Messages) != test.wantLen || page.HasMore != test.hasMore || page.NextCursorMessageID != test.cursorID {
				t.Fatalf("unexpected page: %+v", page)
			}
		})
	}
}

func TestSyncIncludesSentDirectMessagesWithoutDuplicatingSelfMessage(t *testing.T) {
	database, mock := newInboxDatabase(t)
	timestamp := "2026-09-30T10:00:00Z"
	mock.ExpectQuery(`(?s)m\.is_group = FALSE.*\(m\.to_user_id = \$1 OR m\.from_user_id = \$2\).*joined_at.*LIMIT \$10`).
		WithArgs(int64(1), int64(1), timestamp, timestamp, int64(0), timestamp, timestamp, int64(0), int64(1), 3).
		WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id", "timestamp"}).AddRow(42, 1, 2, timestamp).AddRow(43, 1, 1, timestamp))
	page, err := GetSyncMessagesPageWithDB(database, 1, timestamp, 0, 2)
	if err != nil || len(page.Messages) != 2 || page.HasMore || page.Messages[0].FromUserID != 1 || page.NextCursorMessageID != 43 {
		t.Fatalf("outgoing sync: %+v %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecallSyncEmptyPreservesCursorAndBoundsPage(t *testing.T) {
	database, mock := newInboxDatabase(t)
	timestamp := "2026-09-30T10:00:00Z"
	mock.ExpectQuery(`(?s)m\.is_recalled = TRUE.*recalled_at.*LIMIT \$8`).
		WithArgs(timestamp, timestamp, int64(42), int64(1), int64(1), int64(1), int64(1), MaxSyncPageSize+1).
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}))
	page, err := GetRecalledMessagesPageWithDB(database, 1, timestamp, 42, 10000)
	if err != nil || page.NextCursorTimestamp != timestamp || page.NextCursorMessageID != 42 || page.HasMore {
		t.Fatalf("empty recall cursor: %+v %v", page, err)
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT m\.\*`).WillReturnError(injected)
	if page, err := GetRecalledMessagesPageWithDB(database, 1, timestamp, 42, 1); !errors.Is(err, injected) || page != nil {
		t.Fatalf("recall failure hidden: %+v %v", page, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
