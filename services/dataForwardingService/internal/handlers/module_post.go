package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	storage "Betterfly2/proto/storage"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"
)

func init() {
	registerDFRequestModule(registerPostModule)
}

func registerPostModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_Post) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 Post 消息: from=%d to=%d", payload.Post.GetFromId(), payload.Post.GetToId())
		return dfRequestResult{}, handlePostMessage(ctx.fromID, ctx.message)
	})
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
	publishMessagePushBestEffort([]int64{payload.GetToId()}, payload)

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
	publishMessagePushBestEffort(membersWithoutSender(memberIDs, fromID), payload)
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

func publishMessagePushBestEffort(targetUserIDs []int64, payload *pb.Post) {
	if len(targetUserIDs) == 0 || payload == nil {
		return
	}
	if err := publishPushRequest(buildMessagePushRequest(targetUserIDs, payload)); err != nil {
		logger.Sugar().Warnf("发布普通消息APNs请求失败: sender_user_id=%d conversation_id=%d targets=%d error=%v", payload.GetFromId(), payload.GetToId(), len(targetUserIDs), err)
	}
}

func buildMessagePushRequest(targetUserIDs []int64, payload *pb.Post) *pushpb.RequestMessage {
	conversationID := payload.GetFromId()
	if payload.GetIsGroup() {
		conversationID = payload.GetToId()
	}
	return &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds:  targetUserIDs,
		SenderUserId:   payload.GetFromId(),
		ConversationId: conversationID,
		IsGroup:        payload.GetIsGroup(),
		MessageType:    payload.GetMsgType(),
		SentAt:         payload.GetTimestamp(),
		Preview:        messagePushPreview(payload),
	}}}
}

func messagePushPreview(payload *pb.Post) string {
	if payload == nil {
		return "发来一条消息"
	}
	var preview string
	switch strings.ToLower(strings.TrimSpace(payload.GetMsgType())) {
	case "text", "link":
		preview = strings.TrimSpace(payload.GetMsg())
	case "image":
		preview = "发送了一张图片"
	case "gif":
		preview = "发送了一个 GIF"
	case "file":
		if name := strings.TrimSpace(payload.GetRealFileName()); name != "" {
			preview = "发送了文件：" + name
		} else {
			preview = "发送了一个文件"
		}
	case "audio":
		preview = "发送了一条语音"
	case "video":
		preview = "发送了一段视频"
	default:
		preview = "发来一条消息"
	}
	if preview == "" {
		preview = "发来一条消息"
	}
	runes := []rune(preview)
	if len(runes) > 180 {
		preview = string(runes[:180]) + "…"
	}
	return preview
}

func membersWithoutSender(memberIDs []int64, senderID int64) []int64 {
	targets := make([]int64, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID != senderID {
			targets = append(targets, memberID)
		}
	}
	return targets
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
