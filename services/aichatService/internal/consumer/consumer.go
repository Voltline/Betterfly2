package consumer

import (
	"context"
	"errors"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/kafkaconsumer"
	"aichatService/internal/bot"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type triggerHandler interface {
	Handle(context.Context, *chatbotpb.ChatbotTrigger) error
}

// Handler 消费机器人平台转来的触发；同一分区按顺序处理，同一会话的提问依次作答。
type Handler struct {
	*kafkaconsumer.Handler
	service triggerHandler
}

func NewHandler(service triggerHandler, publish kafkaconsumer.DLQPublisher) *Handler {
	handler := &Handler{service: service}
	handler.Handler = kafkaconsumer.New(kafkaconsumer.LoadConfig("aichat", "AICHAT", "aichat-service-dlq"), handler.process, publish)
	return handler
}

func (h *Handler) process(ctx context.Context, message *sarama.ConsumerMessage) kafkaconsumer.Result {
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(message.Value, env); err != nil {
		return kafkaconsumer.Permanentf("decode chatbot envelope: %v", err)
	}
	if env.GetType() != envelope.MessageType_CHATBOT_REQUEST {
		return kafkaconsumer.Permanentf("unexpected chatbot envelope type: %s", env.GetType())
	}
	request := &chatbotpb.RequestMessage{}
	if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
		return kafkaconsumer.Permanentf("decode chatbot request: %v", err)
	}
	trigger := request.GetTrigger()
	if trigger == nil {
		// 好友申请、入群等事件由机器人平台处理，AI助手不需要。
		return kafkaconsumer.Success()
	}
	if err := h.service.Handle(ctx, trigger); err != nil {
		if errors.Is(err, bot.ErrInvalidTrigger) {
			return kafkaconsumer.Permanent(err)
		}
		return kafkaconsumer.Transient(err)
	}
	return kafkaconsumer.Success()
}
