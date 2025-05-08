package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/grpcClient"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/utils"
	"errors"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func HandleRequestData(data []byte) (*pb.RequestMessage, error) {
	req := &pb.RequestMessage{}
	err := proto.Unmarshal(data, req)
	if err != nil {
		// 反序列化失败了，说明数据不是有效的pb数据
		logger.Sugar().Errorf("反序列化失败: %v", err)
		return nil, err
	}

	return req, nil
}

func RequestMessageHandler(fromID int64, message *pb.RequestMessage) (int, error) {
	sugar := logger.Sugar()
	var err error
	res := 0
	switch payload := message.Payload.(type) {
	case *pb.RequestMessage_Post:
		sugar.Infof("收到 Post 消息: %+v", payload.Post)
		err = handlePostMessage(fromID, message)
	case *pb.RequestMessage_QueryUser:
		sugar.Infof("收到 QueryUser 消息: %+v", payload.QueryUser)
	case *pb.RequestMessage_InsertContact:
		sugar.Infof("收到 InsertContact 消息: %+v", payload.InsertContact)
	case *pb.RequestMessage_QueryGroup:
		sugar.Infof("收到 QueryGroup 消息: %+v", payload.QueryGroup)
	case *pb.RequestMessage_InsertGroup:
		sugar.Infof("收到 InsertGroup 消息: %+v", payload.InsertGroup)
	case *pb.RequestMessage_InsertGroupUser:
		sugar.Infof("收到 InsertGroupUser 消息: %+v", payload.InsertGroupUser)
	case *pb.RequestMessage_FileRequest:
		sugar.Infof("收到 FileRequest 消息: %+v", payload.FileRequest)
	case *pb.RequestMessage_UpdateAvatar:
		sugar.Infof("收到 UpdateAvatar 消息: %+v", payload.UpdateAvatar)
	case *pb.RequestMessage_Logout:
		res = 1
		sugar.Infof("收到登出报文: %+v", payload.Logout)
	case *pb.RequestMessage_Login, *pb.RequestMessage_Signup:
		sugar.Warnf("收到认证服务请求，不处理：%+v", payload)
	default:
		sugar.Warnf("收到不可处理Payload: %+v", payload)
	}
	return res, err
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
	rpcClient, err := grpcClient.GetAuthClient()
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
	authServiceRsp, err := rpcClient.Login(context.Background(), authLoginReq)
	logger.Sugar().Infof("authServiceRsp: %s", authServiceRsp.String())
	if err != nil {
		return errRsp, -1, err
	}
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
	rpcClient, err := grpcClient.GetAuthClient()
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
	authServiceRsp, err := rpcClient.Signup(context.Background(), authSignupReq)
	logger.Sugar().Infof("authServiceRsp: %s", authServiceRsp.String())
	if err != nil {
		return errRsp, err
	}
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
	default:
		signupRsp.Result = pb.SignupResult_SIGNUP_SVR_ERROR
	}
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Signup{
			Signup: signupRsp,
		},
	}, nil
}

func handlePostMessage(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法转发消息")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetPost()
	payload.FromId = fromID

	targetTopic := redisClient.GetContainerByConnection(strconv.FormatInt(payload.GetToId(), 10))
	if targetTopic == "" {
		// TODO: 消息保存
		logger.Sugar().Warnf("%s 用户不在线", strconv.FormatInt(payload.GetToId(), 10))
	}
	rspBytes, _ := proto.Marshal(message)
	err = publishMessage(rspBytes, targetTopic) // 将消息转发到消息队列
	if err != nil {
		logger.Sugar().Warnf("消息转发失败: %v", err)
		return err
	}

	return nil
}

func InplaceHandlePostMessage(message *pb.RequestMessage) error {
	payload := message.GetPost()
	logger.Sugar().Infof("InplaceHandlePostMessage-payload: %s", payload.String())
	rsp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Post{
			Post: payload,
		},
	}
	rspBytes, _ := proto.Marshal(rsp)
	err := SendMessage(strconv.FormatInt(payload.GetToId(), 10), rspBytes)
	if err != nil {
		return err
	}

	logger.Sugar().Infof("%d 成功向 %d 发送消息", payload.GetFromId(), payload.GetToId())
	return nil
}
