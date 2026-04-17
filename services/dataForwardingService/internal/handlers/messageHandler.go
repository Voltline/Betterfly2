package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	auth "Betterfly2/proto/server_rpc/auth"
	storage "Betterfly2/proto/storage"
	sharedDB "Betterfly2/shared/db"
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
	"strings"

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
		err = handleInsertContact(fromID, message)
	case *pb.RequestMessage_QueryContacts:
		sugar.Debugf("收到 QueryContacts 消息")
		err = handleQueryContacts(fromID, message)
	case *pb.RequestMessage_DeleteContact:
		sugar.Debugf("收到 DeleteContact 消息")
		err = handleDeleteContact(fromID, message)
	case *pb.RequestMessage_UpdateContactAlias:
		sugar.Debugf("收到 UpdateContactAlias 消息")
		err = handleUpdateContactAlias(fromID, message)
	case *pb.RequestMessage_UpdateContactNotify:
		sugar.Debugf("收到 UpdateContactNotify 消息")
		err = handleUpdateContactNotify(fromID, message)
	case *pb.RequestMessage_QueryGroup:
		sugar.Debugf("收到 QueryGroup 消息")
		err = handleQueryGroup(fromID, message)
	case *pb.RequestMessage_InsertGroup:
		sugar.Debugf("收到 InsertGroup 消息")
		err = handleInsertGroup(fromID, message)
	case *pb.RequestMessage_InsertGroupUser:
		sugar.Debugf("收到 InsertGroupUser 消息")
		err = handleInsertGroupUser(fromID, message)
	case *pb.RequestMessage_QueryGroupMembers:
		sugar.Debugf("收到 QueryGroupMembers 消息")
		err = handleQueryGroupMembers(fromID, message)
	case *pb.RequestMessage_QueryJoinedGroups:
		sugar.Debugf("收到 QueryJoinedGroups 消息")
		err = handleQueryJoinedGroups(fromID, message)
	case *pb.RequestMessage_DeleteGroupUser:
		sugar.Debugf("收到 DeleteGroupUser 消息")
		err = handleDeleteGroupUser(fromID, message)
	case *pb.RequestMessage_UpdateAvatar:
		sugar.Debugf("收到 UpdateAvatar 消息")
		err = handleUpdateAvatar(fromID, message)
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
				FromUserId:   payload.GetFromId(),
				ToUserId:     payload.GetToId(),
				Content:      payload.GetMsg(),
				MessageType:  payload.GetMsgType(),
				IsGroup:      payload.GetIsGroup(),
				RealFileName: payload.GetRealFileName(),
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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布消息到storage-service失败: %v", err)
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
	if err := validatePostPayload(payload); err != nil {
		return err
	}

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

	if payload.GetIsGroup() {
		return routeGroupMessage(fromID, payload, message, currentContainerID)
	}

	if targetTopic == "" {
		// 用户不在线，只保存消息（已保存），不进行转发
		logger.Sugar().Debugf("%s 用户不在线，消息已保存", targetUserID)
		return nil
	}

	return routePostToTarget(targetUserID, targetTopic, currentContainerID, payload, message)
}

func InplaceHandlePostMessage(message *pb.RequestMessage) error {
	payload := message.GetPost()
	logger.Sugar().Debugf("InplaceHandlePostMessage-payload: %s", payload.String())
	if err := validatePostPayload(payload); err != nil {
		return err
	}

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

func validatePostPayload(payload *pb.Post) error {
	if payload == nil {
		return errors.New("post消息为空")
	}

	msgType := strings.TrimSpace(payload.GetMsgType())
	msg := strings.TrimSpace(payload.GetMsg())
	realFileName := strings.TrimSpace(payload.GetRealFileName())

	if msgType == "file" {
		if msg == "" {
			return errors.New("文件消息缺少file_hash")
		}
		if realFileName == "" {
			return errors.New("文件消息缺少real_file_name")
		}
		return nil
	}

	if realFileName != "" {
		payload.RealFileName = ""
	}

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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布查询请求到storage-service失败: %v", err)
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

	storeReq := buildSyncMessagesStorageRequest(fromID, payload, currentContainerID)

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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布同步查询请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf(
		"同步消息查询请求已发送到storageService: requester_user_id=%d, query_target_id=%d",
		fromID,
		payload.GetToUserId(),
	)
	return nil
}

func buildSyncMessagesStorageRequest(fromID int64, payload *pb.QuerySyncMessages, currentContainerID string) *storage.RequestMessage {
	return &storage.RequestMessage{
		FromKafkaTopic: currentContainerID,
		// storage 响应需要回到发起同步请求的登录用户，而不是查询目标实体。
		TargetUserId: fromID,
		Payload: &storage.RequestMessage_QuerySyncMessages{
			QuerySyncMessages: &storage.QuerySyncMessages{
				ToUserId:  payload.GetToUserId(),
				Timestamp: payload.GetTimestamp(),
			},
		},
	}
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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布用户查询请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户信息查询请求已发送到storageService: to_query_user_id=%d", payload.GetToQueryUserId())
	return nil
}

func handleInsertContact(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法添加好友")
	}

	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetInsertContact()
	if payload == nil {
		return errors.New("insert_contact消息为空")
	}
	if payload.GetToInsertUserId() <= 0 {
		return errors.New("to_insert_user_id非法")
	}
	if payload.GetToInsertUserId() == fromID {
		return errors.New("不能添加自己为好友")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildInsertContactFriendRequest(fromID, payload.GetToInsertUserId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("添加好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToInsertUserId())
	return nil
}

func handleQueryContacts(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询好友列表")
	}

	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetQueryContacts()
	if payload == nil {
		return errors.New("query_contacts消息为空")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	friendReq := buildQueryContactsFriendRequest(fromID, currentContainerID)

	if err := publishFriendRequest(friendReq); err != nil {
		logger.Sugar().Errorf("发布QueryContacts请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryContactsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_QueryFriendList{
			QueryFriendList: &friend.QueryFriendList{
				UserId: fromID,
			},
		},
	}
}

func handleQueryGroup(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询群信息")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetQueryGroup()
	if payload == nil {
		return errors.New("query_group消息为空")
	}
	if payload.GetToQueryGroupId() <= 0 {
		return errors.New("to_query_group_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildQueryGroupFriendRequest(fromID, payload.GetToQueryGroupId(), payload.GetClientNeedSave(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群信息查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetToQueryGroupId())
	return nil
}

func buildQueryGroupFriendRequest(fromID, groupID int64, clientNeedSave bool, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_QueryGroup{
			QueryGroup: &friend.QueryGroup{
				RequestUserId:  fromID,
				GroupId:        groupID,
				ClientNeedSave: clientNeedSave,
			},
		},
	}
}

func handleInsertGroup(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法创建群组")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetInsertGroup()
	if payload == nil {
		return errors.New("insert_group消息为空")
	}
	if payload.GetToBeCreatedGroupId() <= 0 || strings.TrimSpace(payload.GetToBeCreatedGroupName()) == "" {
		return errors.New("群组信息非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildInsertGroupFriendRequest(fromID, payload.GetToBeCreatedGroupId(), payload.GetToBeCreatedGroupName(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("创建群组请求已发送到friendService: owner_id=%d, group_id=%d", fromID, payload.GetToBeCreatedGroupId())
	return nil
}

func buildInsertGroupFriendRequest(fromID, groupID int64, groupName, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_CreateGroup{
			CreateGroup: &friend.CreateGroup{
				OwnerUserId: fromID,
				GroupId:     groupID,
				GroupName:   groupName,
			},
		},
	}
}

func handleInsertGroupUser(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法加入群组")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetInsertGroupUser()
	if payload == nil {
		return errors.New("insert_group_user消息为空")
	}
	if payload.GetTargetGroupId() <= 0 {
		return errors.New("target_group_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildInsertGroupUserFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("加入群组请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildInsertGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_AddGroupMember{
			AddGroupMember: &friend.AddGroupMember{
				UserId:  fromID,
				GroupId: groupID,
			},
		},
	}
}

func handleQueryGroupMembers(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询群成员")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetQueryGroupMembers()
	if payload == nil {
		return errors.New("query_group_members消息为空")
	}
	if payload.GetTargetGroupId() <= 0 {
		return errors.New("target_group_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildQueryGroupMembersFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroupMembers请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群成员列表查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func handleQueryJoinedGroups(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法查询已加入群列表")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetQueryJoinedGroups()
	if payload == nil {
		return errors.New("query_joined_groups消息为空")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildQueryJoinedGroupsFriendRequest(fromID, currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryJoinedGroups请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("已加入群列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryJoinedGroupsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_QueryJoinedGroups{
			QueryJoinedGroups: &friend.QueryJoinedGroups{
				UserId: fromID,
			},
		},
	}
}

func buildQueryGroupMembersFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_QueryGroupMembers{
			QueryGroupMembers: &friend.QueryGroupMembers{
				RequestUserId: fromID,
				GroupId:       groupID,
			},
		},
	}
}

func handleDeleteGroupUser(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法退出群组")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetDeleteGroupUser()
	if payload == nil {
		return errors.New("delete_group_user消息为空")
	}
	if payload.GetTargetGroupId() <= 0 {
		return errors.New("target_group_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildDeleteGroupUserFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("退群请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildDeleteGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_RemoveGroupMember{
			RemoveGroupMember: &friend.RemoveGroupMember{
				RequestUserId: fromID,
				GroupId:       groupID,
				UserId:        fromID,
			},
		},
	}
}

func handleUpdateAvatar(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法更新头像")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetUpdateAvatar()
	if payload == nil {
		return errors.New("update_avatar消息为空")
	}
	if !payload.GetIsGroup() {
		return errors.New("当前仅支持通过UpdateAvatar更新群头像")
	}
	if payload.GetTargetId() <= 0 || strings.TrimSpace(payload.GetAvatarHash()) == "" {
		return errors.New("群头像更新请求非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildUpdateGroupAvatarFriendRequest(fromID, payload.GetTargetId(), payload.GetAvatarHash(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateAvatar请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群头像更新请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetId())
	return nil
}

func buildUpdateGroupAvatarFriendRequest(fromID, groupID int64, avatarHash, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_UpdateGroupAvatar{
			UpdateGroupAvatar: &friend.UpdateGroupAvatar{
				RequestUserId: fromID,
				GroupId:       groupID,
				AvatarHash:    avatarHash,
			},
		},
	}
}

func buildInsertContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_AddDirectFriend{
			AddDirectFriend: &friend.AddDirectFriend{
				UserId:   fromID,
				FriendId: targetUserID,
			},
		},
	}
}

func handleDeleteContact(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法删除好友")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetDeleteContact()
	if payload == nil {
		return errors.New("delete_contact消息为空")
	}
	if payload.GetToDeleteUserId() <= 0 || payload.GetToDeleteUserId() == fromID {
		return errors.New("to_delete_user_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildDeleteContactFriendRequest(fromID, payload.GetToDeleteUserId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("删除好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToDeleteUserId())
	return nil
}

func buildDeleteContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_RemoveDirectFriend{
			RemoveDirectFriend: &friend.RemoveDirectFriend{
				UserId:   fromID,
				FriendId: targetUserID,
			},
		},
	}
}

func handleUpdateContactAlias(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法更新好友备注")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetUpdateContactAlias()
	if payload == nil {
		return errors.New("update_contact_alias消息为空")
	}
	if payload.GetTargetUserId() <= 0 {
		return errors.New("target_user_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildUpdateContactAliasFriendRequest(fromID, payload.GetTargetUserId(), payload.GetNewAlias(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactAlias请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友备注更新请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetTargetUserId())
	return nil
}

func buildUpdateContactAliasFriendRequest(fromID, targetUserID int64, alias, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_UpdateFriendAlias{
			UpdateFriendAlias: &friend.UpdateFriendAlias{
				UserId:   fromID,
				FriendId: targetUserID,
				Alias:    alias,
			},
		},
	}
}

func handleUpdateContactNotify(fromID int64, message *pb.RequestMessage) error {
	jwt := message.GetJwt()
	if jwt == "" {
		return errors.New("用户未携带有效JWT，无法更新好友通知设置")
	}
	if err := utils.ValidateAndParseJWT(fromID, jwt); err != nil {
		return err
	}

	payload := message.GetUpdateContactNotify()
	if payload == nil {
		return errors.New("update_contact_notify消息为空")
	}
	if payload.GetTargetUserId() <= 0 {
		return errors.New("target_user_id非法")
	}

	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	if err := publishFriendRequest(buildUpdateContactNotifyFriendRequest(fromID, payload.GetTargetUserId(), payload.GetIsNotify(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactNotify请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友通知设置更新请求已发送到friendService: user_id=%d, friend_id=%d, is_notify=%v", fromID, payload.GetTargetUserId(), payload.GetIsNotify())
	return nil
}

func buildUpdateContactNotifyFriendRequest(fromID, targetUserID int64, isNotify bool, currentContainerID string) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: currentContainerID,
		TargetUserId:   fromID,
		Payload: &friend.RequestMessage_UpdateFriendNotify{
			UpdateFriendNotify: &friend.UpdateFriendNotify{
				UserId:   fromID,
				FriendId: targetUserID,
				IsNotify: isNotify,
			},
		},
	}
}

func publishFriendRequest(friendReq *friend.RequestMessage) error {
	friendReqBytes, err := proto.Marshal(friendReq)
	if err != nil {
		logger.Sugar().Errorf("序列化friend请求失败: %v", err)
		return err
	}

	envBytes, err := proto.Marshal(&envelope.Envelope{
		Type:    envelope.MessageType_FRIEND_REQUEST,
		Payload: friendReqBytes,
	})
	if err != nil {
		logger.Sugar().Errorf("序列化Envelope失败: %v", err)
		return err
	}

	return publisher.PublishMessage(string(envBytes), "friend-service")
}

func routeGroupMessage(fromID int64, payload *pb.Post, message *pb.RequestMessage, currentContainerID string) error {
	isMember, err := sharedDB.IsActiveGroupMember(payload.GetToId(), fromID)
	if err != nil {
		return err
	}
	if !isMember {
		return errors.New("当前用户不在该群中，无法发送群消息")
	}

	memberIDs, err := sharedDB.GetActiveGroupMemberIDs(payload.GetToId())
	if err != nil {
		return err
	}

	delivered := 0
	for _, memberID := range memberIDs {
		if memberID == fromID {
			continue
		}

		targetUserID := strconv.FormatInt(memberID, 10)
		targetTopic := redisClient.GetContainerByConnection(targetUserID)
		if targetTopic == "" {
			continue
		}

		if targetTopic == currentContainerID {
			if err := routePostToTarget(targetUserID, targetTopic, currentContainerID, payload, message); err != nil {
				logger.Sugar().Errorf("群消息本地转发失败: group_id=%d, target_user=%s, err=%v", payload.GetToId(), targetUserID, err)
				continue
			}
			delivered++
			continue
		}

		if err := routeGroupPostCrossContainer(targetTopic, memberID, payload); err != nil {
			logger.Sugar().Errorf("群消息转发失败: group_id=%d, target_user=%s, err=%v", payload.GetToId(), targetUserID, err)
			continue
		}
		delivered++
	}

	logger.Sugar().Debugf("群消息处理完成: group_id=%d, delivered=%d", payload.GetToId(), delivered)
	return nil
}

func routeGroupPostCrossContainer(targetContainerID string, targetUserID int64, payload *pb.Post) error {
	delivery := &pb.GroupPostDelivery{
		TargetUserId: targetUserID,
		Post:         payload,
	}
	deliveryBytes, err := proto.Marshal(delivery)
	if err != nil {
		logger.Sugar().Errorf("序列化GroupPostDelivery失败: %v", err)
		return err
	}

	envBytes, err := proto.Marshal(&envelope.Envelope{
		Type:    envelope.MessageType_DF_RESPONSE,
		Payload: deliveryBytes,
	})
	if err != nil {
		logger.Sugar().Errorf("序列化群消息DF_RESPONSE失败: %v", err)
		return err
	}

	if err := publisher.PublishMessage(string(envBytes), targetContainerID); err != nil {
		logger.Sugar().Errorf("发布群消息到目标容器失败: container=%s, err=%v", targetContainerID, err)
		return err
	}

	return nil
}

func routePostToTarget(targetUserID, targetTopic, currentContainerID string, payload *pb.Post, message *pb.RequestMessage) error {
	if targetTopic == "" {
		logger.Sugar().Debugf("%s 用户不在线，消息已保存", targetUserID)
		return nil
	}

	wsHandler := GetWebSocketHandler()
	if wsHandler == nil || wsHandler.router == nil {
		logger.Sugar().Errorf("WebSocket处理器或路由器未初始化")
		return fmt.Errorf("WebSocket处理器或路由器未初始化")
	}

	messageBytes, err := buildPostDeliveryMessageBytes(targetTopic, currentContainerID, payload, message)
	if err != nil {
		return err
	}

	if err := wsHandler.router.RouteMessage(targetUserID, messageBytes); err != nil {
		logger.Sugar().Errorf("路由器发送消息失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息路由成功: %d -> %s (容器: %s)", payload.GetFromId(), targetUserID, targetTopic)
	return nil
}

func buildPostDeliveryMessageBytes(targetTopic, currentContainerID string, payload *pb.Post, message *pb.RequestMessage) ([]byte, error) {
	if targetTopic == currentContainerID {
		rsp := &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Post{
				Post: payload,
			},
		}
		messageBytes, err := proto.Marshal(rsp)
		if err != nil {
			logger.Sugar().Errorf("序列化响应消息失败: %v", err)
			return nil, err
		}
		return messageBytes, nil
	}

	messageBytes, err := proto.Marshal(message)
	if err != nil {
		logger.Sugar().Errorf("序列化RequestMessage失败: %v", err)
		return nil, err
	}
	return messageBytes, nil
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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布用户名更新请求到storage-service失败: %v", err)
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

	// 发布到storage-service主题
	err = publisher.PublishMessage(string(envBytes), "storage-service")
	if err != nil {
		logger.Sugar().Errorf("发布用户头像更新请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户头像更新请求已发送到storageService: user_id=%d, new_avatar=%s", payload.GetUserId(), payload.GetNewAvatarUrl())
	return nil
}
