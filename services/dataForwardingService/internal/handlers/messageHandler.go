package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	auth "Betterfly2/proto/server_rpc/auth"
	storage "Betterfly2/proto/storage"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"context"
	"data_forwarding_service/internal/grpcClient"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

type dfRequestContext struct {
	fromID  int64
	message *pb.RequestMessage
}

type dfRequestResult struct {
	code int
}

var dfRequestRouter = newDFRequestRouter()

func newDFRequestRouter() *dispatch.OneofRouter[dfRequestContext, dfRequestResult] {
	router := dispatch.NewOneofRouter[dfRequestContext, dfRequestResult]()
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_Post) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 Post 消息: from=%d to=%d", payload.Post.GetFromId(), payload.Post.GetToId())
		return dfRequestResult{}, handlePostMessage(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryUser 消息")
		return dfRequestResult{}, handleQueryUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertContact) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertContact 消息")
		return dfRequestResult{}, handleInsertContact(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryContacts) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryContacts 消息")
		return dfRequestResult{}, handleQueryContacts(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_DeleteContact) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 DeleteContact 消息")
		return dfRequestResult{}, handleDeleteContact(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateContactAlias) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateContactAlias 消息")
		return dfRequestResult{}, handleUpdateContactAlias(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateContactNotify) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateContactNotify 消息")
		return dfRequestResult{}, handleUpdateContactNotify(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroup) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryGroup 消息")
		return dfRequestResult{}, handleQueryGroup(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertGroup) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertGroup 消息")
		return dfRequestResult{}, handleInsertGroup(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertGroupUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertGroupUser 消息")
		return dfRequestResult{}, handleInsertGroupUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroupMembers) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryGroupMembers 消息")
		return dfRequestResult{}, handleQueryGroupMembers(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryJoinedGroups) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryJoinedGroups 消息")
		return dfRequestResult{}, handleQueryJoinedGroups(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_DeleteGroupUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 DeleteGroupUser 消息")
		return dfRequestResult{}, handleDeleteGroupUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateAvatar) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateAvatar 消息")
		return dfRequestResult{}, handleUpdateAvatar(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateUserName) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateUserName 消息")
		return dfRequestResult{}, handleUpdateUserName(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateUserAvatar) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateUserAvatar 消息")
		return dfRequestResult{}, handleUpdateUserAvatar(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_QueryMessage) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryMessage 消息: message_id=%d", payload.QueryMessage.GetMessageId())
		return dfRequestResult{}, handleQueryMessage(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_QuerySyncMessages) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QuerySyncMessages 消息: to_user_id=%d", payload.QuerySyncMessages.GetToUserId())
		return dfRequestResult{}, handleQuerySyncMessages(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(_ dfRequestContext, _ *pb.RequestMessage_Logout) (dfRequestResult, error) {
		logger.Sugar().Infof("用户登出")
		return dfRequestResult{code: 1}, nil
	})
	dispatch.Register(router, func(_ dfRequestContext, payload *pb.RequestMessage_Login) (dfRequestResult, error) {
		logger.Sugar().Warnf("收到认证服务请求，不处理：%+v", payload)
		return dfRequestResult{}, nil
	})
	dispatch.Register(router, func(_ dfRequestContext, payload *pb.RequestMessage_Signup) (dfRequestResult, error) {
		logger.Sugar().Warnf("收到认证服务请求，不处理：%+v", payload)
		return dfRequestResult{}, nil
	})
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
	result, err := dfRequestRouter.Dispatch(dfRequestContext{
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
	storeReq := buildStoreNewMessageStorageRequest(payload, currentContainerID)
	if err := publishStorageRequest(storeReq); err != nil {
		logger.Sugar().Errorf("发布消息到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息已保存到storageService: from=%d to=%d", payload.GetFromId(), payload.GetToId())
	return nil
}

func buildStoreNewMessageStorageRequest(payload *pb.Post, currentContainerID string) *storage.RequestMessage {
	req := newStorageRequest(currentContainerID, payload.GetFromId())
	req.Payload = &storage.RequestMessage_StoreNewMessage{
		StoreNewMessage: &storage.StoreNewMessage{
			FromUserId:   payload.GetFromId(),
			ToUserId:     payload.GetToId(),
			Content:      payload.GetMsg(),
			MessageType:  payload.GetMsgType(),
			IsGroup:      payload.GetIsGroup(),
			RealFileName: payload.GetRealFileName(),
		},
	}
	return req
}

func handlePostMessage(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "转发消息", "post", (*pb.RequestMessage).GetPost)
	if err != nil {
		return err
	}
	payload.FromId = fromID
	if err := validatePostPayload(payload); err != nil {
		return err
	}

	targetUserID := strconv.FormatInt(payload.GetToId(), 10)
	targetTopic := redisClient.GetContainerByConnection(targetUserID)

	currentContainerID := currentContainerTopic()

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
	payload, err := authenticatedPayload(fromID, message, "查询消息", "query_message", (*pb.RequestMessage).GetQueryMessage)
	if err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	storeReq := newStorageRequest(currentContainerID, fromID)
	storeReq.Payload = &storage.RequestMessage_QueryMessage{
		QueryMessage: &storage.QueryMessage{
			MessageId: payload.GetMessageId(),
		},
	}

	if err := publishStorageRequest(storeReq); err != nil {
		logger.Sugar().Errorf("发布查询请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("消息查询请求已发送到storageService: message_id=%d", payload.GetMessageId())
	return nil
}

// handleQuerySyncMessages 处理同步消息请求
func handleQuerySyncMessages(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询同步消息", "query_sync_messages", (*pb.RequestMessage).GetQuerySyncMessages)
	if err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	storeReq := buildSyncMessagesStorageRequest(fromID, payload, currentContainerID)
	if err := publishStorageRequest(storeReq); err != nil {
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
	req := newStorageRequest(currentContainerID, fromID)
	req.Payload = &storage.RequestMessage_QuerySyncMessages{
		QuerySyncMessages: &storage.QuerySyncMessages{
			ToUserId:  payload.GetToUserId(),
			Timestamp: payload.GetTimestamp(),
		},
	}
	return req
}

// handleQueryUser 处理查询用户信息请求
func handleQueryUser(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询用户信息", "query_user", (*pb.RequestMessage).GetQueryUser)
	if err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	storeReq := newStorageRequest(currentContainerID, fromID)
	storeReq.Payload = &storage.RequestMessage_QueryUser{
		QueryUser: &storage.QueryUser{
			UserId: payload.GetToQueryUserId(),
		},
	}

	if err := publishStorageRequest(storeReq); err != nil {
		logger.Sugar().Errorf("发布用户查询请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户信息查询请求已发送到storageService: to_query_user_id=%d", payload.GetToQueryUserId())
	return nil
}

func handleInsertContact(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "添加好友", "insert_contact", (*pb.RequestMessage).GetInsertContact)
	if err != nil {
		return err
	}
	if err := requirePositiveID("to_insert_user_id", payload.GetToInsertUserId()); err != nil {
		return err
	}
	if payload.GetToInsertUserId() == fromID {
		return errors.New("不能添加自己为好友")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertContactFriendRequest(fromID, payload.GetToInsertUserId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("添加好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToInsertUserId())
	return nil
}

func handleQueryContacts(fromID int64, message *pb.RequestMessage) error {
	if _, err := authenticatedPayload(fromID, message, "查询好友列表", "query_contacts", (*pb.RequestMessage).GetQueryContacts); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	friendReq := buildQueryContactsFriendRequest(fromID, currentContainerID)

	if err := publishFriendRequest(friendReq); err != nil {
		logger.Sugar().Errorf("发布QueryContacts请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryContactsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryFriendList{
		QueryFriendList: &friend.QueryFriendList{
			UserId: fromID,
		},
	}
	return req
}

func handleQueryGroup(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询群信息", "query_group", (*pb.RequestMessage).GetQueryGroup)
	if err != nil {
		return err
	}
	if err := requirePositiveID("to_query_group_id", payload.GetToQueryGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryGroupFriendRequest(fromID, payload.GetToQueryGroupId(), payload.GetClientNeedSave(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群信息查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetToQueryGroupId())
	return nil
}

func buildQueryGroupFriendRequest(fromID, groupID int64, clientNeedSave bool, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryGroup{
		QueryGroup: &friend.QueryGroup{
			RequestUserId:  fromID,
			GroupId:        groupID,
			ClientNeedSave: clientNeedSave,
		},
	}
	return req
}

func handleInsertGroup(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "创建群组", "insert_group", (*pb.RequestMessage).GetInsertGroup)
	if err != nil {
		return err
	}
	if payload.GetToBeCreatedGroupId() <= 0 || strings.TrimSpace(payload.GetToBeCreatedGroupName()) == "" {
		return errors.New("群组信息非法")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertGroupFriendRequest(fromID, payload.GetToBeCreatedGroupId(), payload.GetToBeCreatedGroupName(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("创建群组请求已发送到friendService: owner_id=%d, group_id=%d", fromID, payload.GetToBeCreatedGroupId())
	return nil
}

func buildInsertGroupFriendRequest(fromID, groupID int64, groupName, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_CreateGroup{
		CreateGroup: &friend.CreateGroup{
			OwnerUserId: fromID,
			GroupId:     groupID,
			GroupName:   groupName,
		},
	}
	return req
}

func handleInsertGroupUser(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "加入群组", "insert_group_user", (*pb.RequestMessage).GetInsertGroupUser)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertGroupUserFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("加入群组请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildInsertGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_AddGroupMember{
		AddGroupMember: &friend.AddGroupMember{
			UserId:  fromID,
			GroupId: groupID,
		},
	}
	return req
}

func handleQueryGroupMembers(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询群成员", "query_group_members", (*pb.RequestMessage).GetQueryGroupMembers)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryGroupMembersFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroupMembers请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群成员列表查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func handleQueryJoinedGroups(fromID int64, message *pb.RequestMessage) error {
	if _, err := authenticatedPayload(fromID, message, "查询已加入群列表", "query_joined_groups", (*pb.RequestMessage).GetQueryJoinedGroups); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryJoinedGroupsFriendRequest(fromID, currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryJoinedGroups请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("已加入群列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryJoinedGroupsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryJoinedGroups{
		QueryJoinedGroups: &friend.QueryJoinedGroups{
			UserId: fromID,
		},
	}
	return req
}

func buildQueryGroupMembersFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryGroupMembers{
		QueryGroupMembers: &friend.QueryGroupMembers{
			RequestUserId: fromID,
			GroupId:       groupID,
		},
	}
	return req
}

func handleDeleteGroupUser(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "退出群组", "delete_group_user", (*pb.RequestMessage).GetDeleteGroupUser)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildDeleteGroupUserFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("退群请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildDeleteGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_RemoveGroupMember{
		RemoveGroupMember: &friend.RemoveGroupMember{
			RequestUserId: fromID,
			GroupId:       groupID,
			UserId:        fromID,
		},
	}
	return req
}

func handleUpdateAvatar(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新头像", "update_avatar", (*pb.RequestMessage).GetUpdateAvatar)
	if err != nil {
		return err
	}
	if !payload.GetIsGroup() {
		return errors.New("当前仅支持通过UpdateAvatar更新群头像")
	}
	if err := requirePositiveID("target_id", payload.GetTargetId()); err != nil {
		return errors.New("群头像更新请求非法")
	}
	if strings.TrimSpace(payload.GetAvatarHash()) == "" {
		return errors.New("群头像更新请求非法")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateGroupAvatarFriendRequest(fromID, payload.GetTargetId(), payload.GetAvatarHash(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateAvatar请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群头像更新请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetId())
	return nil
}

func buildUpdateGroupAvatarFriendRequest(fromID, groupID int64, avatarHash, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateGroupAvatar{
		UpdateGroupAvatar: &friend.UpdateGroupAvatar{
			RequestUserId: fromID,
			GroupId:       groupID,
			AvatarHash:    avatarHash,
		},
	}
	return req
}

func buildInsertContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_AddDirectFriend{
		AddDirectFriend: &friend.AddDirectFriend{
			UserId:   fromID,
			FriendId: targetUserID,
		},
	}
	return req
}

func handleDeleteContact(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "删除好友", "delete_contact", (*pb.RequestMessage).GetDeleteContact)
	if err != nil {
		return err
	}
	if err := requireNonSelfID("to_delete_user_id", payload.GetToDeleteUserId(), fromID); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildDeleteContactFriendRequest(fromID, payload.GetToDeleteUserId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("删除好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToDeleteUserId())
	return nil
}

func buildDeleteContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_RemoveDirectFriend{
		RemoveDirectFriend: &friend.RemoveDirectFriend{
			UserId:   fromID,
			FriendId: targetUserID,
		},
	}
	return req
}

func handleUpdateContactAlias(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新好友备注", "update_contact_alias", (*pb.RequestMessage).GetUpdateContactAlias)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_user_id", payload.GetTargetUserId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateContactAliasFriendRequest(fromID, payload.GetTargetUserId(), payload.GetNewAlias(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactAlias请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友备注更新请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetTargetUserId())
	return nil
}

func buildUpdateContactAliasFriendRequest(fromID, targetUserID int64, alias, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateFriendAlias{
		UpdateFriendAlias: &friend.UpdateFriendAlias{
			UserId:   fromID,
			FriendId: targetUserID,
			Alias:    alias,
		},
	}
	return req
}

func handleUpdateContactNotify(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新好友通知设置", "update_contact_notify", (*pb.RequestMessage).GetUpdateContactNotify)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_user_id", payload.GetTargetUserId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateContactNotifyFriendRequest(fromID, payload.GetTargetUserId(), payload.GetIsNotify(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactNotify请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友通知设置更新请求已发送到friendService: user_id=%d, friend_id=%d, is_notify=%v", fromID, payload.GetTargetUserId(), payload.GetIsNotify())
	return nil
}

func buildUpdateContactNotifyFriendRequest(fromID, targetUserID int64, isNotify bool, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateFriendNotify{
		UpdateFriendNotify: &friend.UpdateFriendNotify{
			UserId:   fromID,
			FriendId: targetUserID,
			IsNotify: isNotify,
		},
	}
	return req
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

	targetUserIDs := make([]string, 0, len(memberIDs))
	memberIDByUserID := make(map[string]int64, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID == fromID {
			continue
		}
		targetUserID := strconv.FormatInt(memberID, 10)
		targetUserIDs = append(targetUserIDs, targetUserID)
		memberIDByUserID[targetUserID] = memberID
	}
	containerByUserID := redisClient.GetContainersByConnections(targetUserIDs)

	delivered := 0
	crossContainerTargets := make(map[string][]int64)
	for _, targetUserID := range targetUserIDs {
		targetTopic := containerByUserID[targetUserID]
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

		memberID := memberIDByUserID[targetUserID]
		crossContainerTargets[targetTopic] = append(crossContainerTargets[targetTopic], memberID)
	}

	for targetTopic, targetUserIDs := range crossContainerTargets {
		if err := routeGroupPostBatchCrossContainer(targetTopic, targetUserIDs, payload); err != nil {
			logger.Sugar().Errorf("群消息批量转发失败: group_id=%d, target_container=%s, targets=%d, err=%v", payload.GetToId(), targetTopic, len(targetUserIDs), err)
			continue
		}
		delivered += len(targetUserIDs)
	}

	logger.Sugar().Debugf("群消息处理完成: group_id=%d, delivered=%d", payload.GetToId(), delivered)
	return nil
}

func routeGroupPostCrossContainer(targetContainerID string, targetUserID int64, payload *pb.Post) error {
	return routeGroupPostBatchCrossContainer(targetContainerID, []int64{targetUserID}, payload)
}

func routeGroupPostBatchCrossContainer(targetContainerID string, targetUserIDs []int64, payload *pb.Post) error {
	if len(targetUserIDs) == 0 {
		return nil
	}

	envBytes, err := buildGroupPostDeliveryEnvelopeBytes(targetUserIDs, payload)
	if err != nil {
		return err
	}

	if err := publisher.PublishMessage(string(envBytes), targetContainerID); err != nil {
		logger.Sugar().Errorf("发布群消息到目标容器失败: container=%s, err=%v", targetContainerID, err)
		return err
	}

	return nil
}

func buildGroupPostDeliveryEnvelopeBytes(targetUserIDs []int64, payload *pb.Post) ([]byte, error) {
	if len(targetUserIDs) == 0 {
		return nil, nil
	}

	var deliveryPayload *pb.DFInternalDelivery
	if len(targetUserIDs) > 1 {
		deliveryPayload = &pb.DFInternalDelivery{
			Payload: &pb.DFInternalDelivery_GroupPostBatchDelivery{
				GroupPostBatchDelivery: &pb.GroupPostBatchDelivery{
					TargetUserIds: targetUserIDs,
					Post:          payload,
				},
			},
		}
	} else {
		deliveryPayload = &pb.DFInternalDelivery{
			Payload: &pb.DFInternalDelivery_GroupPostDelivery{
				GroupPostDelivery: &pb.GroupPostDelivery{
					TargetUserId: targetUserIDs[0],
					Post:         payload,
				},
			},
		}
	}

	envBytes, err := mq.MarshalEnvelope(envelope.MessageType_DF_RESPONSE, deliveryPayload)
	if err != nil {
		logger.Sugar().Errorf("序列化群消息DF_RESPONSE失败: %v", err)
		return nil, err
	}

	return envBytes, nil
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
	payload, err := authenticatedPayload(fromID, message, "更新用户名", "update_user_name", (*pb.RequestMessage).GetUpdateUserName)
	if err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	storeReq := newStorageRequest(currentContainerID, fromID)
	storeReq.Payload = &storage.RequestMessage_UpdateUserName{
		UpdateUserName: &storage.UpdateUserName{
			UserId:      payload.GetUserId(),
			NewUserName: payload.GetNewUserName(),
		},
	}

	if err := publishStorageRequest(storeReq); err != nil {
		logger.Sugar().Errorf("发布用户名更新请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户名更新请求已发送到storageService: user_id=%d, new_name=%s", payload.GetUserId(), payload.GetNewUserName())
	return nil
}

// handleUpdateUserAvatar 处理更新用户头像请求
func handleUpdateUserAvatar(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新用户头像", "update_user_avatar", (*pb.RequestMessage).GetUpdateUserAvatar)
	if err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	storeReq := newStorageRequest(currentContainerID, fromID)
	storeReq.Payload = &storage.RequestMessage_UpdateUserAvatar{
		UpdateUserAvatar: &storage.UpdateUserAvatar{
			UserId:       payload.GetUserId(),
			NewAvatarUrl: payload.GetNewAvatarUrl(),
		},
	}

	if err := publishStorageRequest(storeReq); err != nil {
		logger.Sugar().Errorf("发布用户头像更新请求到storage-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("用户头像更新请求已发送到storageService: user_id=%d, new_avatar=%s", payload.GetUserId(), payload.GetNewAvatarUrl())
	return nil
}
