package call

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGroupAuthorizerCurrentMembershipExcludesChannelsAndFailsClosed(t *testing.T) {
	connection, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := NewGormGroupAuthorizer(database)
	query := `(?s)SELECT group_members.role, groups.name.*JOIN group_members.*LEFT JOIN channel_settings.*groups.is_delete = false AND channel_settings.group_id IS NULL`
	for _, role := range []string{"owner", "admin", "member", "invalid"} {
		mock.ExpectQuery(query).WithArgs(int64(2), int64(10)).WillReturnRows(sqlmock.NewRows([]string{"role", "name"}).AddRow(role, "群聊"))
		access, err := authorizer.Access(context.Background(), 10, 2)
		if role == "invalid" {
			if !errors.Is(err, ErrForbidden) {
				t.Fatal("invalid role accepted")
			}
		} else if err != nil || access.Role != role {
			t.Fatal("valid member rejected", err)
		}
	}
	mock.ExpectQuery(query).WithArgs(int64(9), int64(10)).WillReturnRows(sqlmock.NewRows([]string{"role", "name"}))
	if _, err := authorizer.Access(context.Background(), 10, 9); !errors.Is(err, ErrForbidden) {
		t.Fatal("nonmember accepted")
	}
	injected := errors.New("database unavailable")
	mock.ExpectQuery(query).WillReturnError(injected)
	if _, err := authorizer.Access(context.Background(), 10, 2); !errors.Is(err, injected) {
		t.Fatal("database failure became permission success/domain rejection")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupAuthorizerListsCurrentOrdinaryGroupMembers(t *testing.T) {
	connection, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := NewGormGroupAuthorizer(database)
	query := `(?s)SELECT "group_members"\."user_id" FROM "groups".*JOIN group_members.*LEFT JOIN channel_settings.*groups.is_delete = false AND channel_settings.group_id IS NULL.*group_members.role IN.*ORDER BY group_members.user_id`
	mock.ExpectQuery(query).WithArgs(int64(10), "owner", "admin", "member").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(2).AddRow(3))
	members, err := authorizer.Members(context.Background(), 10)
	if err != nil || len(members) != 3 || members[0] != 1 || members[2] != 3 {
		t.Fatal("current member list not returned", members, err)
	}
	mock.ExpectQuery(query).WithArgs(int64(20), "owner", "admin", "member").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}))
	if members, err := authorizer.Members(context.Background(), 20); err != nil || len(members) != 0 {
		t.Fatal("empty member list not preserved", members, err)
	}
	fail := errors.New("database unavailable")
	mock.ExpectQuery(query).WithArgs(int64(10), "owner", "admin", "member").WillReturnError(fail)
	if _, err := authorizer.Members(context.Background(), 10); !errors.Is(err, fail) {
		t.Fatal("database failure was interpreted as an empty member list", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
