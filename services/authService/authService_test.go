package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"authService/internal/utils"
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSignupRejectsInvalidCredentialsBeforeDatabaseAccess(t *testing.T) {
	service := &AuthService{}
	tests := []struct {
		name string
		req  *pb.SignupReq
		want pb.AuthResult
	}{
		{name: "empty account", req: &pb.SignupReq{Password: "password"}, want: pb.AuthResult_ACCOUNT_EMPTY},
		{name: "account too long", req: &pb.SignupReq{Account: strings.Repeat("a", db.MaxNameLen+1), Password: "password"}, want: pb.AuthResult_ACCOUNT_TOO_LONG},
		{name: "empty password", req: &pb.SignupReq{Account: "alice"}, want: pb.AuthResult_PASSWORD_EMPTY},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := service.Signup(context.Background(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetResult() != tt.want || resp.GetUserId() != 0 || resp.GetAccount() != tt.req.GetAccount() {
				t.Fatalf("unexpected signup response: %+v", resp)
			}
		})
	}
}

func TestUserBriefString(t *testing.T) {
	if got := userBriefStr(&db.User{ID: 42, Account: "alice"}); got != "user42[alice]" {
		t.Fatalf("unexpected user brief: %q", got)
	}
}

func TestLoginRejectsInvalidJWTWithoutIssuingReplacement(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	wrongKey := []byte("abcdef0123456789abcdef0123456789")
	token, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: wrongKey})
	if err != nil {
		t.Fatal(err)
	}
	mock := useAuthMockDB(t)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE account = \$1`).
		WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account", "password_hash", "jwt_key"}).
			AddRow(int64(9), "alice", "hash", key))

	resp, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Jwt: token})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetResult() != pb.AuthResult_JWT_ERROR || resp.GetJwt() != "" {
		t.Fatalf("invalid JWT must not receive a replacement token: %+v", resp)
	}
}

func TestLoginRejectsJWTWhenUserSigningKeyIsMissing(t *testing.T) {
	token, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: nil})
	if err != nil {
		t.Fatal(err)
	}
	mock := useAuthMockDB(t)
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE account = \$1`).
		WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account", "password_hash", "jwt_key"}).
			AddRow(int64(9), "alice", "hash", nil))

	resp, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Jwt: token})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetResult() != pb.AuthResult_JWT_ERROR || resp.GetJwt() != "" {
		t.Fatalf("user without signing key must reject JWT login: %+v", resp)
	}
}

func TestPasswordLoginRejectsWrongPasswordAndIssuesJWTOnSuccess(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")

	t.Run("wrong password", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByAccount(mock, passwordHash, key)
		resp, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Password: "wrong-password"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetResult() != pb.AuthResult_PASSWORD_ERROR || resp.GetJwt() != "" {
			t.Fatalf("unexpected wrong-password response: %+v", resp)
		}
	})

	t.Run("success", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByAccount(mock, passwordHash, key)
		resp, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Password: "correct-password"})
		if err != nil {
			t.Fatal(err)
		}
		claims, validateErr := utils.ValidateJWT(resp.GetJwt(), key)
		if resp.GetResult() != pb.AuthResult_OK || validateErr != nil || claims.ID != 9 || claims.Account != "alice" {
			t.Fatalf("successful login did not issue a valid JWT: response=%+v claims=%+v err=%v", resp, claims, validateErr)
		}
	})
}

func expectUserByAccount(mock sqlmock.Sqlmock, passwordHash []byte, key []byte) {
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE account = \$1`).
		WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account", "password_hash", "jwt_key"}).
			AddRow(int64(9), "alice", string(passwordHash), key))
}

func useAuthMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, DriverName: "postgres"}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	original := db.DB
	db.DB = func(...interface{}) *gorm.DB { return gormDB }
	t.Cleanup(func() {
		db.DB = original
		_ = sqlDB.Close()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet database expectation: %v", err)
		}
	})
	return mock
}
