package db

import (
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"regexp"
	"strings"
	"testing"
)

func TestDiscussionAuthorizationIsScopedAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		fail  bool
	}{{"current member before join", 1, false}, {"departed or private-channel nonmember", 0, false}, {"database failure", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			injected := errors.New("database offline")
			q := mock.ExpectQuery(`(?s)SELECT count\(\*\).*JOIN group_members gm.*source.discussion_root_message_id.*origin.is_public`).WithArgs(int64(2), int64(102), int64(2))
			if tc.fail {
				q.WillReturnError(injected)
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))
			}
			ok, err := CanReadDiscussionMessageWithDB(database, 2, &Message{MessageID: 102, IsGroup: true, ToUserID: 9, DiscussionRootMessageID: 101})
			if tc.fail {
				if ok || !errors.Is(err, injected) {
					t.Fatal(ok, err)
				}
			} else if err != nil || ok != (tc.count == 1) {
				t.Fatal(ok, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDiscussionInvalidInputsAndCanonicalRetry(t *testing.T) {
	database, mock := newInboxDatabase(t)
	for _, args := range [][3]int64{{0, 9, 10}, {1, 9, -1}, {1, 9, 9}} {
		if _, err := SetDiscussionGroupWithDB(database, args[0], args[1], args[2]); !errors.Is(err, ErrChannelInvalidArgument) {
			t.Fatal(err)
		}
	}
	if _, _, err := StoreNewMessageWithDB(database, 1, 2, "comment", "text", "", false, "id", "", 0, 101); !errors.Is(err, ErrInvalidReply) {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE from_user_id`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "content", "discussion_root_message_id", "timestamp"}).AddRow(102, "canonical", 101, "2026-10-02T00:00:00Z"))
	m, created, err := StoreNewMessageWithDB(database, 1, 9, "changed", "text", "", true, "id", "", 0, 999)
	if err != nil || created || m.MessageID != 102 || m.DiscussionRootMessageID != 101 || m.Content != "canonical" {
		t.Fatal(m, created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDiscussionMigrationV9IdempotentAndRetryable(t *testing.T) {
	database, mock := newInboxDatabase(t)
	plan := migrationPlan()
	pending, err := pendingMigrations(plan, []int{1, 2, 3, 4, 5, 6, 7, 8})
	if err != nil || len(pending) != 1 || pending[0].Version != 9 {
		t.Fatal(pending, err)
	}
	if pending, err := pendingMigrations(plan, []int{1, 2, 3, 4, 5, 6, 7, 8, 9}); err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	injected := errors.New("DDL interruption")
	mock.ExpectExec(regexp.QuoteMeta(`ALTER TABLE channel_settings ADD COLUMN IF NOT EXISTS discussion_group_id bigint`)).WillReturnError(injected)
	if err := migrateDiscussionSchema(database); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	for range 2 {
		for range 7 {
			mock.ExpectExec(`(ALTER TABLE.*ADD COLUMN IF NOT EXISTS|CREATE.*INDEX IF NOT EXISTS)`).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := migrateDiscussionSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(DiscussionReadPredicate, "joined_at") || !strings.Contains(DiscussionReadPredicate, "origin_group.is_delete = FALSE") {
		t.Fatal("thread history exception is not scoped to valid source channel")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
