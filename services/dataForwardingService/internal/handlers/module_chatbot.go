package handlers

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"

	chatbotpb "Betterfly2/proto/chatbot"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"data_forwarding_service/internal/monitor"
	"data_forwarding_service/internal/publisher"
)

const (
	defaultChatbotName  = "Betterfly 助手"
	defaultChatbotTopic = "chatbot-service"
)

// chatbotConfig 描述ChatbotService托管的机器人账号。机器人是users表里的真实用户，
// 消息照常入库与同步；DF只负责在消息入库后通知ChatbotService，并替它接受好友申请和群邀请。
type chatbotConfig struct {
	userID  int64
	mention string // 归一化后的“@名字”
	topic   string
}

var chatbot = loadChatbotConfig()

// loadChatbotConfig 未配置CHATBOT_USER_ID或取值非法时关闭机器人，DF行为与之前一致。
func loadChatbotConfig() chatbotConfig {
	id, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("CHATBOT_USER_ID")), 10, 64)
	if err != nil || id <= 1 || monitor.IsMonitorID(id) {
		return chatbotConfig{}
	}
	name := strings.TrimSpace(os.Getenv("CHATBOT_NAME"))
	if name == "" {
		name = defaultChatbotName
	}
	topic := strings.TrimSpace(os.Getenv("CHATBOT_TOPIC"))
	if topic == "" {
		topic = defaultChatbotTopic
	}
	return chatbotConfig{userID: id, mention: normalizeMention("@" + name), topic: topic}
}

// IsChatbotID 判断用户ID是否为机器人账号。
func IsChatbotID(userID int64) bool {
	return chatbot.userID > 0 && userID == chatbot.userID
}

// normalizeMention 去掉空白并转小写，“@Betterfly 助手”和“@betterfly助手”视为同一个提及。
func normalizeMention(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}

// withoutChatbot 从群成员中去掉机器人：它没有连接也没有推送设备，投递和APNs都只会空转。
func withoutChatbot(memberIDs []int64) ([]int64, bool) {
	if chatbot.userID <= 0 {
		return memberIDs, false
	}
	for i, memberID := range memberIDs {
		if memberID == chatbot.userID {
			rest := make([]int64, 0, len(memberIDs)-1)
			rest = append(rest, memberIDs[:i]...)
			return append(rest, memberIDs[i+1:]...), true
		}
	}
	return memberIDs, false
}

// chatbotRepliesInGroup 群里只有文字消息@机器人，或引用机器人的消息时才触发；
// 频道卡片和讨论串不触发，机器人自己的消息也不触发。
func chatbotRepliesInGroup(payload *pb.Post) (bool, error) {
	if IsChatbotID(payload.GetFromId()) || payload.GetSourceChannelMessageId() != 0 || payload.GetDiscussionRootMessageId() != 0 ||
		strings.TrimSpace(payload.GetMsgType()) != "text" {
		return false, nil
	}
	if strings.Contains(normalizeMention(payload.GetMsg()), chatbot.mention) {
		return true, nil
	}
	if payload.GetReplyToMessageId() <= 0 {
		return false, nil
	}
	quoted, err := sharedDB.GetMessageByIDWithDB(sharedDB.DB(), payload.GetReplyToMessageId())
	if err != nil {
		return false, err
	}
	return quoted != nil && IsChatbotID(quoted.FromUserID), nil
}

func buildChatbotTrigger(messageID int64, payload *pb.Post, replyTopic string) ([]byte, string, error) {
	req := &chatbotpb.RequestMessage{Payload: &chatbotpb.RequestMessage_Trigger{Trigger: &chatbotpb.ChatbotTrigger{
		MessageId: messageID, FromUserId: payload.GetFromId(), ToId: payload.GetToId(), IsGroup: payload.GetIsGroup(), ReplyTopic: replyTopic,
	}}}
	encoded, err := mq.MarshalEnvelope(envelope.MessageType_CHATBOT_REQUEST, req)
	if err != nil {
		return nil, "", err
	}
	// 同一会话的触发进同一分区，机器人按发送顺序作答。
	key := fmt.Sprintf("u:%d", payload.GetFromId())
	if payload.GetIsGroup() {
		key = fmt.Sprintf("g:%d", payload.GetToId())
	}
	return encoded, key, nil
}

var publishChatbotMessage = publisher.PublishKeyedMessage

// publishChatbotTrigger 失败时返回错误，交给storage响应的重试：重投递由客户端按message_id去重，
// 推送有账本去重，机器人回复以client_message_id去重。
func publishChatbotTrigger(messageID int64, payload *pb.Post) error {
	encoded, key, err := buildChatbotTrigger(messageID, payload, currentContainerTopic())
	if err != nil {
		return err
	}
	if err := publishChatbotMessage(encoded, chatbot.topic, key); err != nil {
		logger.Sugar().Warnw("发布机器人触发失败", "message_id", messageID, "error", err)
		return err
	}
	return nil
}

// chatbotPendingRequest 从好友服务的创建回包里取出发给机器人的待处理好友申请或群邀请。
// 只看发给申请人的回包：申请人所在DF一定会收到它，重复提交（REQUEST_PENDING）也会再带一次，
// 机器人当时没接上的申请可以靠重发补上；发给机器人本人的created事件只会被丢弃。
func chatbotPendingRequest(resp *friend.ResponseMessage) *friend.RelationshipRequestInfo {
	operation := resp.GetRelationshipOperationRsp()
	if chatbot.userID <= 0 || operation == nil || IsChatbotID(resp.GetTargetUserId()) {
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

func buildChatbotAcceptRequest(request *friend.RelationshipRequestInfo, replyTopic string) *friend.RequestMessage {
	req := newFriendRequest(replyTopic, chatbot.userID)
	if request.GetRequestType() == sharedDB.RequestTypeFriend {
		req.Payload = &friend.RequestMessage_ResolveFriendRequest{ResolveFriendRequest: &friend.ResolveFriendRequest{
			UserId: chatbot.userID, RequestId: request.GetRequestId(), Decision: friend.RequestDecision_REQUEST_ACCEPT,
		}}
	} else {
		req.Payload = &friend.RequestMessage_ResolveGroupInvitation{ResolveGroupInvitation: &friend.ResolveGroupInvitation{
			UserId: chatbot.userID, InvitationId: request.GetRequestId(), Decision: friend.RequestDecision_REQUEST_ACCEPT,
		}}
	}
	return req
}

var publishChatbotFriendRequest = publishFriendRequest

// AcceptChatbotRequest 以机器人身份接受发给它的好友申请或群邀请，权限与状态校验仍由FriendService完成。
// 同意后好友服务照常把resolved事件推给申请人；发给机器人的回包由DF丢弃。重复同意只会得到INVALID_STATE。
func AcceptChatbotRequest(resp *friend.ResponseMessage) error {
	request := chatbotPendingRequest(resp)
	if request == nil {
		return nil
	}
	if err := publishChatbotFriendRequest(buildChatbotAcceptRequest(request, currentContainerTopic())); err != nil {
		return errors.Join(errors.New("机器人接受申请失败"), err)
	}
	logger.Sugar().Infow("机器人接受关系申请", "request_id", request.GetRequestId(), "request_type", request.GetRequestType(), "requester_user_id", request.GetRequesterUserId())
	return nil
}
