package db

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestReplyRequiresSameConversationAndReadableTarget(t *testing.T) {
	for _, test := range []struct {
		name                   string
		from, to               int64
		group, missing, denied bool
		want                   error
	}{
		{name: "direct outgoing", from: 1, to: 2},
		{name: "direct incoming", from: 2, to: 1},
		{name: "other recipient", from: 1, to: 3, want: ErrInvalidReply},
		{name: "other conversation type", from: 1, to: 2, group: true, want: ErrInvalidReply},
		{name: "missing", missing: true, want: ErrInvalidReply},
		{name: "group readable", from: 3, to: 9, group: true},
		{name: "group before join or departed", from: 3, to: 9, group: true, denied: true, want: ErrInvalidReply},
		{name: "different group", from: 3, to: 10, group: true, want: ErrInvalidReply},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			rows := sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id", "is_group", "timestamp", "is_recalled"})
			if !test.missing {
				rows.AddRow(44, test.from, test.to, test.group, "2026-10-02T00:00:00Z", true)
			}
			mock.ExpectQuery(`SELECT \* FROM "messages"`).WithArgs(int64(44), 1).WillReturnRows(rows)
			isGroup := test.group && test.to >= 9
			targetID := int64(2)
			if isGroup {
				targetID = 9
			}
			if test.group && test.to == 9 {
				mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
				count := 1
				if test.denied {
					count = 0
				}
				mock.ExpectQuery(`(?s)SELECT count\(\*\).*COALESCE\(NULLIF\(joined_at`).WithArgs(int64(9), int64(1), "2026-10-02T00:00:00Z").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
			}
			err := validateReplyWithDB(database, 1, targetID, isGroup, 44)
			if !errors.Is(err, test.want) {
				t.Fatalf("reply authorization err=%v want=%v", err, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplyPersistenceAndCanonicalReplay(t *testing.T) {
	database, mock := newInboxDatabase(t)
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE from_user_id`).WillReturnRows(sqlmock.NewRows([]string{"message_id"}))
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE message_id`).WithArgs(int64(44), 1).WillReturnRows(sqlmock.NewRows([]string{"message_id", "from_user_id", "to_user_id"}).AddRow(44, 2, 1))
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "messages" .*"reply_to_message_id".*ON CONFLICT`).WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow(45))
	mock.ExpectCommit()
	message, created, err := StoreNewMessageWithDB(database, 1, 2, "reply", "text", "", false, "stable-reply", "", 44)
	if err != nil || !created || message.ReplyToMessageID != 44 || message.MessageID != 45 {
		t.Fatalf("persistence: %v %v", message, err)
	}
	// A changed retry reference is ignored, without reauthorizing or inserting it.
	mock.ExpectQuery(`SELECT \* FROM "messages" WHERE from_user_id`).WillReturnRows(sqlmock.NewRows([]string{"message_id", "content", "reply_to_message_id", "timestamp"}).AddRow(45, "reply", 44, message.Timestamp))
	replay, created, err := StoreNewMessageWithDB(database, 1, 2, "changed", "text", "", false, "stable-reply", "", 999)
	if err != nil || created || replay.MessageID != 45 || replay.ReplyToMessageID != 44 || replay.Content != "reply" || replay.Timestamp != message.Timestamp {
		t.Fatalf("noncanonical replay: %v %v", replay, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplyDatabaseFailureAndNegativeIDNeverInsert(t *testing.T) {
	database, mock := newInboxDatabase(t)
	if _, _, err := StoreNewMessageWithDB(database, 1, 2, "reply", "text", "", false, "", "", -1); !errors.Is(err, ErrInvalidReply) {
		t.Fatal(err)
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnError(injected)
	if _, _, err := StoreNewMessageWithDB(database, 1, 2, "reply", "text", "", false, "", "", 44); !errors.Is(err, injected) {
		t.Fatalf("transient error hidden: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelPinPermissionsScopeAndRecalledTarget(t *testing.T) {
	for _, test := range []struct {
		name, role        string
		messageID, target int64
		recalled, missing bool
		want              error
	}{
		{"owner pin", "owner", 44, 9, false, false, nil},
		{"admin pin", "admin", 44, 9, false, false, nil},
		{"member denied", "member", 44, 9, false, false, ErrChannelForbidden},
		{"nonmember denied", "", 44, 9, false, false, ErrChannelForbidden},
		{"unpin", "owner", 0, 9, false, false, nil},
		{"recalled", "owner", 44, 9, true, false, ErrChannelInvalidState},
		{"other channel", "owner", 44, 10, false, false, ErrChannelNotFound},
		{"missing", "owner", 44, 9, false, true, ErrChannelNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT \* FROM "groups".*FOR UPDATE`).WithArgs(int64(9), 1).WillReturnRows(sqlmock.NewRows([]string{"group_id", "owner_user_id"}).AddRow(9, 1))
			mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9, true))
			roles := sqlmock.NewRows([]string{"role"})
			if test.role != "" {
				roles.AddRow(test.role)
			}
			mock.ExpectQuery(`SELECT \* FROM "group_members"`).WillReturnRows(roles)
			if test.role == "owner" || test.role == "admin" {
				if test.messageID > 0 {
					messages := sqlmock.NewRows([]string{"message_id", "to_user_id", "is_group", "is_recalled"})
					if !test.missing {
						messages.AddRow(44, test.target, true, test.recalled)
					}
					mock.ExpectQuery(`(?s)SELECT \* FROM "messages".*FOR UPDATE`).WithArgs(test.messageID, 1).WillReturnRows(messages)
				}
			}
			if test.want == nil {
				mock.ExpectExec(`UPDATE "channel_settings" SET "pinned_message_id"`).WithArgs(test.messageID, int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE "groups" SET "update_time"`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`(?s)SELECT groups.group_id.*channel_settings.pinned_message_id`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "pinned_message_id"}).AddRow(9, test.messageID))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			view, err := SetChannelPinWithDB(database, 1, 9, test.messageID)
			if !errors.Is(err, test.want) || (test.want == nil && view.PinnedMessageID != test.messageID) {
				t.Fatalf("pin: %v %v", view, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGroupNotifyUpdatesOnlyCallerAndPreservesJoinedAt(t *testing.T) {
	for _, test := range []struct {
		name string
		rows int64
		fail bool
	}{{"mute", 1, false}, {"nonmember or deleted", 0, false}, {"database error", 0, true}} {
		t.Run(test.name, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			mock.ExpectBegin()
			update := mock.ExpectExec(`UPDATE "group_members" SET "notifications_muted"=\$1,"update_time"=\$2 WHERE .*user_id.*EXISTS`).WithArgs(true, sqlmock.AnyArg(), int64(9), int64(2))
			injected := errors.New("database error")
			if test.fail {
				update.WillReturnError(injected)
				mock.ExpectRollback()
			} else {
				update.WillReturnResult(sqlmock.NewResult(0, test.rows))
				mock.ExpectCommit()
			}
			_, err := UpdateGroupNotifyWithDB(database, 2, 9, false)
			if test.fail && !errors.Is(err, injected) || !test.fail && test.rows == 0 && !errors.Is(err, ErrRelationshipNotFound) || test.rows == 1 && err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChannelPinDatabaseFailureRollsBackWithoutSuccess(t *testing.T) {
	database, mock := newInboxDatabase(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT \* FROM "groups".*FOR UPDATE`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "owner_user_id"}).AddRow(9, 1))
	mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "is_public"}).AddRow(9, true))
	mock.ExpectQuery(`SELECT \* FROM "group_members"`).WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("owner"))
	mock.ExpectExec(`UPDATE "channel_settings"`).WillReturnResult(sqlmock.NewResult(0, 1))
	injected := errors.New("database interrupted after pin update")
	mock.ExpectExec(`UPDATE "groups"`).WillReturnError(injected)
	mock.ExpectRollback()
	view, err := SetChannelPinWithDB(database, 1, 9, 0)
	if view != nil || !errors.Is(err, injected) {
		t.Fatalf("partial pin reported success: %v %v", view, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConversationMigrationV8RepeatAndFailure(t *testing.T) {
	plan := migrationPlan()
	pending, err := pendingMigrations(plan, []int{1, 2, 3, 4, 5, 6, 7})
	if err != nil || len(pending) != 2 || pending[0].Version != 8 {
		t.Fatalf("v8 upgrade %v %v", pending, err)
	}
	if pending, err = pendingMigrations(plan, []int{1, 2, 3, 4, 5, 6, 7, 8}); err != nil || len(pending) != 1 || pending[0].Version != 9 {
		t.Fatalf("v8 repeat %v %v", pending, err)
	}
	database, mock := newInboxDatabase(t)
	statements := []string{`ALTER TABLE channel_settings ADD COLUMN IF NOT EXISTS pinned_message_id bigint NOT NULL DEFAULT 0`, `ALTER TABLE group_members ADD COLUMN IF NOT EXISTS notifications_muted boolean NOT NULL DEFAULT false`, `ALTER TABLE messages ADD COLUMN IF NOT EXISTS reply_to_message_id bigint NOT NULL DEFAULT 0`}
	injected := errors.New("interrupted DDL")
	mock.ExpectExec(regexp.QuoteMeta(statements[0])).WillReturnError(injected)
	if err := migrateConversationFeaturesSchema(database); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	for range 2 {
		for _, sql := range statements {
			mock.ExpectExec(regexp.QuoteMeta(sql)).WillReturnResult(sqlmock.NewResult(0, 0))
		}
		if err := migrateConversationFeaturesSchema(database); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
