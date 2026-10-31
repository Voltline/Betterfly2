package handlers

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	chatbotpb "Betterfly2/proto/chatbot"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"Betterfly2/shared/utils"
	"data_forwarding_service/internal/publisher"
)

const defaultChatbotTopic = "chatbot-service"

const (
	BotEventRelationshipRequest = "relationship_request"
	BotEventFriendAdded         = "friend_added"
	BotEventGroupJoined         = "group_joined"
	BotEventFriendRemoved       = "friend_removed"
	BotEventGroupRemoved        = "group_removed"
)

// chatbotConfig 描述机器人平台的接入。机器人是users表里ID落在机器人段的真实用户，消息照常入库与同步；
// DF只负责在消息入库后通知机器人平台，并把与机器人有关的关系变化转给它。
type chatbotConfig struct {
	enabled bool
	topic   string
}

var chatbot = loadChatbotConfig()

// loadChatbotConfig 未开启CHATBOT_ENABLED时DF不识别机器人，行为与之前一致。
func loadChatbotConfig() chatbotConfig {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("CHATBOT_ENABLED")))
	topic := strings.TrimSpace(os.Getenv("CHATBOT_TOPIC"))
	if topic == "" {
		topic = defaultChatbotTopic
	}
	return chatbotConfig{enabled: enabled, topic: topic}
}

// IsChatbotID 判断用户ID是否为机器人账号。
func IsChatbotID(userID int64) bool {
	return chatbot.enabled && sharedDB.IsBotUserID(userID)
}

// withoutChatbot 从群成员中分出机器人：它们没有连接也没有推送设备，投递和APNs都只会空转。
func withoutChatbot(memberIDs []int64) ([]int64, []int64) {
	if !chatbot.enabled {
		return memberIDs, nil
	}
	var rest, bots []int64
	for _, memberID := range memberIDs {
		if IsChatbotID(memberID) {
			bots = append(bots, memberID)
		} else {
			rest = append(rest, memberID)
		}
	}
	if bots == nil {
		return memberIDs, nil
	}
	return rest, bots
}

// chatbotSeesGroupPost 只有群主时间线上的消息交给机器人平台判断是否@了某个机器人；
// 频道卡片、讨论串和机器人自己发的消息不触发，避免机器人之间互相应答。
func chatbotSeesGroupPost(payload *pb.Post) bool {
	return !IsChatbotID(payload.GetFromId()) && payload.GetSourceChannelMessageId() == 0 && payload.GetDiscussionRootMessageId() == 0
}

func buildChatbotTrigger(messageID int64, payload *pb.Post, botIDs []int64) ([]byte, string, error) {
	req := &chatbotpb.RequestMessage{Payload: &chatbotpb.RequestMessage_Trigger{Trigger: &chatbotpb.ChatbotTrigger{
		MessageId: messageID, FromUserId: payload.GetFromId(), ToId: payload.GetToId(), IsGroup: payload.GetIsGroup(), BotUserIds: botIDs,
	}}}
	encoded, err := mq.MarshalEnvelope(envelope.MessageType_CHATBOT_REQUEST, req)
	if err != nil {
		return nil, "", err
	}
	// 同一会话的触发进同一分区，机器人按发送顺序收到消息。
	key := fmt.Sprintf("u:%d", payload.GetFromId())
	if payload.GetIsGroup() {
		key = fmt.Sprintf("g:%d", payload.GetToId())
	}
	return encoded, key, nil
}

var publishChatbotMessage = publisher.PublishKeyedMessage

// publishChatbotTrigger 失败时返回错误，交给storage响应的重试：重投递由客户端按message_id去重，
// 推送有账本去重，机器人平台按message_id去重。
func publishChatbotTrigger(messageID int64, payload *pb.Post, botIDs []int64) error {
	encoded, key, err := buildChatbotTrigger(messageID, payload, botIDs)
	if err != nil {
		return err
	}
	if err := publishChatbotMessage(encoded, chatbot.topic, key); err != nil {
		logger.Sugar().Warnw("发布机器人触发失败", "message_id", messageID, "error", err)
		return err
	}
	return nil
}

func publishBotEvent(event *chatbotpb.BotEvent) error {
	if event.GetOccurredAt() == "" {
		event.OccurredAt = utils.NowTime()
	}
	encoded, err := mq.MarshalEnvelope(envelope.MessageType_CHATBOT_REQUEST, &chatbotpb.RequestMessage{
		Payload: &chatbotpb.RequestMessage_Event{Event: event},
	})
	if err != nil {
		return err
	}
	if err := publishChatbotMessage(encoded, chatbot.topic, fmt.Sprintf("b:%d", event.GetBotUserId())); err != nil {
		return errors.Join(errors.New("发布机器人事件失败"), err)
	}
	logger.Sugar().Infow("发布机器人事件", "type", event.GetType(), "bot_user_id", event.GetBotUserId(), "user_id", event.GetUserId(), "group_id", event.GetGroupId())
	return nil
}

// chatbotPendingRequest 从好友服务的创建回包里取出发给机器人的待处理好友申请或群邀请。
// 只看发给申请人的回包：申请人所在DF一定会收到它，重复提交（REQUEST_PENDING）也会再带一次，
// 机器人当时没处理的申请可以靠重发补上；发给机器人本人的created事件只会被丢弃。
func chatbotPendingRequest(resp *friend.ResponseMessage) *friend.RelationshipRequestInfo {
	operation := resp.GetRelationshipOperationRsp()
	if !chatbot.enabled || operation == nil || IsChatbotID(resp.GetTargetUserId()) {
		return nil
	}
	if resp.GetResult() != friend.FriendResult_FRIEND_OK && resp.GetResult() != friend.FriendResult_REQUEST_PENDING {
		return nil
	}
	request := operation.GetRequest()
	if request == nil || !IsChatbotID(request.GetTargetUserId()) || request.GetStatus() != sharedDB.RequestStatusPending {
		return nil
	}
	switch {
	case operation.GetOperation() == "create_friend_request" && request.GetRequestType() == sharedDB.RequestTypeFriend:
	case operation.GetOperation() == "invite_group_member" && request.GetRequestType() == sharedDB.RequestTypeGroupInvite:
	default:
		return nil
	}
	return request
}

// chatbotRelationshipEvent 把好友服务回给普通用户的响应里与机器人有关的变化转成机器人事件：
// 发给机器人的申请交给平台决定是否同意；申请被机器人同意后的resolved事件、删除机器人好友、
// 把机器人移出群分别通知机器人。没有关系变化时返回nil。
func chatbotRelationshipEvent(resp *friend.ResponseMessage) *chatbotpb.BotEvent {
	if !chatbot.enabled || IsChatbotID(resp.GetTargetUserId()) {
		return nil
	}
	if request := chatbotPendingRequest(resp); request != nil {
		return &chatbotpb.BotEvent{
			Type: BotEventRelationshipRequest, BotUserId: request.GetTargetUserId(), UserId: request.GetRequesterUserId(),
			GroupId: request.GetGroupId(), RequestId: request.GetRequestId(), RequestType: request.GetRequestType(),
		}
	}
	if changed := resp.GetRelationshipChangedEvent(); changed != nil {
		request := changed.GetRequest()
		if changed.GetOperation() != "resolved" || request.GetStatus() != sharedDB.RequestStatusAccepted ||
			!IsChatbotID(request.GetTargetUserId()) || request.GetRequesterUserId() != resp.GetTargetUserId() {
			return nil
		}
		event := &chatbotpb.BotEvent{BotUserId: request.GetTargetUserId(), UserId: request.GetRequesterUserId(), OccurredAt: changed.GetChangedAt()}
		switch request.GetRequestType() {
		case sharedDB.RequestTypeFriend:
			event.Type = BotEventFriendAdded
		case sharedDB.RequestTypeGroupInvite:
			event.Type, event.GroupId = BotEventGroupJoined, request.GetGroupId()
		default:
			return nil
		}
		return event
	}
	if resp.GetResult() != friend.FriendResult_FRIEND_OK {
		return nil
	}
	if operation := resp.GetFriendOperationRsp(); operation.GetOperation() == "remove_direct_friend" && IsChatbotID(operation.GetFriendId()) {
		return &chatbotpb.BotEvent{Type: BotEventFriendRemoved, BotUserId: operation.GetFriendId(), UserId: resp.GetTargetUserId(), OccurredAt: operation.GetUpdateTime()}
	}
	if operation := resp.GetGroupOperationRsp(); (operation.GetOperation() == "kick_group_member" || operation.GetOperation() == "remove_group_member") &&
		IsChatbotID(operation.GetUserId()) {
		return &chatbotpb.BotEvent{
			Type: BotEventGroupRemoved, BotUserId: operation.GetUserId(), UserId: resp.GetTargetUserId(), GroupId: operation.GetGroupId(), OccurredAt: operation.GetUpdateTime(),
		}
	}
	return nil
}

var publishChatbotEvent = publishBotEvent

// ForwardChatbotRelationshipEvent 把与机器人有关的关系变化交给机器人平台；同意与否、权限与状态仍由
// 平台和FriendService决定。发布失败返回错误，由Kafka重投这条好友响应。
func ForwardChatbotRelationshipEvent(resp *friend.ResponseMessage) error {
	event := chatbotRelationshipEvent(resp)
	if event == nil {
		return nil
	}
	return publishChatbotEvent(event)
}
