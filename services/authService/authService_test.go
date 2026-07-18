package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"authService/internal/utils"
	"context"
	"database/sql/driver"
	"errors"
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

func TestPasswordLoginDoesNotIssueJWTWhenSigningKeyPersistenceFails(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	mock := useAuthMockDB(t)
	expectUserByAccount(mock, passwordHash, nil)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET "jwt_key"=\$1 WHERE id = \$2`).
		WithArgs(sqlmock.AnyArg(), int64(9)).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	resp, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Password: "correct-password"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetResult() != pb.AuthResult_SERVICE_ERROR || resp.GetJwt() != "" {
		t.Fatalf("persistence failure issued a JWT: %+v", resp)
	}
}

func TestChangePasswordRotatesCredentialsAndReturnsUsableJWT(t *testing.T) {
	oldPassword := "old-password"
	newPassword := "new-password"
	oldHash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	oldJWT, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: oldKey})
	if err != nil {
		t.Fatal(err)
	}

	mock := useAuthMockDB(t)
	expectUserByID(mock, oldHash, oldKey)
	newKey := &bytesCapture{}
	newHash := &stringCapture{}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET "jwt_key"=\$1,"password_hash"=\$2 WHERE id = \$3 AND password_hash = \$4 AND jwt_key = \$5`).
		WithArgs(newKey, newHash, int64(9), string(oldHash), oldKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{
		UserId: 9, Jwt: oldJWT, OldPassword: oldPassword, NewPassword: newPassword,
	})
	if err != nil || response.GetResult() != pb.AuthResult_OK || response.GetJwt() == "" {
		t.Fatalf("change password failed: response=%+v err=%v", response, err)
	}
	claims, err := utils.ValidateJWT(response.GetJwt(), newKey.value)
	if err != nil || claims.ID != 9 || claims.Account != "alice" {
		t.Fatalf("new JWT is invalid: claims=%+v err=%v", claims, err)
	}
	if _, err := utils.ValidateJWT(oldJWT, newKey.value); err == nil {
		t.Fatal("old JWT remained valid after password change")
	}
	if bcrypt.CompareHashAndPassword([]byte(newHash.value), []byte(newPassword)) != nil {
		t.Fatal("new password was not persisted as the replacement hash")
	}
	if bcrypt.CompareHashAndPassword([]byte(newHash.value), []byte(oldPassword)) == nil {
		t.Fatal("old password still matches replacement hash")
	}
	expectUserByID(mock, []byte(newHash.value), newKey.value)
	checkNew, err := (&AuthService{}).CheckJwt(context.Background(), &pb.CheckJwtReq{UserId: 9, Jwt: response.GetJwt()})
	if err != nil || checkNew.GetResult() != pb.AuthResult_OK {
		t.Fatalf("new JWT was rejected by Auth: response=%+v err=%v", checkNew, err)
	}
	expectUserByID(mock, []byte(newHash.value), newKey.value)
	checkOld, err := (&AuthService{}).CheckJwt(context.Background(), &pb.CheckJwtReq{UserId: 9, Jwt: oldJWT})
	if err != nil || checkOld.GetResult() != pb.AuthResult_JWT_ERROR {
		t.Fatalf("old JWT was not rejected by Auth: response=%+v err=%v", checkOld, err)
	}

	expectUserByAccount(mock, []byte(newHash.value), newKey.value)
	oldLogin, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Password: oldPassword})
	if err != nil || oldLogin.GetResult() != pb.AuthResult_PASSWORD_ERROR {
		t.Fatalf("old password login was not rejected: response=%+v err=%v", oldLogin, err)
	}
	expectUserByAccount(mock, []byte(newHash.value), newKey.value)
	newLogin, err := (&AuthService{}).Login(context.Background(), &pb.LoginReq{Account: "alice", Password: newPassword})
	if err != nil || newLogin.GetResult() != pb.AuthResult_OK || newLogin.GetJwt() == "" {
		t.Fatalf("new password login failed: response=%+v err=%v", newLogin, err)
	}
}

func TestChangePasswordFailuresDoNotUpdateCredentials(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	validJWT, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: key})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("password too short", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByID(mock, passwordHash, key)
		response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{UserId: 9, Jwt: validJWT, OldPassword: "old-password", NewPassword: "short"})
		if err != nil || response.GetResult() != pb.AuthResult_PASSWORD_TOO_SHORT {
			t.Fatalf("unexpected short password result: %+v err=%v", response, err)
		}
	})
	t.Run("password too long", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByID(mock, passwordHash, key)
		response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{UserId: 9, Jwt: validJWT, OldPassword: "old-password", NewPassword: strings.Repeat("x", 73)})
		if err != nil || response.GetResult() != pb.AuthResult_PASSWORD_TOO_LONG {
			t.Fatalf("unexpected long password result: %+v err=%v", response, err)
		}
	})
	t.Run("invalid jwt", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByID(mock, passwordHash, key)
		response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{UserId: 9, Jwt: "invalid", OldPassword: "old-password", NewPassword: "new-password"})
		if err != nil || response.GetResult() != pb.AuthResult_JWT_ERROR {
			t.Fatalf("unexpected invalid JWT result: %+v err=%v", response, err)
		}
	})
	t.Run("wrong old password", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByID(mock, passwordHash, key)
		response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{UserId: 9, Jwt: validJWT, OldPassword: "wrong-password", NewPassword: "new-password"})
		if err != nil || response.GetResult() != pb.AuthResult_PASSWORD_ERROR {
			t.Fatalf("unexpected old password result: %+v err=%v", response, err)
		}
	})
	t.Run("database failure", func(t *testing.T) {
		mock := useAuthMockDB(t)
		expectUserByID(mock, passwordHash, key)
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "users" SET`).WillReturnError(errors.New("database unavailable"))
		mock.ExpectRollback()
		response, err := (&AuthService{}).ChangePassword(context.Background(), &pb.ChangePasswordReq{UserId: 9, Jwt: validJWT, OldPassword: "old-password", NewPassword: "new-password"})
		if err != nil || response.GetResult() != pb.AuthResult_SERVICE_ERROR || response.GetJwt() != "" {
			t.Fatalf("database failure reported success: %+v err=%v", response, err)
		}
	})
}

func TestRevokeSessionsRotatesJWTKey(t *testing.T) {
	passwordHash := []byte("password-hash")
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	oldJWT, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: oldKey})
	if err != nil {
		t.Fatal(err)
	}
	mock := useAuthMockDB(t)
	expectUserByID(mock, passwordHash, oldKey)
	newKey := &bytesCapture{}
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET "jwt_key"=\$1 WHERE id = \$2 AND jwt_key = \$3`).
		WithArgs(newKey, int64(9), oldKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	response, err := (&AuthService{}).RevokeSessions(context.Background(), &pb.RevokeSessionsReq{UserId: 9, Jwt: oldJWT})
	if err != nil || response.GetResult() != pb.AuthResult_OK {
		t.Fatalf("revoke sessions failed: response=%+v err=%v", response, err)
	}
	if _, err := utils.ValidateJWT(oldJWT, newKey.value); err == nil {
		t.Fatal("current JWT remained valid after RevokeSessions")
	}
}

func TestRevokeSessionsDatabaseFailureDoesNotReportSuccess(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	jwt, err := utils.GenerateJWT(&db.User{ID: 9, Account: "alice", JwtKey: key})
	if err != nil {
		t.Fatal(err)
	}
	mock := useAuthMockDB(t)
	expectUserByID(mock, []byte("password-hash"), key)
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "users" SET "jwt_key"=\$1 WHERE id = \$2 AND jwt_key = \$3`).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	response, err := (&AuthService{}).RevokeSessions(context.Background(), &pb.RevokeSessionsReq{UserId: 9, Jwt: jwt})
	if err != nil || response.GetResult() != pb.AuthResult_SERVICE_ERROR {
		t.Fatalf("database failure reported revocation success: response=%+v err=%v", response, err)
	}
}

func expectUserByAccount(mock sqlmock.Sqlmock, passwordHash []byte, key []byte) {
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE account = \$1`).
		WithArgs("alice", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account", "password_hash", "jwt_key"}).
			AddRow(int64(9), "alice", string(passwordHash), key))
}

func expectUserByID(mock sqlmock.Sqlmock, passwordHash []byte, key []byte) {
	mock.ExpectQuery(`SELECT \* FROM "users" WHERE "users"\."id" = \$1 ORDER BY "users"\."id" LIMIT \$2`).
		WithArgs(int64(9), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account", "password_hash", "jwt_key"}).
			AddRow(int64(9), "alice", string(passwordHash), key))
}

type bytesCapture struct{ value []byte }

func (capture *bytesCapture) Match(value driver.Value) bool {
	bytes, ok := value.([]byte)
	if !ok {
		return false
	}
	capture.value = append(capture.value[:0], bytes...)
	return len(capture.value) == 32
}

type stringCapture struct{ value string }

func (capture *stringCapture) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	capture.value = text
	return strings.HasPrefix(text, "$2")
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
