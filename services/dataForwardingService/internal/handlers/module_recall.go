package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"
)

// DeliverMessageRecall performs best-effort realtime delivery. Offline users
// converge through the recalled fields returned by message sync.
func DeliverMessageRecall(event *pb.MessageRecallEvent) error {
	if event == nil || event.GetResult() != pb.MessageRecallResult_MESSAGE_RECALL_OK || event.GetMessageId() <= 0 {
		return fmt.Errorf("待投递的消息撤回事件无效")
	}

	var targetIDs []int64
	if event.GetIsGroup() {
		memberIDs, err := sharedDB.GetActiveGroupMemberIDs(event.GetToUserId())
		if err != nil {
			return err
		}
		targetIDs = memberIDs
	} else {
		targetIDs = []int64{event.GetToUserId()}
	}
	targetIDs = recallTargetsWithoutOperator(targetIDs, event.GetOperatorUserId())
	if len(targetIDs) == 0 {
		return nil
	}
	if err := publishPushRequest(buildMessageRecallPushRequest(targetIDs, event)); err != nil {
		return fmt.Errorf("发布消息撤回APNs请求失败: %w", err)
	}

	userIDs := make([]string, 0, len(targetIDs))
	userIDValues := make(map[string]int64, len(targetIDs))
	for _, targetID := range targetIDs {
		userID := strconv.FormatInt(targetID, 10)
		userIDs = append(userIDs, userID)
		userIDValues[userID] = targetID
	}
	routes, err := redisClient.GetContainersByConnections(userIDs)
	if err != nil {
		return err
	}

	responseBytes, err := proto.Marshal(&pb.ResponseMessage{
		Payload: &pb.ResponseMessage_MessageRecallEvent{MessageRecallEvent: event},
	})
	if err != nil {
		return err
	}
	currentTopic := currentContainerTopic()
	wsHandler := GetWebSocketHandler()
	if wsHandler == nil {
		return errors.New("WebSocket处理器未初始化")
	}
	crossContainerTargets := make(map[string][]int64)
	for _, userID := range userIDs {
		topic := routes[userID]
		if topic == "" {
			continue
		}
		if topic == currentTopic {
			if err := wsHandler.SendMessage(userID, responseBytes); err != nil {
				return err
			}
			continue
		}
		crossContainerTargets[topic] = append(crossContainerTargets[topic], userIDValues[userID])
	}

	for topic, topicTargets := range crossContainerTargets {
		if err := publishRecallDelivery(topic, topicTargets, event); err != nil {
			return err
		}
	}
	return nil
}

func buildMessageRecallPushRequest(targetUserIDs []int64, event *pb.MessageRecallEvent) *pushpb.RequestMessage {
	if event == nil {
		return &pushpb.RequestMessage{}
	}
	conversationID := event.GetFromUserId()
	if event.GetIsGroup() {
		conversationID = event.GetToUserId()
	}
	return &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessageRecall{MessageRecall: &pushpb.MessageRecallPushRequest{
		TargetUserIds: targetUserIDs,
		MessageId:     event.GetMessageId(), ConversationId: conversationID, IsGroup: event.GetIsGroup(),
		OperatorUserId: event.GetOperatorUserId(), RecalledAt: event.GetRecalledAt(),
	}}}
}

func recallTargetsWithoutOperator(targets []int64, operatorUserID int64) []int64 {
	result := make([]int64, 0, len(targets))
	seen := make(map[int64]struct{}, len(targets))
	for _, targetID := range targets {
		if targetID <= 0 || targetID == operatorUserID {
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		result = append(result, targetID)
	}
	return result
}

func publishRecallDelivery(topic string, targetUserIDs []int64, event *pb.MessageRecallEvent) error {
	if topic == "" || len(targetUserIDs) == 0 {
		return nil
	}
	delivery := &pb.DFInternalDelivery{}
	if len(targetUserIDs) == 1 {
		delivery.Payload = &pb.DFInternalDelivery_MessageRecallDelivery{MessageRecallDelivery: &pb.MessageRecallDelivery{
			TargetUserId: targetUserIDs[0],
			Event:        event,
		}}
	} else {
		delivery.Payload = &pb.DFInternalDelivery_MessageRecallBatchDelivery{MessageRecallBatchDelivery: &pb.MessageRecallBatchDelivery{
			TargetUserIds: targetUserIDs,
			Event:         event,
		}}
	}
	envelopeBytes, err := mq.MarshalEnvelope(envelope.MessageType_DF_RESPONSE, delivery)
	if err != nil {
		return err
	}
	if err := publisher.PublishMessage(string(envelopeBytes), topic); err != nil {
		logger.Sugar().Errorf("跨容器发布消息撤回事件失败: topic=%s targets=%d err=%v", topic, len(targetUserIDs), err)
		return err
	}
	return nil
}
