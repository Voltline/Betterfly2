package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/grpcClient"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/utils"
	"errors"
	"google.golang.org/protobuf/proto"
	"os"
	"strconv"
)

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
	sugar := logger.Sugar()
	var err error
	res := 0
	switch payload := message.Payload.(type) {
	case *pb.RequestMessage_Post:
		sugar.Debugf("收到 Post 消息: from=%d to=%d", payload.Post.GetFromId(), payload.Post.GetToId())
		err = handlePostMessage(fromID, message)
	case *pb.RequestMessage_QueryUser:
		sugar.Debugf("收到 QueryUser 消息")
	case *pb.RequestMessage_InsertContact:
		sugar.Debugf("收到 InsertContact 消息")
	case *pb.RequestMessage_QueryGroup:
		sugar.Debugf("收到 QueryGroup 消息")
	case *pb.RequestMessage_InsertGroup:
		sugar.Debugf("收到 InsertGroup 消息")
	case *pb.RequestMessage_InsertGroupUser:
		sugar.Debugf("收到 InsertGroupUser 消息")
	case *pb.RequestMessage_FileRequest:
		sugar.Debugf("收到 FileRequest 消息")
	case *pb.RequestMessage_UpdateAvatar:
		sugar.Debugf("收到 UpdateAvatar 消息")
	case *pb.RequestMessage_Logout:
		res = 1
		sugar.Infof("用户登出")
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
	logger.Sugar().Debugf("authServiceRsp: %s", authServiceRsp.String())
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
	logger.Sugar().Debugf("authServiceRsp: %s", authServiceRsp.String())
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

	targetUserID := strconv.FormatInt(payload.GetToId(), 10)
	targetTopic := redisClient.GetContainerByConnection(targetUserID)
	if targetTopic == "" {
		// TODO: 消息保存
		logger.Sugar().Debugf("%s 用户不在线", targetUserID)
		return nil
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 检查是否在同一容器内
	if targetTopic == currentContainerID {
		// 同容器内消息，直接发送
		rsp := &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Post{
				Post: payload,
			},
		}
		rspBytes, err := proto.Marshal(rsp)
		if err != nil {
			logger.Sugar().Errorf("序列化响应消息失败: %v", err)
			return err
		}

		// 使用全局WebSocket处理器的连接管理器直接发送消息
		wsHandler := GetWebSocketHandler()
		if wsHandler != nil {
			err = wsHandler.connManager.SendMessageToUser(targetUserID, rspBytes)
			if err != nil {
				logger.Sugar().Warnf("发送消息失败: %v", err)
				return err
			}
			logger.Sugar().Debugf("同容器内消息发送成功: %d -> %d", payload.GetFromId(), payload.GetToId())
		} else {
			logger.Sugar().Errorf("WebSocket处理器未初始化")
		}
	} else {
		// 跨容器消息，通过Kafka转发
		rspBytes, _ := proto.Marshal(message)
		err = publisher.PublishMessage(string(rspBytes), targetTopic) // 将消息转发到消息队列
		if err != nil {
			logger.Sugar().Warnf("消息转发失败: %v", err)
			return err
		}
		logger.Sugar().Debugf("跨容器消息转发成功: %d -> %d (目标容器: %s)", payload.GetFromId(), payload.GetToId(), targetTopic)
	}

	return nil
}

func InplaceHandlePostMessage(message *pb.RequestMessage) error {
	payload := message.GetPost()
	logger.Sugar().Debugf("InplaceHandlePostMessage-payload: %s", payload.String())

	// 构建响应消息
	rsp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Post{
			Post: payload,
		},
	}

	// 序列化响应消息
	rspBytes, err := proto.Marshal(rsp)
	if err != nil {
		logger.Sugar().Errorf("序列化响应消息失败: %v", err)
		return err
	}

	// 使用全局WebSocket处理器的连接管理器直接发送消息
	wsHandler := GetWebSocketHandler()
	if wsHandler != nil {
		err = wsHandler.connManager.SendMessageToUser(strconv.FormatInt(payload.GetToId(), 10), rspBytes)
		if err != nil {
			logger.Sugar().Warnf("发送消息失败: %v", err)
			return err
		}
		logger.Sugar().Debugf("%d 成功向 %d 发送消息", payload.GetFromId(), payload.GetToId())
	} else {
		logger.Sugar().Errorf("WebSocket处理器未初始化")
	}

	return nil
}
