package consumer

import (
	"context"
	"errors"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/kafkaconsumer"
	"chatbotService/internal/platform"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type router interface {
	HandleTrigger(context.Context, *chatbotpb.ChatbotTrigger) error
	HandleEvent(context.Context, *chatbotpb.BotEvent) error
}

// Handler 消费DF发来的机器人触发与关系事件；同一分区按顺序处理，同一会话的消息按顺序交给机器人。
type Handler struct {
	*kafkaconsumer.Handler
	service router
}

func NewHandler(service router, publish kafkaconsumer.DLQPublisher) *Handler {
	handler := &Handler{service: service}
	handler.Handler = kafkaconsumer.New(kafkaconsumer.LoadConfig("chatbot", "CHATBOT", "chatbot-service-dlq"), handler.process, publish)
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
	var err error
	switch payload := request.GetPayload().(type) {
	case *chatbotpb.RequestMessage_Trigger:
		err = h.service.HandleTrigger(ctx, payload.Trigger)
	case *chatbotpb.RequestMessage_Event:
		err = h.service.HandleEvent(ctx, payload.Event)
	default:
		return kafkaconsumer.Permanentf("chatbot request has no payload")
	}
	if err != nil {
		if errors.Is(err, platform.ErrInvalidTrigger) {
			return kafkaconsumer.Permanent(err)
		}
		return kafkaconsumer.Transient(err)
	}
	return kafkaconsumer.Success()
}
