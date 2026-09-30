package handler

import (
	"errors"
	"testing"

	"Betterfly2/proto/storage"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestSyncRecoversRecallBeforeMessageCursorAndPagesIndependently(t *testing.T) {
	mock := useMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache()}
	query := &storage.QuerySyncMessages{ToUserId: 1, CursorTimestamp: "2026-09-30T10:00:00Z", CursorMessageId: 100, PageSize: 1, IncludeRecalledChanges: true}
	req := &storage.RequestMessage{TargetUserId: 1}
	columns := []string{"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "real_file_name", "is_group", "is_recalled", "recalled_at", "recalled_by"}
	for page := 0; page < 2; page++ {
		mock.ExpectQuery(`(?s)SELECT \*.*ORDER BY timestamp ASC, message_id ASC\s+LIMIT \$10`).
			WithArgs(int64(1), int64(1), query.CursorTimestamp, query.CursorTimestamp, int64(100), query.CursorTimestamp, query.CursorTimestamp, int64(100), int64(1), 2).
			WillReturnRows(sqlmock.NewRows(columns))
		cursor, cursorID := "1970-01-01T00:00:00Z", int64(0)
		if page == 1 {
			cursor, cursorID = "2026-09-30T09:01:00Z", 42
		}
		rows := sqlmock.NewRows(columns)
		if page == 0 {
			rows.AddRow(42, 1, 2, "secret", "2026-09-30T09:00:00Z", "file", "secret.pdf", false, true, "2026-09-30T09:01:00Z", 1)
		}
		rows.AddRow(43, 2, 99, "group secret", "2026-09-30T09:00:01Z", "text", "", true, true, "2026-09-30T09:01:00Z", 2)
		mock.ExpectQuery(`(?s)SELECT m\.\*.*m\.is_recalled = TRUE.*m\.recalled_at = \$2 AND m\.message_id > \$3.*m\.from_user_id = \$4 OR m\.to_user_id = \$5.*EXISTS.*gm\.user_id = \$7.*m\.timestamp >= COALESCE.*ORDER BY m\.recalled_at ASC, m\.message_id ASC LIMIT \$8`).
			WithArgs(cursor, cursor, cursorID, int64(1), int64(1), int64(1), int64(1), 2).WillReturnRows(rows)
		response, err := handler.handleQuerySyncMessagesWithDB(handler.requestDatabase(), req, query)
		if err != nil {
			t.Fatal(err)
		}
		sync := response.GetSyncMsgsRsp()
		if len(sync.GetMsgs()) != 0 || sync.GetNextCursorMessageId() != 100 || sync.GetNextCursorTimestamp() != query.CursorTimestamp || len(sync.GetRecalledMsgs()) != 1 {
			t.Fatalf("cursors coupled: %+v", sync)
		}
		msg := sync.GetRecalledMsgs()[0]
		if !msg.GetIsRecalled() || msg.GetContent() != "" || msg.GetRealFileName() != "" || msg.GetMessageId() != int64(42+page) || sync.GetRecallsHasMore() != (page == 0) {
			t.Fatalf("bad recall page: %+v", sync)
		}
		query.RecallCursorTimestamp, query.RecallCursorMessageId = sync.GetNextRecallCursorTimestamp(), sync.GetNextRecallCursorMessageId()
	}
}

func TestRecallSyncQueryFailureDoesNotReturnPartialSuccess(t *testing.T) {
	mock := useMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache()}
	injected := errors.New("recall query database unavailable")
	mock.ExpectQuery(`(?s)SELECT \*.*LIMIT \$10`).WillReturnRows(sqlmock.NewRows([]string{"message_id"}))
	mock.ExpectQuery(`SELECT m\.\*`).WillReturnError(injected)
	response, err := handler.handleQuerySyncMessagesWithDB(handler.requestDatabase(), &storage.RequestMessage{TargetUserId: 1}, &storage.QuerySyncMessages{ToUserId: 1, Timestamp: "2026-09-30T10:00:00Z", IncludeRecalledChanges: true})
	if !errors.Is(err, injected) || response != nil {
		t.Fatalf("partial success returned: %+v %v", response, err)
	}
}

func TestRecallSyncRejectsBadIdentityOrCursorBeforeDatabase(t *testing.T) {
	mock := useMockDB(t)
	handler := &StorageHandler{}
	for _, query := range []*storage.QuerySyncMessages{
		{ToUserId: 2, IncludeRecalledChanges: true},
		{ToUserId: 1, IncludeRecalledChanges: true, RecallCursorTimestamp: "not-a-time"},
	} {
		response, err := handler.handleQuerySyncMessagesWithDB(handler.requestDatabase(), &storage.RequestMessage{TargetUserId: 1}, query)
		if err != nil || response.GetResult() == storage.StorageResult_OK {
			t.Fatalf("bad query accepted: %+v %v", response, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
