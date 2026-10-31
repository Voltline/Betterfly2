package handlers

import (
	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	pushpb "Betterfly2/proto/push"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"data_forwarding_service/internal/publisher"
	"os"
	"strings"
)

const (
	storageServiceTopic = "storage-service"
	friendServiceTopic  = "friend-service"
	callServiceTopic    = "call-service"
	pushServiceTopic    = "push-service"
)

// SharedDeliveryTopic 是所有DF共同消费的topic，消息在消费组内只由一个DF处理。
func SharedDeliveryTopic() string {
	if topic := strings.TrimSpace(os.Getenv("DF_SHARED_TOPIC")); topic != "" {
		return topic
	}
	return "data-forwarding-shared"
}

func currentContainerTopic() string {
	containerID := os.Getenv("HOSTNAME")
	if containerID == "" {
		return "local"
	}
	return containerID
}

func newStorageRequest(fromKafkaTopic string, targetUserID int64) *storage.RequestMessage {
	return &storage.RequestMessage{
		FromKafkaTopic: fromKafkaTopic,
		TargetUserId:   targetUserID,
	}
}

func newFriendRequest(fromKafkaTopic string, targetUserID int64) *friend.RequestMessage {
	return &friend.RequestMessage{
		FromKafkaTopic: fromKafkaTopic,
		TargetUserId:   targetUserID,
	}
}

func publishStorageRequest(req *storage.RequestMessage) error {
	_, err := mq.PublishEnvelope(publisher.PublishMessage, storageServiceTopic, envelope.MessageType_STORAGE_REQUEST, req)
	if err != nil {
		logger.Sugar().Errorf("发布storage请求失败: %v", err)
	}
	return err
}

func publishFriendRequest(req *friend.RequestMessage) error {
	_, err := mq.PublishEnvelope(publisher.PublishMessage, friendServiceTopic, envelope.MessageType_FRIEND_REQUEST, req)
	if err != nil {
		logger.Sugar().Errorf("发布friend请求失败: %v", err)
	}
	return err
}

func publishCallRequest(req *callpb.InternalRequest) error {
	_, err := mq.PublishEnvelope(publisher.PublishMessage, callServiceTopic, envelope.MessageType_CALL_REQUEST, req)
	if err != nil {
		logger.Sugar().Errorf("发布call请求失败: %v", err)
	}
	return err
}

func publishPushRequest(req *pushpb.RequestMessage) error {
	_, err := mq.PublishEnvelope(publisher.PublishMessage, pushServiceTopic, envelope.MessageType_PUSH_REQUEST, req)
	if err != nil {
		logger.Sugar().Errorf("发布push请求失败: %v", err)
	}
	return err
}
