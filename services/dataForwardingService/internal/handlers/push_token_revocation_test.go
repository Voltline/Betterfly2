package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	pushpb "Betterfly2/proto/push"
	auth "Betterfly2/proto/server_rpc/auth"
	sharedDB "Betterfly2/shared/db"
	"testing"
	"time"
)

func capturePushTokenRevocations(t *testing.T, enabled string) *[]*pushpb.RevokeUserTokens {
	t.Helper()
	t.Setenv(pushTokenRevocationEnv, enabled)
	var published []*pushpb.RevokeUserTokens
	original := publishPushTokenRevocation
	publishPushTokenRevocation = func(request *pushpb.RequestMessage) error {
		published = append(published, request.GetRevokeUserTokens())
		return nil
	}
	t.Cleanup(func() { publishPushTokenRevocation = original })
	return &published
}

func changePasswordRequest() *pb.RequestMessage {
	return &pb.RequestMessage{Jwt: "old-jwt", Payload: &pb.RequestMessage_ChangePassword{ChangePassword: &pb.ChangePassword{
		OldPassword: "old-password", NewPassword: "new-password",
	}}}
}

func TestPushTokenRevocationDisabledByDefault(t *testing.T) {
	published := capturePushTokenRevocations(t, "")
	withAuthClient(t, authClientStub{
		revokeSessionsResponse: &auth.RevokeSessionsRsp{Result: auth.AuthResult_OK},
		changePasswordResponse: &auth.ChangePasswordRsp{Result: auth.AuthResult_OK, Jwt: "new-jwt"},
	})
	if _, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_ALL_SESSIONS, "valid-jwt")); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestMessageHandler(42, changePasswordRequest()); err != nil {
		t.Fatal(err)
	}
	if len(*published) != 0 {
		t.Fatalf("revocation published while disabled: %v", *published)
	}
}

func TestPushTokenRevocationAfterSuccessfulCredentialChange(t *testing.T) {
	published := capturePushTokenRevocations(t, "true")
	withAuthClient(t, authClientStub{
		revokeSessionsResponse: &auth.RevokeSessionsRsp{Result: auth.AuthResult_OK},
		changePasswordResponse: &auth.ChangePasswordRsp{Result: auth.AuthResult_OK, Jwt: "new-jwt"},
	})
	before := time.Now().UTC()
	if _, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_ALL_SESSIONS, "valid-jwt")); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestMessageHandler(42, changePasswordRequest()); err != nil {
		t.Fatal(err)
	}
	if len(*published) != 2 || (*published)[0].GetReason() != "revoke_sessions" || (*published)[1].GetReason() != "change_password" {
		t.Fatalf("published = %v", *published)
	}
	for _, revocation := range *published {
		cutoff, err := time.Parse(sharedDB.ReliabilityTimeLayout, revocation.GetRevokedBefore())
		if revocation.GetUserId() != 42 || err != nil || cutoff.Before(before.Truncate(time.Microsecond)) || cutoff.After(time.Now()) {
			t.Fatalf("revocation = %+v err=%v", revocation, err)
		}
	}
}

func TestPushTokenRevocationSkippedWhenCredentialChangeFails(t *testing.T) {
	published := capturePushTokenRevocations(t, "true")
	withAuthClient(t, authClientStub{
		revokeSessionsResponse: &auth.RevokeSessionsRsp{Result: auth.AuthResult_JWT_ERROR},
		changePasswordResponse: &auth.ChangePasswordRsp{Result: auth.AuthResult_PASSWORD_ERROR},
	})
	if _, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_ALL_SESSIONS, "bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestMessageHandler(42, changePasswordRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_CURRENT_CONNECTION, "valid-jwt")); err != nil {
		t.Fatal(err)
	}
	if len(*published) != 0 {
		t.Fatalf("revocation published after failure: %v", *published)
	}
}
