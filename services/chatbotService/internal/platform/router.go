package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"chatbotService/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ErrInvalidTrigger 表示触发消息本身不合法，重试也不会成功。
var ErrInvalidTrigger = errors.New("机器人触发消息无效")

var routedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "betterfly_chatbot_routed_total",
	Help: "机器人平台处理触发与事件的结果",
}, []string{"kind", "result"})

// Publisher 按key发布到Kafka。
type Publisher func(ctx context.Context, topic, key string, payload []byte) error

// Platform 把DF发来的触发与事件分发给各个机器人：内置机器人转发到它的Kafka topic，
// 第三方机器人写成更新，由长轮询或webhook取走。
type Platform struct {
	config  config.Config
	store   *Store
	publish Publisher
	waiters *waiters
	objects Objects // 未配置RustFS时为nil
	now     func() time.Time
	// onQueued 在写入新更新后调用，唤醒webhook投递
	onQueued func()
}

func New(cfg config.Config, store *Store, publish Publisher, objects Objects) *Platform {
	return &Platform{config: cfg, store: store, publish: publish, waiters: newWaiters(), objects: objects, now: time.Now}
}

func (p *Platform) result(kind, result string, fields ...any) {
	routedTotal.WithLabelValues(kind, result).Inc()
	if result != "delivered" && result != "queued" {
		logger.Sugar().Infow("机器人平台跳过", append([]any{"kind", kind, "result", result}, fields...)...)
	}
}

// HandleTrigger 处理一条入库后的消息。单聊交给to_id对应的机器人；群里交给被@、被引用或收到命令的机器人。
func (p *Platform) HandleTrigger(ctx context.Context, trigger *chatbotpb.ChatbotTrigger) error {
	if trigger.GetMessageId() <= 0 || trigger.GetFromUserId() <= 0 || trigger.GetToId() <= 0 ||
		(!trigger.GetIsGroup() && !db.IsBotUserID(trigger.GetToId())) {
		return ErrInvalidTrigger
	}
	if db.IsBotUserID(trigger.GetFromUserId()) {
		p.result("trigger", "from_bot", "message_id", trigger.GetMessageId())
		return nil // 机器人之间互相应答会无限循环
	}
	message, err := p.store.Message(ctx, trigger.GetMessageId())
	if err != nil {
		return err
	}
	if message == nil || message.FromUserID != trigger.GetFromUserId() || message.ToUserID != trigger.GetToId() || message.IsGroup != trigger.GetIsGroup() {
		p.result("trigger", "mismatch", "message_id", trigger.GetMessageId())
		return nil
	}
	if message.IsRecalled {
		p.result("trigger", "recalled", "message_id", message.MessageID)
		return nil
	}
	if !message.IsGroup {
		bot, err := p.store.Bot(ctx, message.ToUserID)
		if err != nil {
			return err
		}
		if bot == nil || bot.Disabled {
			p.result("trigger", "inactive_bot", "message_id", message.MessageID, "bot_user_id", message.ToUserID)
			return nil
		}
		return p.deliver(ctx, bot, trigger, message, "")
	}

	botIDs := trigger.GetBotUserIds()
	if len(botIDs) == 0 {
		if botIDs, err = p.store.GroupBots(ctx, message.ToUserID); err != nil {
			return err
		}
	}
	text := messageText(message)
	command, commandTarget := parseCommand(text)
	var replyToSender int64
	if message.ReplyToMessageID > 0 {
		if replyToSender, err = p.store.MessageSender(ctx, message.ReplyToMessageID); err != nil {
			return err
		}
	}
	for _, botID := range botIDs {
		bot, err := p.store.Bot(ctx, botID)
		if err != nil {
			return err
		}
		if bot == nil || bot.Disabled || !bot.AllowGroups {
			p.result("trigger", "inactive_bot", "message_id", message.MessageID, "bot_user_id", botID)
			continue
		}
		addressed := replyToSender == bot.UserID || mentions(text, bot)
		botCommand := ""
		// 只有第三方机器人接收命令；/cmd@account只给对应账号，不带@的命令给群里所有第三方机器人。
		if command != "" && bot.Delivery == db.BotDeliveryAPI && (commandTarget == "" || commandTarget == strings.ToLower(bot.Account)) {
			addressed, botCommand = true, command
		}
		if !addressed {
			continue
		}
		if err := p.deliver(ctx, bot, trigger, message, botCommand); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) deliver(ctx context.Context, bot *BotInfo, trigger *chatbotpb.ChatbotTrigger, message *db.Message, command string) error {
	if bot.Delivery == db.BotDeliveryKafka {
		forward := &chatbotpb.ChatbotTrigger{
			MessageId: trigger.GetMessageId(), FromUserId: trigger.GetFromUserId(), ToId: trigger.GetToId(), IsGroup: trigger.GetIsGroup(),
		}
		if message.IsGroup {
			forward.BotUserIds = []int64{bot.UserID}
		}
		payload, err := mq.MarshalEnvelope(envelope.MessageType_CHATBOT_REQUEST, &chatbotpb.RequestMessage{
			Payload: &chatbotpb.RequestMessage_Trigger{Trigger: forward},
		})
		if err != nil {
			return err
		}
		if err := p.publish(ctx, bot.KafkaTopic, conversationKey(message), payload); err != nil {
			return fmt.Errorf("转发机器人触发失败: %w", err)
		}
		p.result("trigger", "delivered")
		return nil
	}

	if command == "" && !message.IsGroup {
		command, _ = parseCommand(messageText(message))
	}
	update, err := p.messageUpdate(ctx, message, command)
	if err != nil {
		return err
	}
	return p.queue(ctx, bot.UserID, fmt.Sprintf("m:%d", message.MessageID), update)
}

func conversationKey(message *db.Message) string {
	if message.IsGroup {
		return fmt.Sprintf("g:%d", message.ToUserID)
	}
	return fmt.Sprintf("u:%d", message.FromUserID)
}

func (p *Platform) messageUpdate(ctx context.Context, message *db.Message, command string) (*Update, error) {
	from, err := p.userBrief(ctx, message.FromUserID)
	if err != nil {
		return nil, err
	}
	chat := Chat{ID: message.FromUserID, Type: ChatPrivate}
	if message.IsGroup {
		chat = Chat{ID: message.ToUserID, Type: ChatGroup}
		if chat.Title, err = p.store.GroupName(ctx, message.ToUserID); err != nil {
			return nil, err
		}
	}
	update := &UpdateMessage{
		MessageID: message.MessageID, Chat: chat, From: from, Date: message.Timestamp, Type: message.MessageType,
		Caption: message.Caption, File: fileID(message), ReplyToMessageID: message.ReplyToMessageID, Command: command,
	}
	if update.File == nil {
		update.Text = message.Content
	}
	return &Update{Message: update}, nil
}

func (p *Platform) userBrief(ctx context.Context, userID int64) (UserBrief, error) {
	user, err := p.store.User(ctx, userID)
	if err != nil {
		return UserBrief{}, err
	}
	if user == nil {
		return UserBrief{ID: userID}, nil
	}
	return *user, nil
}

func (p *Platform) queue(ctx context.Context, botID int64, dedupKey string, update *Update) error {
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	created, err := p.store.InsertUpdate(ctx, botID, dedupKey, string(payload))
	if err != nil {
		return err
	}
	if created {
		p.waiters.notify(botID)
		if p.onQueued != nil {
			p.onQueued()
		}
		p.result("update", "queued")
	} else {
		p.result("update", "duplicate", "bot_user_id", botID, "dedup_key", dedupKey)
	}
	return nil
}

// HandleEvent 处理与机器人有关的关系变化。好友申请一律同意；群邀请在允许入群时同意；
// 停用的机器人拒绝所有申请。其余事件转给第三方机器人。
func (p *Platform) HandleEvent(ctx context.Context, event *chatbotpb.BotEvent) error {
	if !db.IsBotUserID(event.GetBotUserId()) || event.GetUserId() <= 0 {
		return ErrInvalidTrigger
	}
	bot, err := p.store.Bot(ctx, event.GetBotUserId())
	if err != nil {
		return err
	}
	if bot == nil {
		// 内置机器人还没启动登记时申请保持待处理，用户重新提交即可补上。
		p.result("event", "unknown_bot", "bot_user_id", event.GetBotUserId(), "type", event.GetType())
		return nil
	}
	if event.GetType() == "relationship_request" {
		return p.resolveRequest(ctx, bot, event)
	}
	if bot.Delivery != db.BotDeliveryAPI {
		p.result("event", "builtin", "bot_user_id", bot.UserID, "type", event.GetType())
		return nil
	}
	user, err := p.userBrief(ctx, event.GetUserId())
	if err != nil {
		return err
	}
	update := &UpdateEvent{Type: event.GetType(), User: user, Date: event.GetOccurredAt()}
	switch event.GetType() {
	case "friend_added", "friend_removed":
	case "group_joined", "group_removed":
		if event.GetGroupId() <= 0 {
			return ErrInvalidTrigger
		}
		title, err := p.store.GroupName(ctx, event.GetGroupId())
		if err != nil {
			return err
		}
		update.Chat = &Chat{ID: event.GetGroupId(), Type: ChatGroup, Title: title}
	default:
		p.result("event", "unknown_type", "type", event.GetType())
		return nil
	}
	dedupKey := fmt.Sprintf("e:%s:%d:%d:%s", event.GetType(), event.GetUserId(), event.GetGroupId(), event.GetOccurredAt())
	return p.queue(ctx, bot.UserID, truncate(dedupKey, 128), &Update{Event: update})
}

func (p *Platform) resolveRequest(ctx context.Context, bot *BotInfo, event *chatbotpb.BotEvent) error {
	if event.GetRequestId() <= 0 {
		return ErrInvalidTrigger
	}
	decision := friend.RequestDecision_REQUEST_ACCEPT
	if bot.Disabled || (event.GetRequestType() == db.RequestTypeGroupInvite && !bot.AllowGroups) {
		decision = friend.RequestDecision_REQUEST_REJECT
	}
	// 回包发往DF共享topic；目标是机器人，DF直接丢弃。申请人的resolved事件由FriendService照常推送。
	request := &friend.RequestMessage{FromKafkaTopic: p.config.ReplyTopic, TargetUserId: bot.UserID}
	switch event.GetRequestType() {
	case db.RequestTypeFriend:
		request.Payload = &friend.RequestMessage_ResolveFriendRequest{ResolveFriendRequest: &friend.ResolveFriendRequest{
			UserId: bot.UserID, RequestId: event.GetRequestId(), Decision: decision,
		}}
	case db.RequestTypeGroupInvite:
		request.Payload = &friend.RequestMessage_ResolveGroupInvitation{ResolveGroupInvitation: &friend.ResolveGroupInvitation{
			UserId: bot.UserID, InvitationId: event.GetRequestId(), Decision: decision,
		}}
	default:
		return ErrInvalidTrigger
	}
	payload, err := mq.MarshalEnvelope(envelope.MessageType_FRIEND_REQUEST, request)
	if err != nil {
		return err
	}
	if err := p.publish(ctx, p.config.FriendTopic, fmt.Sprintf("b:%d", bot.UserID), payload); err != nil {
		return fmt.Errorf("处理机器人关系申请失败: %w", err)
	}
	logger.Sugar().Infow("机器人处理关系申请", "bot_user_id", bot.UserID, "request_id", event.GetRequestId(),
		"request_type", event.GetRequestType(), "decision", decision.String())
	p.result("event", "resolved")
	return nil
}
