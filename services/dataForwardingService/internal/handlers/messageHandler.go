package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/grpcClient"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

var getAuthClient = grpcClient.GetAuthClient

type dfRequestContext struct {
	fromID  int64
	message *pb.RequestMessage
}

type dfRequestResult struct {
	code int
}

type dfRequestModule func(*dispatch.OneofRouter[dfRequestContext, dfRequestResult])

var (
	dfRequestModules    []dfRequestModule
	dfRequestRouter     *dispatch.OneofRouter[dfRequestContext, dfRequestResult]
	dfRequestRouterOnce sync.Once
)

func registerDFRequestModule(register dfRequestModule) {
	dfRequestModules = append(dfRequestModules, register)
}

func getDFRequestRouter() *dispatch.OneofRouter[dfRequestContext, dfRequestResult] {
	dfRequestRouterOnce.Do(func() {
		dfRequestRouter = newDFRequestRouter()
	})
	return dfRequestRouter
}

func newDFRequestRouter() *dispatch.OneofRouter[dfRequestContext, dfRequestResult] {
	router := dispatch.NewOneofRouter[dfRequestContext, dfRequestResult]()
	for _, register := range dfRequestModules {
		register(router)
	}
	return router
}

func HandleRequestData(data []byte) (*pb.RequestMessage, error) {
	req := &pb.RequestMessage{}
	err := proto.Unmarshal(data, req)
	if err != nil {
		// 反序列化失败了，说明数据不是有效的pb数据
		logger.Sugar().Warnf("反序列化失败: %v", err)
		return nil, err
	}

	return req, nil
}

func RequestMessageHandler(fromID int64, message *pb.RequestMessage) (int, error) {
	result, err := getDFRequestRouter().Dispatch(dfRequestContext{
		fromID:  fromID,
		message: message,
	}, message.Payload)
	if err != nil {
		if errors.Is(err, dispatch.ErrNilPayload) || errors.Is(err, dispatch.ErrUnregisteredPayload) {
			logger.Sugar().Warnf("收到不可处理Payload: %+v", message.Payload)
			return 0, nil
		}
		return 0, err
	}
	return result.code, nil
}

func HandleLoginMessage(message *pb.RequestMessage) (*pb.ResponseMessage, int64, error) {
	jwt := message.GetJwt()
	errRsp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Login{
			Login: &pb.LoginRsp{
				Result: pb.LoginResult_LOGIN_SVR_ERROR,
			},
		},
	}
	rpcClient, err := getAuthClient()
	if err != nil {
		return errRsp, -1, err
	}
	clientLoginReq := message.GetLogin()
	authLoginReq := &auth.LoginReq{}
	if jwt == "" {
		// 没有jwt代表账户密码登录
		authLoginReq.Account = clientLoginReq.GetAccount()
		authLoginReq.Password = clientLoginReq.GetPassword()
	} else {
		// 有jwt，不需要密码
		authLoginReq.Account = clientLoginReq.GetAccount()
		authLoginReq.Jwt = jwt
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authServiceRsp, err := rpcClient.Login(ctx, authLoginReq)
	if err != nil {
		return errRsp, -1, err
	}
	if authServiceRsp == nil {
		return errRsp, -1, errors.New("auth service returned an empty login response")
	}
	logger.Sugar().Debugf(
		"authService登录响应: result=%s user_id=%d account=%q",
		authServiceRsp.GetResult(), authServiceRsp.GetUserId(), authServiceRsp.GetAccount(),
	)
	loginRsp := &pb.LoginRsp{}
	var userID int64 = -1
	switch authServiceRsp.Result {
	case auth.AuthResult_OK:
		loginRsp.Result = pb.LoginResult_LOGIN_OK
		loginRsp.Jwt = authServiceRsp.GetJwt()
		loginRsp.UserId = authServiceRsp.GetUserId()
		userID = authServiceRsp.GetUserId()
	case auth.AuthResult_ACCOUNT_NOT_EXIST:
		loginRsp.Result = pb.LoginResult_ACCOUNT_NOT_EXIST
	case auth.AuthResult_PASSWORD_ERROR:
		loginRsp.Result = pb.LoginResult_PASSWORD_ERROR
	case auth.AuthResult_JWT_ERROR:
		loginRsp.Result = pb.LoginResult_JWT_ERROR
	default:
		loginRsp.Result = pb.LoginResult_LOGIN_SVR_ERROR
	}
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Login{
			Login: loginRsp,
		},
	}, userID, nil
}

func HandleSignupMessage(message *pb.RequestMessage) (*pb.ResponseMessage, error) {
	rpcClient, err := getAuthClient()
	errRsp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Signup{
			Signup: &pb.SignupRsp{
				Result: pb.SignupResult_SIGNUP_SVR_ERROR,
			},
		},
	}
	if err != nil {
		return errRsp, err
	}
	clientSignupReq := message.GetSignup()
	authSignupReq := &auth.SignupReq{
		Account:  clientSignupReq.GetAccount(),
		Password: clientSignupReq.GetPassword(),
		UserName: clientSignupReq.GetUserName(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authServiceRsp, err := rpcClient.Signup(ctx, authSignupReq)
	if err != nil {
		return errRsp, err
	}
	if authServiceRsp == nil {
		return errRsp, errors.New("auth service returned an empty signup response")
	}
	logger.Sugar().Debugf("authServiceRsp: %s", authServiceRsp.String())
	signupRsp := &pb.SignupRsp{}
	switch authServiceRsp.Result {
	case auth.AuthResult_OK:
		signupRsp.Result = pb.SignupResult_SIGNUP_OK
	case auth.AuthResult_ACCOUNT_EXIST:
		signupRsp.Result = pb.SignupResult_ACCOUNT_EXIST
	case auth.AuthResult_ACCOUNT_EMPTY:
		signupRsp.Result = pb.SignupResult_ACCOUNT_EMPTY
	case auth.AuthResult_PASSWORD_EMPTY:
		signupRsp.Result = pb.SignupResult_PASSWORD_EMPTY
	case auth.AuthResult_ACCOUNT_TOO_LONG:
		signupRsp.Result = pb.SignupResult_ACCOUNT_TOO_LONG
	default:
		signupRsp.Result = pb.SignupResult_SIGNUP_SVR_ERROR
	}
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Signup{
			Signup: signupRsp,
		},
	}, nil
}
