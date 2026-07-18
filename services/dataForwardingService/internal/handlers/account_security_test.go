package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"data_forwarding_service/internal/connection"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestLegacyEmptyLogoutClosesOnlyCurrentConnection(t *testing.T) {
	var revokeRequest *auth.RevokeSessionsReq
	withAuthClient(t, authClientStub{revokeSessionsRequest: &revokeRequest})

	request := &pb.RequestMessage{}
	if err := proto.Unmarshal([]byte{0x22, 0x00}, request); err != nil {
		t.Fatalf("failed to decode legacy logout fixture: %v", err)
	}
	if request.GetLogout() == nil || request.GetLogout().GetScope() != pb.LogoutScope_CURRENT_CONNECTION {
		t.Fatalf("legacy logout did not default to CURRENT_CONNECTION: %+v", request)
	}
	result, err := RequestMessageHandler(42, request)
	if err != nil || result.code != 1 || result.response != nil {
		t.Fatalf("legacy logout must close current connection: result=%+v err=%v", result, err)
	}
	if revokeRequest != nil {
		t.Fatalf("legacy logout unexpectedly called RevokeSessions: %+v", revokeRequest)
	}
}

func TestLogoutAllSessionsRevokesBeforeClosing(t *testing.T) {
	var revokeRequest *auth.RevokeSessionsReq
	withAuthClient(t, authClientStub{
		revokeSessionsResponse: &auth.RevokeSessionsRsp{Result: auth.AuthResult_OK},
		revokeSessionsRequest:  &revokeRequest,
	})
	result, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_ALL_SESSIONS, "valid-jwt"))
	if err != nil || result.code != 1 || result.response != nil {
		t.Fatalf("successful all-session logout did not close current connection: result=%+v err=%v", result, err)
	}
	if revokeRequest == nil || revokeRequest.GetUserId() != 42 || revokeRequest.GetJwt() != "valid-jwt" {
		t.Fatalf("RevokeSessions did not receive authenticated connection identity: %+v", revokeRequest)
	}
}

func TestLogoutAllSessionsFailureKeepsConnectionOpen(t *testing.T) {
	tests := []struct {
		name   string
		jwt    string
		client authClientStub
	}{
		{name: "missing jwt", jwt: "", client: authClientStub{}},
		{name: "invalid jwt", jwt: "bad", client: authClientStub{revokeSessionsResponse: &auth.RevokeSessionsRsp{Result: auth.AuthResult_JWT_ERROR}}},
		{name: "old auth unimplemented", jwt: "valid", client: authClientStub{revokeSessionsError: status.Error(codes.Unimplemented, "unknown method")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withAuthClient(t, test.client)
			result, err := RequestMessageHandler(42, logoutRequest(pb.LogoutScope_ALL_SESSIONS, test.jwt))
			if err != nil || result.code != 0 {
				t.Fatalf("failed revocation must keep connection open: result=%+v err=%v", result, err)
			}
			if result.response.GetWarn().GetWarningMessage() == "" {
				t.Fatalf("failed revocation did not produce warning: %+v", result.response)
			}
		})
	}
}

func TestChangePasswordUsesConnectionIdentityAndMapsResponse(t *testing.T) {
	var authRequest *auth.ChangePasswordReq
	withAuthClient(t, authClientStub{
		changePasswordResponse: &auth.ChangePasswordRsp{Result: auth.AuthResult_OK, Jwt: "new-jwt"},
		changePasswordRequest:  &authRequest,
	})
	request := &pb.RequestMessage{
		Jwt: "old-jwt",
		Payload: &pb.RequestMessage_ChangePassword{ChangePassword: &pb.ChangePassword{
			OldPassword: "old-password", NewPassword: "new-password",
		}},
	}
	result, err := RequestMessageHandler(42, request)
	if err != nil || result.code != 0 {
		t.Fatalf("change password dispatch failed: result=%+v err=%v", result, err)
	}
	if authRequest == nil || authRequest.GetUserId() != 42 || authRequest.GetJwt() != "old-jwt" || authRequest.GetOldPassword() != "old-password" {
		t.Fatalf("Auth request did not use trusted connection identity: %+v", authRequest)
	}
	response := result.response.GetAccountSecurityRsp()
	if response.GetOperation() != "change_password" || response.GetResult() != pb.AccountSecurityResult_ACCOUNT_SECURITY_OK || response.GetJwt() != "new-jwt" {
		t.Fatalf("unexpected account security response: %+v", response)
	}
}

func TestChangePasswordUnimplementedMapsToServiceError(t *testing.T) {
	withAuthClient(t, authClientStub{changePasswordError: status.Error(codes.Unimplemented, "unknown method")})
	request := &pb.RequestMessage{Jwt: "jwt", Payload: &pb.RequestMessage_ChangePassword{ChangePassword: &pb.ChangePassword{OldPassword: "old", NewPassword: "new-password"}}}
	result, err := RequestMessageHandler(42, request)
	if err != nil {
		t.Fatalf("unimplemented Auth RPC must not panic or escape: %v", err)
	}
	if got := result.response.GetAccountSecurityRsp().GetResult(); got != pb.AccountSecurityResult_ACCOUNT_SECURITY_SERVICE_ERROR {
		t.Fatalf("unimplemented Auth RPC mapped to %s", got)
	}
}

func TestAccountSecurityResponseReturnsOnOriginatingConnection(t *testing.T) {
	withAuthClient(t, authClientStub{changePasswordResponse: &auth.ChangePasswordRsp{Result: auth.AuthResult_OK, Jwt: "new-jwt"}})
	conn := &connection.Connection{UserID: "42", SendChan: make(chan []byte, 1)}
	handler := &WebSocketHandler{}
	handler.handleAuthenticatedMessage(conn, &pb.RequestMessage{
		Jwt: "old-jwt",
		Payload: &pb.RequestMessage_ChangePassword{ChangePassword: &pb.ChangePassword{
			OldPassword: "old-password", NewPassword: "new-password",
		}},
	})

	select {
	case encoded := <-conn.SendChan:
		response := &pb.ResponseMessage{}
		if err := proto.Unmarshal(encoded, response); err != nil {
			t.Fatal(err)
		}
		if response.GetAccountSecurityRsp().GetJwt() != "new-jwt" {
			t.Fatalf("originating connection received unexpected response: %+v", response)
		}
	default:
		t.Fatal("originating connection did not receive account security response")
	}
}

func logoutRequest(scope pb.LogoutScope, jwt string) *pb.RequestMessage {
	return &pb.RequestMessage{Jwt: jwt, Payload: &pb.RequestMessage_Logout{Logout: &pb.LogoutReq{Scope: scope}}}
}
