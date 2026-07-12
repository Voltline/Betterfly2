package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/monitor"
)

func init() {
	registerDFRequestModule(registerStorageRequestModules)
}

func registerStorageRequestModules(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_QueryMessage) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryMessage 消息: message_id=%d", payload.QueryMessage.GetMessageId())
		return dfRequestResult{}, handleQueryMessage(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_QuerySyncMessages) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QuerySyncMessages 消息: to_user_id=%d", payload.QuerySyncMessages.GetToUserId())
		return dfRequestResult{}, handleQuerySyncMessages(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryUser 消息")
		return dfRequestResult{}, handleQueryUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateUserName) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateUserName 消息")
		return dfRequestResult{}, handleUpdateUserName(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateUserAvatar) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateUserAvatar 消息")
		return dfRequestResult{}, handleUpdateUserAvatar(ctx.fromID, ctx.message)
	})
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
	if monitor.IsMonitorID(payload.GetToQueryUserId()) {
		return handleMonitorQueryUser(fromID)
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
