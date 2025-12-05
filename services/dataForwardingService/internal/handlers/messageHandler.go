package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	auth "Betterfly2/proto/server_rpc/auth"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/grpcClient"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/utils"
	"errors"
	"fmt"
	"os"
	"strconv"

	"google.golang.org/protobuf/proto"
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
		err = handleQueryUser(fromID, message)
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
	case *pb.RequestMessage_UpdateUserName:
		sugar.Debugf("收到 UpdateUserName 消息")
		err = handleUpdateUserName(fromID, message)
	case *pb.RequestMessage_UpdateUserAvatar:
		sugar.Debugf("收到 UpdateUserAvatar 消息")
		err = handleUpdateUserAvatar(fromID, message)
	case *pb.RequestMessage_QueryMessage:
		sugar.Debugf("收到 QueryMessage 消息: message_id=%d", payload.QueryMessage.GetMessageId())
		err = handleQueryMessage(fromID, message)
	case *pb.RequestMessage_QuerySyncMessages:
		sugar.Debugf("收到 QuerySyncMessages 消息: to_user_id=%d", payload.QuerySyncMessages.GetToUserId())
		err = handleQuerySyncMessages(fromID, message)
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

// sendMessageToStorage 发送消息到storageService进行存储
func sendMessageToStorage(payload *pb.Post, currentContainerID string) error {
	// 构建storage请求消息
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   payload.GetToId(),
		Payload: &storage.RequestMessage_StoreNewMessage{
			StoreNewMessage: &storage.StoreNewMessage{
				FromUserId:  payload.GetFromId(),
				ToUserId:    payload.GetToId(),
				Content:     payload.GetMsg(),
				MessageType: payload.GetMsgType(),
				IsGroup:     payload.GetIsGroup(),
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布消息到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息已保存到storageService: from=%d to=%d", payload.GetFromId(), payload.GetToId())
	return nil
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
	if payload == nil {
		return errors.New("post消息为空")
	}
	payload.FromId = fromID

	targetUserID := strconv.FormatInt(payload.GetToId(), 10)
	targetTopic := redisClient.GetContainerByConnection(targetUserID)

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 无论用户是否在线，都将消息保存到storageService
	storageErr := sendMessageToStorage(payload, currentContainerID)
	if storageErr != nil {
		// 记录错误但继续处理（消息存储失败不影响转发）
		logger.Sugar().Errorf("消息保存到storageService失败: %v", storageErr)
		// 不返回错误，继续尝试转发消息
	}

	if targetTopic == "" {
		// 用户不在线，只保存消息（已保存），不进行转发
		logger.Sugar().Debugf("%s 用户不在线，消息已保存", targetUserID)
		return nil
	}

	// 获取WebSocket处理器和路由器
	wsHandler := GetWebSocketHandler()
	if wsHandler == nil || wsHandler.router == nil {
		logger.Sugar().Errorf("WebSocket处理器或路由器未初始化")
		return fmt.Errorf("WebSocket处理器或路由器未初始化")
	}

	// 构建要发送的消息
	var messageBytes []byte

	if targetTopic == currentContainerID {
		// 同容器内消息，发送ResponseMessage格式
		rsp := &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Post{
				Post: payload,
			},
		}
		messageBytes, err = proto.Marshal(rsp)
		if err != nil {
			logger.Sugar().Errorf("序列化响应消息失败: %v", err)
			return err
		}
		logger.Sugar().Debugf("构建同容器ResponseMessage，长度: %d", len(messageBytes))
	} else {
		// 跨容器消息，发送RequestMessage格式
		messageBytes, err = proto.Marshal(message)
		if err != nil {
			logger.Sugar().Errorf("序列化RequestMessage失败: %v", err)
			return err
		}
		logger.Sugar().Debugf("构建跨容器RequestMessage，长度: %d", len(messageBytes))
	}

	// 使用路由器统一发送消息
	err = wsHandler.router.RouteMessage(targetUserID, messageBytes)
	if err != nil {
		logger.Sugar().Errorf("路由器发送消息失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息路由成功: %d -> %s (容器: %s)", payload.GetFromId(), targetUserID, targetTopic)
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

	// 使用路由器发送消息
	wsHandler := GetWebSocketHandler()
	if wsHandler == nil || wsHandler.router == nil {
		logger.Sugar().Errorf("WebSocket处理器或路由器未初始化")
		return fmt.Errorf("WebSocket处理器或路由器未初始化")
	}

	targetUserID := strconv.FormatInt(payload.GetToId(), 10)
	err = wsHandler.router.RouteMessage(targetUserID, rspBytes)
	if err != nil {
		logger.Sugar().Errorf("路由器发送消息失败: %v", err)
		return err
	}
	logger.Sugar().Debugf("%d 成功向 %d 发送消息", payload.GetFromId(), payload.GetToId())

	return nil
}

// handleQueryMessage 处理查询单条消息请求
func handleQueryMessage(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询消息")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetQueryMessage()
	if payload == nil {
		return errors.New("query_message消息为空")
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 构建storage查询请求
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID, // 查询结果返回给请求者
		Payload: &storage.RequestMessage_QueryMessage{
			QueryMessage: &storage.QueryMessage{
				MessageId: payload.GetMessageId(),
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage查询请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布查询请求到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息查询请求已发送到storageService: message_id=%d", payload.GetMessageId())
	return nil
}

// handleQuerySyncMessages 处理同步消息请求
func handleQuerySyncMessages(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询同步消息")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetQuerySyncMessages()
	if payload == nil {
		return errors.New("query_sync_messages消息为空")
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 构建storage同步查询请求
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   payload.GetToUserId(), // 注意：这里使用payload中的to_user_id，而不是fromID
		Payload: &storage.RequestMessage_QuerySyncMessages{
			QuerySyncMessages: &storage.QuerySyncMessages{
				ToUserId:  payload.GetToUserId(),
				Timestamp: payload.GetTimestamp(),
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage同步查询请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布同步查询请求到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("同步消息查询请求已发送到storageService: to_user_id=%d", payload.GetToUserId())
	return nil
}

// handleQueryUser 处理查询用户信息请求
func handleQueryUser(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询用户信息")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetQueryUser()
	if payload == nil {
		return errors.New("query_user消息为空")
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 构建storage查询请求
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID, // 查询结果返回给请求者
		Payload: &storage.RequestMessage_QueryUser{
			QueryUser: &storage.QueryUser{
				UserId: payload.GetToQueryUserId(), // 要查询的用户ID
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage用户查询请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布用户查询请求到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户信息查询请求已发送到storageService: to_query_user_id=%d", payload.GetToQueryUserId())
	return nil
}

// handleUpdateUserName 处理更新用户名请求
func handleUpdateUserName(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法更新用户名")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetUpdateUserName()
	if payload == nil {
		return errors.New("update_user_name消息为空")
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 构建storage更新请求
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID, // 结果返回给请求者
		Payload: &storage.RequestMessage_UpdateUserName{
			UpdateUserName: &storage.UpdateUserName{
				UserId:      payload.GetUserId(),
				NewUserName: payload.GetNewUserName(),
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage用户名更新请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布用户名更新请求到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户名更新请求已发送到storageService: user_id=%d, new_name=%s", payload.GetUserId(), payload.GetNewUserName())
	return nil
}

// handleUpdateUserAvatar 处理更新用户头像请求
func handleUpdateUserAvatar(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法更新用户头像")
	}

	err := utils.ValidateAndParseJWT(fromID, jwt)
	if err != nil {
		return err
	}
	payload := message.GetUpdateUserAvatar()
	if payload == nil {
		return errors.New("update_user_avatar消息为空")
	}

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 构建storage更新请求
	storeReq := &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID, // 结果返回给请求者
		Payload: &storage.RequestMessage_UpdateUserAvatar{
			UpdateUserAvatar: &storage.UpdateUserAvatar{
				UserId:       payload.GetUserId(),
				NewAvatarUrl: payload.GetNewAvatarUrl(),
			},
		},
	}

	// 序列化存储请求
	storeReqBytes, err := proto.Marshal(storeReq)
	if err != nil {
		logger.Sugar().Errorf("序列化storage用户头像更新请求失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_REQUEST,
		Payload: storeReqBytes,
	}
	envBytes, err := proto.Marshal(env)
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发布到storage-requests主题
	err = publisher.PublishMessage(string(envBytes), "storage-requests")
	if err != nil {
		logger.Sugar().Errorf("发布用户头像更新请求到storage-requests失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户头像更新请求已发送到storageService: user_id=%d, new_avatar=%s", payload.GetUserId(), payload.GetNewAvatarUrl())
	return nil
}
