package db

import (
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"strings"
	"testing"
	"time"
)

func TestChannelInputNormalizationAndBounds(t *testing.T) {
	for _, test := range []struct {
		in, out string
		invalid bool
	}{
		{" @Daily_News ", "daily_news", false}, {"", "", false}, {"abc", "", true}, {"with-hyphen", "", true}, {"123abc", "", true}, {strings.Repeat("a", 33), "", true},
	} {
		value, err := NormalizeChannelUsername(test.in)
		if (err != nil) != test.invalid || value != test.out {
			t.Fatalf("username %q -> %q %v", test.in, value, err)
		}
	}
	if !ValidChannelText(strings.Repeat("中", 100), 100) || ValidChannelText(strings.Repeat("中", 101), 100) || ValidChannelText("\xff", 100) {
		t.Fatal("Unicode bounds incorrect")
	}
	for _, test := range []struct {
		size    int32
		want    int
		invalid bool
	}{{0, 50, false}, {1, 1, false}, {10000, 100, false}, {-1, 0, true}} {
		size, err := ChannelPageSize(test.size)
		if size != test.want || (err != nil) != test.invalid {
			t.Fatalf("page=%d ->%d %v", test.size, size, err)
		}
	}
	database, mock := newInboxDatabase(t)
	if _, err := SubscribeChannelWithDB(database, 2, 0); !errors.Is(err, ErrChannelInvalidArgument) {
		t.Fatal(err)
	}
	if err := UnsubscribeChannelWithDB(database, 0, 2); !errors.Is(err, ErrChannelInvalidArgument) {
		t.Fatal(err)
	}
	if _, _, err := GetChannelHistoryWithDB(database, 2, 1, -1, 50); !errors.Is(err, ErrChannelInvalidArgument) {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelPublishGateChecksRoleAndDeletedGroup(t *testing.T) {
	database, mock := newInboxDatabase(t)
	for _, count := range []int64{1, 0} {
		mock.ExpectQuery(`(?s)LEFT JOIN channel_settings.*groups.is_delete = FALSE.*channel_settings.group_id IS NULL OR group_members.role IN`).WithArgs(int64(9), int64(2), GroupRoleOwner, GroupRoleAdmin).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
		allowed, err := CanPublishGroupMessageWithDB(database, 9, 2)
		if err != nil || allowed != (count > 0) {
			t.Fatalf("publish %v %v", allowed, err)
		}
	}
	injected := errors.New("database failed")
	mock.ExpectQuery(`SELECT count\(\*\)`).WillReturnError(injected)
	if allowed, err := CanPublishGroupMessageWithDB(database, 9, 2); allowed || !errors.Is(err, injected) {
		t.Fatalf("fail open %v %v", allowed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelManagerRecallOldPost(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member"} {
		t.Run(role, func(t *testing.T) {
			database, mock := newInboxDatabase(t)
			now := time.Now().UTC().Truncate(time.Second)
			expectRecallMessage(mock, 42, 1, 9, now.Add(-24*time.Hour).Format(time.RFC3339), true, false, "", 0)
			mock.ExpectQuery(`SELECT \* FROM "channel_settings"`).WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(9))
			mock.ExpectQuery(`(?s)SELECT groups.group_id.*JOIN groups`).WillReturnRows(sqlmock.NewRows([]string{"group_id", "my_role", "subscribed"}).AddRow(9, role, true))
			if role != "member" {
				mock.ExpectBegin()
				mock.ExpectExec(`UPDATE "messages" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				mock.ExpectBegin()
				mock.ExpectExec(`UPDATE "channel_settings" SET "pinned_message_id"`).WithArgs(0, int64(9), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			outcome, err := RecallMessageWithDB(database, 2, 42, now)
			if err != nil {
				t.Fatal(err)
			}
			want := MessageRecallOK
			if role == "member" {
				want = MessageRecallForbidden
			}
			if outcome.Status != want {
				t.Fatalf("%s recall: %v", role, outcome.Status)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
