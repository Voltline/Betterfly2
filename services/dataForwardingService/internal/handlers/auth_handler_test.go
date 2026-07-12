package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	context "context"
	"errors"
	"testing"

	"google.golang.org/grpc"
)

type authClientStub struct {
	loginResponse  *auth.LoginRsp
	loginError     error
	signupResponse *auth.SignupRsp
	signupError    error
}

func (s authClientStub) Login(context.Context, *auth.LoginReq, ...grpc.CallOption) (*auth.LoginRsp, error) {
	return s.loginResponse, s.loginError
}

func (s authClientStub) Signup(context.Context, *auth.SignupReq, ...grpc.CallOption) (*auth.SignupRsp, error) {
	return s.signupResponse, s.signupError
}

func (s authClientStub) CheckJwt(context.Context, *auth.CheckJwtReq, ...grpc.CallOption) (*auth.CheckJwtRsp, error) {
	return nil, errors.New("not implemented")
}

func TestAuthHandlersPreserveRPCFailuresWithoutPanicking(t *testing.T) {
	rpcErr := errors.New("auth unavailable")
	withAuthClient(t, authClientStub{loginError: rpcErr, signupError: rpcErr})

	loginResponse, userID, err := HandleLoginMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Login{Login: &pb.LoginReq{Account: "alice"}}})
	if !errors.Is(err, rpcErr) || userID != -1 || loginResponse.GetLogin().GetResult() != pb.LoginResult_LOGIN_SVR_ERROR {
		t.Fatalf("unexpected login failure mapping: response=%+v user_id=%d err=%v", loginResponse, userID, err)
	}

	signupResponse, err := HandleSignupMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Signup{Signup: &pb.SignupReq{Account: "alice"}}})
	if !errors.Is(err, rpcErr) || signupResponse.GetSignup().GetResult() != pb.SignupResult_SIGNUP_SVR_ERROR {
		t.Fatalf("unexpected signup failure mapping: response=%+v err=%v", signupResponse, err)
	}
}

func TestAuthHandlersRejectEmptySuccessfulRPCResponses(t *testing.T) {
	withAuthClient(t, authClientStub{})

	if _, _, err := HandleLoginMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Login{Login: &pb.LoginReq{Account: "alice"}}}); err == nil {
		t.Fatal("empty login response must be rejected")
	}
	if _, err := HandleSignupMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Signup{Signup: &pb.SignupReq{Account: "alice"}}}); err == nil {
		t.Fatal("empty signup response must be rejected")
	}
}

func TestLoginHandlerMapsSuccessfulAuthResponse(t *testing.T) {
	withAuthClient(t, authClientStub{loginResponse: &auth.LoginRsp{Result: auth.AuthResult_OK, UserId: 42, Jwt: "renewed"}})

	response, userID, err := HandleLoginMessage(&pb.RequestMessage{Payload: &pb.RequestMessage_Login{Login: &pb.LoginReq{Account: "alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	if userID != 42 || response.GetLogin().GetResult() != pb.LoginResult_LOGIN_OK || response.GetLogin().GetJwt() != "renewed" {
		t.Fatalf("unexpected successful login mapping: response=%+v user_id=%d", response, userID)
	}
}

func withAuthClient(t *testing.T, client auth.AuthServiceClient) {
	t.Helper()
	original := getAuthClient
	getAuthClient = func() (auth.AuthServiceClient, error) { return client, nil }
	t.Cleanup(func() { getAuthClient = original })
}
