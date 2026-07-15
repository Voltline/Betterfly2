package consumer

import (
	"context"
	"errors"

	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	pushservice "pushService/internal/push"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type pushHandler interface {
	Handle(context.Context, *pushpb.RequestMessage) error
}

type Handler struct {
	service  pushHandler
	reliable *kafkaconsumer.Handler
	publish  kafkaconsumer.DLQPublisher
}

func NewHandler(service pushHandler, publish ...kafkaconsumer.DLQPublisher) *Handler {
	handler := &Handler{service: service}
	if len(publish) > 0 {
		handler.publish = publish[0]
	}
	return handler
}

func (h *Handler) Setup(session sarama.ConsumerGroupSession) error {
	h.initialize()
	return h.reliable.Setup(session)
}

func (h *Handler) Cleanup(session sarama.ConsumerGroupSession) error {
	h.initialize()
	return h.reliable.Cleanup(session)
}

func (h *Handler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	h.initialize()
	return h.reliable.ConsumeClaim(session, claim)
}

func (h *Handler) initialize() {
	if h.reliable != nil {
		return
	}
	h.reliable = kafkaconsumer.New(
		kafkaconsumer.LoadConfig("push", "PUSH", "push-service-dlq"),
		h.process,
		h.publish,
	)
}

func (h *Handler) process(ctx context.Context, message *sarama.ConsumerMessage) kafkaconsumer.Result {
	if h.service == nil {
		return kafkaconsumer.Transientf("push service is not configured")
	}
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(message.Value, env); err != nil {
		return kafkaconsumer.Permanentf("decode push envelope: %v", err)
	}
	if env.GetType() != envelope.MessageType_PUSH_REQUEST {
		return kafkaconsumer.Permanentf("unexpected push envelope type: %s", env.GetType())
	}
	request := &pushpb.RequestMessage{}
	if len(env.GetPayload()) == 0 {
		return kafkaconsumer.Permanentf("empty push request payload")
	}
	if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
		return kafkaconsumer.Permanentf("decode push request: %v", err)
	}
	if request.GetPayload() == nil {
		return kafkaconsumer.Permanentf("push request has no payload")
	}
	if err := h.service.Handle(ctx, request); err != nil {
		if errors.Is(err, pushservice.ErrInvalidRequest) {
			return kafkaconsumer.Permanent(err)
		}
		return kafkaconsumer.Transient(err)
	}
	return kafkaconsumer.Success()
}
