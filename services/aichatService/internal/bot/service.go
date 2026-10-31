package bot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"Betterfly2/shared/utils"
	"aichatService/internal/config"
	"aichatService/internal/llm"
	"aichatService/internal/media"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	replyNotConfigured = "AI 助手还没有配置模型，暂时无法回答。"
	replyTextOnly      = "我目前只能理解文字消息。"
	replyTextAndImage  = "我目前只能理解文字和图片消息。"
	replyImageFailed   = "这张图片我暂时看不了，目前支持 JPEG、PNG、GIF、WebP 和 HEIC。"
	imageUnreadable    = "[图片：无法读取]"
	replyFailed        = "抱歉，我暂时无法回答，请稍后再试。"
	replyEmpty         = "我没有想好怎么回答，换个问法试试？"
	replyMinuteLimit   = "你提问得太快了，请过一分钟再试。"
	replyDayLimit      = "今天的提问次数已经用完了，明天再来吧。"
)

// ErrInvalidTrigger 表示触发消息本身不合法，重试也不会成功。
var ErrInvalidTrigger = errors.New("机器人触发消息无效")

var (
	repliesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_aichat_triggers_total",
		Help: "AI助手触发处理结果",
	}, []string{"result"})
	llmSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_aichat_llm_seconds",
		Help:    "模型接口调用耗时",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120},
	}, []string{"result"})
	imagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_aichat_images_total",
		Help: "交给模型的图片读取结果",
	}, []string{"result"})
)

type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

// Publisher 按key发布到Kafka，同一会话的回复进同一分区，保证多段回复按顺序入库。
type Publisher func(ctx context.Context, topic, key string, payload []byte) error

// Images 把对象存储里的图片转成模型可用的data URL。
type Images interface {
	DataURL(ctx context.Context, storagePath string) (string, error)
}

type Service struct {
	config  config.Config
	store   Store
	llm     Completer // 未配置模型时为nil
	images  Images    // 未开启识图时为nil
	publish Publisher
	limiter *limiter
	now     func() time.Time
}

func NewService(cfg config.Config, store Store, completer Completer, images Images, publish Publisher) *Service {
	return &Service{
		config: cfg, store: store, llm: completer, images: images, publish: publish,
		limiter: newLimiter(cfg.UserRatePerMinute, cfg.UserDailyLimit, cfg.Location),
		now:     time.Now,
	}
}

// Handle 处理机器人平台转来的触发：校验触发消息，生成回复，再以机器人身份经StorageService入库。
// 入库响应发往DF共享topic，由任意DF照常投递和推送。重复触发以client_message_id去重。
func (s *Service) Handle(ctx context.Context, trigger *chatbotpb.ChatbotTrigger) error {
	botID := s.config.BotUserID
	if trigger.GetMessageId() <= 0 || trigger.GetFromUserId() <= 0 || trigger.GetToId() <= 0 ||
		(!trigger.GetIsGroup() && trigger.GetToId() != botID) {
		return ErrInvalidTrigger
	}
	if trigger.GetFromUserId() == botID {
		return s.skip("self", trigger)
	}
	if trigger.GetIsGroup() && len(trigger.GetBotUserIds()) > 0 && !slices.Contains(trigger.GetBotUserIds(), botID) {
		return s.skip("not_addressed", trigger)
	}
	message, err := s.store.Message(ctx, trigger.GetMessageId())
	if err != nil {
		return err
	}
	if message == nil || message.FromUserID != trigger.GetFromUserId() || message.ToUserID != trigger.GetToId() || message.IsGroup != trigger.GetIsGroup() {
		return s.skip("mismatch", trigger)
	}
	if message.IsRecalled {
		return s.skip("recalled", trigger)
	}
	// 服务停机或积压后再回答很久以前的提问只会让人困惑；消费组偏移重置后也不会翻出旧消息作答。
	if sentAt, err := time.Parse(time.RFC3339, message.Timestamp); err == nil && s.now().Sub(sentAt) > s.config.MaxTriggerAge {
		return s.skip("stale", trigger)
	}
	if trigger.GetIsGroup() {
		allowed, err := s.store.CanPublishGroup(ctx, trigger.GetToId(), botID)
		if err != nil {
			return err
		}
		if !allowed {
			return s.skip("not_member", trigger)
		}
	}
	if exists, err := s.store.ReplyExists(ctx, botID, clientMessageID(message.MessageID, 0)); err != nil {
		return err
	} else if exists {
		return s.skip("duplicate", trigger)
	}

	reply, result, err := s.compose(ctx, message)
	if err != nil {
		return err
	}
	if reply == "" {
		return s.skip(result, trigger)
	}
	if err := s.publishReply(ctx, message, reply); err != nil {
		return err
	}
	repliesTotal.WithLabelValues(result).Inc()
	return nil
}

func (s *Service) skip(result string, trigger *chatbotpb.ChatbotTrigger) error {
	repliesTotal.WithLabelValues(result).Inc()
	logger.Sugar().Infow("机器人跳过触发", "result", result, "message_id", trigger.GetMessageId(), "from_user_id", trigger.GetFromUserId())
	return nil
}

// compose 返回回复正文与结果标签；回复为空表示不回复。只有ctx被取消时返回错误，交给Kafka重试。
func (s *Service) compose(ctx context.Context, message *db.Message) (string, string, error) {
	switch s.limiter.allow(message.FromUserID, s.now()) {
	case limitSilent:
		return "", "rate_limited", nil
	case limitNotify:
		if s.limiter.isDayLimit(message.FromUserID) {
			return replyDayLimit, "rate_limited", nil
		}
		return replyMinuteLimit, "rate_limited", nil
	}
	if message.MessageType != "text" && !(s.images != nil && isImage(message.MessageType)) {
		if s.images != nil {
			return replyTextAndImage, "unsupported", nil
		}
		return replyTextOnly, "unsupported", nil
	}
	if s.llm == nil {
		return replyNotConfigured, "unconfigured", nil
	}
	prompt, triggerImageFailed, err := s.buildPrompt(ctx, message)
	if err != nil {
		return "", "", err
	}
	if triggerImageFailed && strings.TrimSpace(message.Caption) == "" {
		return replyImageFailed, "image_unreadable", nil
	}
	started := time.Now()
	raw, err := s.llm.Complete(ctx, prompt)
	var statusErr *llm.StatusError
	if err != nil && ctx.Err() == nil && errors.As(err, &statusErr) && statusErr.RejectsInput() && llm.HasImages(prompt) {
		// 模型不接受图片（不支持多模态或图片过大）时退回纯文字再试一次。
		logger.Sugar().Warnw("模型拒绝带图片的请求，改用纯文字重试", "message_id", message.MessageID, "error", err)
		imagesTotal.WithLabelValues("rejected").Inc()
		raw, err = s.llm.Complete(ctx, llm.WithoutImages(prompt, "[图片]"))
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		llmSeconds.WithLabelValues("error").Observe(time.Since(started).Seconds())
		logger.Sugar().Warnw("模型调用失败", "message_id", message.MessageID, "error", err)
		return replyFailed, "llm_error", nil
	}
	llmSeconds.WithLabelValues("ok").Observe(time.Since(started).Seconds())
	if reply := sanitizeReply(raw); reply != "" {
		return reply, "replied", nil
	}
	return replyEmpty, "empty", nil
}

type turn struct {
	role    string
	content string
	image   string // 图片的data URL，没有图片时为空
}

func isImage(messageType string) bool { return messageType == "image" || messageType == "gif" }

// loadImage 读取用户发的图片。只有在对象存储里校验通过的文件才读取；失败时返回false，
// 只有ctx取消时返回错误。
func (s *Service) loadImage(ctx context.Context, message db.Message) (string, bool, error) {
	fileHash, valid := db.FileHashFromContent(message.Content)
	if !valid {
		imagesTotal.WithLabelValues("invalid").Inc()
		return "", false, nil
	}
	storagePath, err := s.store.ImagePath(ctx, fileHash)
	if err == nil && storagePath == "" {
		imagesTotal.WithLabelValues("missing").Inc()
		return "", false, nil
	}
	var url string
	if err == nil {
		url, err = s.images.DataURL(ctx, storagePath)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		result := "error"
		if errors.Is(err, media.ErrUnsupported) {
			result = "unsupported"
		} else if errors.Is(err, media.ErrTooLarge) {
			result = "too_large"
		}
		imagesTotal.WithLabelValues(result).Inc()
		logger.Sugar().Infow("读取图片失败", "message_id", message.MessageID, "result", result, "error", err)
		return "", false, nil
	}
	imagesTotal.WithLabelValues("ok").Inc()
	return url, true, nil
}

// buildPrompt 取会话最近的消息作为上下文，按字符预算从新到旧截取；触发消息总在其中。
// 开启识图时最近的几张图片也一并交给模型；第二个返回值表示触发消息本身是读不了的图片。
func (s *Service) buildPrompt(ctx context.Context, message *db.Message) ([]llm.Message, bool, error) {
	botID := s.config.BotUserID
	var history []db.Message
	var err error
	if message.IsGroup {
		history, err = s.store.GroupHistory(ctx, message.ToUserID, message.MessageID, s.config.ContextMessages)
	} else {
		history, err = s.store.DirectHistory(ctx, message.FromUserID, botID, message.MessageID, s.config.ContextMessages)
	}
	if err != nil {
		return nil, false, err
	}
	if len(history) == 0 || history[0].MessageID != message.MessageID {
		history = append([]db.Message{*message}, history...)
	}

	var names map[int64]string
	groupName := ""
	if message.IsGroup {
		ids := make([]int64, 0, len(history))
		for _, item := range history {
			if item.FromUserID != botID {
				ids = append(ids, item.FromUserID)
			}
		}
		if names, err = s.store.UserNames(ctx, ids); err != nil {
			return nil, false, err
		}
		if groupName, err = s.store.GroupName(ctx, message.ToUserID); err != nil {
			return nil, false, err
		}
	}

	var turns []turn // 从新到旧
	budget := s.config.ContextChars
	imagesLeft := s.config.ContextImages
	triggerImageFailed := false
	for _, item := range history {
		if item.IsRecalled {
			continue
		}
		content := renderContent(item)
		image := ""
		if s.images != nil && imagesLeft > 0 && isImage(item.MessageType) && item.FromUserID != botID {
			imagesLeft--
			url, ok, err := s.loadImage(ctx, item)
			if err != nil {
				return nil, false, err
			}
			if ok {
				image = url
			} else {
				content = strings.Replace(content, "[图片]", imageUnreadable, 1)
				triggerImageFailed = triggerImageFailed || item.MessageID == message.MessageID
			}
		}
		role := "assistant"
		if item.FromUserID != botID {
			role = "user"
			if message.IsGroup {
				name := strings.TrimSpace(names[item.FromUserID])
				if name == "" {
					name = fmt.Sprintf("用户%d", item.FromUserID)
				}
				content = name + "：" + content
			}
		}
		size := utf8.RuneCountInString(content)
		if size > budget && len(turns) > 0 {
			break
		}
		budget -= size
		turns = append(turns, turn{role: role, content: content, image: image})
	}

	prompt := []llm.Message{{Role: "system", Content: s.systemPrompt(groupName, message.IsGroup)}}
	for i := len(turns) - 1; i >= 0; i-- {
		current := turns[i]
		if len(prompt) == 1 && current.role == "assistant" {
			continue // 部分兼容接口要求system之后第一条是user
		}
		if prompt[len(prompt)-1].Role != current.role {
			prompt = append(prompt, llm.Message{Role: current.role})
		}
		last := &prompt[len(prompt)-1]
		last.Parts = append(last.Parts, llm.Part{Text: current.content})
		if current.image != "" {
			last.Parts = append(last.Parts, llm.Part{ImageURL: current.image})
		}
	}
	for i := 1; i < len(prompt); i++ {
		prompt[i] = mergeTextParts(prompt[i])
	}
	return prompt, triggerImageFailed, nil
}

// mergeTextParts 把相邻的文字段用换行连起来；没有图片时退回普通字符串content，兼容只支持文字的接口。
func mergeTextParts(message llm.Message) llm.Message {
	var parts []llm.Part
	hasImage := false
	for _, part := range message.Parts {
		if part.ImageURL != "" {
			hasImage = true
			parts = append(parts, part)
			continue
		}
		if n := len(parts); n > 0 && parts[n-1].ImageURL == "" {
			parts[n-1].Text += "\n" + part.Text
			continue
		}
		parts = append(parts, part)
	}
	if !hasImage {
		content := ""
		if len(parts) > 0 {
			content = parts[0].Text
		}
		return llm.Message{Role: message.Role, Content: content}
	}
	return llm.Message{Role: message.Role, Parts: parts}
}

func (s *Service) systemPrompt(groupName string, isGroup bool) string {
	now := s.now().In(s.config.Location)
	prompt := s.config.SystemPrompt + fmt.Sprintf("\n当前时间：%s（%s）。", now.Format("2006-01-02 15:04 Monday"), s.config.Location)
	if isGroup {
		if groupName == "" {
			groupName = "未命名群聊"
		}
		prompt += fmt.Sprintf("\n你正在群聊“%s”中。用户消息以“昵称：”开头；请回答最后一条@你或回复你的消息，不要在回复开头写自己的名字或对方昵称。", groupName)
	}
	return prompt
}

func renderContent(message db.Message) string {
	switch message.MessageType {
	case "text", "link":
		return message.Content
	case "image", "gif":
		if caption := strings.TrimSpace(message.Caption); caption != "" {
			return "[图片] " + caption
		}
		return "[图片]"
	case "audio":
		return "[语音]"
	case "video":
		return "[视频]"
	case "file":
		return "[文件]"
	default:
		return "[消息]"
	}
}

func clientMessageID(triggerID int64, part int) string {
	return fmt.Sprintf("chatbot:%d:%d", triggerID, part)
}

// publishReply 以机器人身份提交入库请求。StorageService按(from_user_id, client_message_id)去重，
// 重试只会得到已有消息；入库响应发往DF共享topic，由任意DF投递给会话成员。
func (s *Service) publishReply(ctx context.Context, message *db.Message, reply string) error {
	botID := s.config.BotUserID
	conversation, key := message.FromUserID, fmt.Sprintf("u:%d", message.FromUserID)
	if message.IsGroup {
		conversation, key = message.ToUserID, fmt.Sprintf("g:%d", message.ToUserID)
	}
	for i, part := range splitReply(reply, db.MaxMessageContentLen, s.config.MaxReplyParts) {
		store := &storage.StoreNewMessage{
			FromUserId: botID, ToUserId: conversation, Content: part, MessageType: "text", IsGroup: message.IsGroup,
			ClientMessageId: clientMessageID(message.MessageID, i), ClientTimestamp: utils.NowTime(),
		}
		if i == 0 && message.IsGroup {
			store.ReplyToMessageId = message.MessageID // 群里引用被@的消息，方便看出回答的是谁
		}
		payload, err := mq.MarshalEnvelope(envelope.MessageType_STORAGE_REQUEST, &storage.RequestMessage{
			FromKafkaTopic: s.config.ReplyTopic, TargetUserId: botID,
			Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: store},
		})
		if err != nil {
			return err
		}
		if err := s.publish(ctx, s.config.StorageTopic, key, payload); err != nil {
			return fmt.Errorf("发布机器人回复失败: %w", err)
		}
	}
	return nil
}
