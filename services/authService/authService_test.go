package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"context"
	"strings"
	"testing"
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
