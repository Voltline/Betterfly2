package consumer

import (
	"context"
	"errors"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	callservice "callService/internal/call"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type callHandler interface {
	Handle(context.Context, *callpb.InternalRequest) error
	HandlePushResult(context.Context, *pushpb.VoIPPushResult) error
}

type Handler struct {
	service  callHandler
	reliable *kafkaconsumer.Handler
	publish  kafkaconsumer.DLQPublisher
}

func NewHandler(service callHandler, publish ...kafkaconsumer.DLQPublisher) *Handler {
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
		kafkaconsumer.LoadConfig("call", "CALL", "call-service-dlq"),
		h.process,
		h.publish,
	)
}

func (h *Handler) process(ctx context.Context, message *sarama.ConsumerMessage) kafkaconsumer.Result {
	if h.service == nil {
		return kafkaconsumer.Transientf("call service is not configured")
	}
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(message.Value, env); err != nil {
		return kafkaconsumer.Permanentf("decode call envelope: %v", err)
	}
	switch env.GetType() {
	case envelope.MessageType_CALL_REQUEST:
		request := &callpb.InternalRequest{}
		if len(env.GetPayload()) == 0 {
			return kafkaconsumer.Permanentf("empty call request payload")
		}
		if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
			return kafkaconsumer.Permanentf("decode call request: %v", err)
		}
		if request.GetRequest() == nil || request.GetUserId() <= 0 || request.GetFromKafkaTopic() == "" {
			return kafkaconsumer.Permanentf("incomplete call request")
		}
		if err := h.service.Handle(ctx, request); err != nil {
			if errors.Is(err, callservice.ErrInvalidInput) {
				return kafkaconsumer.Permanent(err)
			}
			return kafkaconsumer.Transient(err)
		}
	case envelope.MessageType_PUSH_RESPONSE:
		response := &pushpb.ResponseMessage{}
		if len(env.GetPayload()) == 0 {
			return kafkaconsumer.Permanentf("empty push response payload")
		}
		if err := proto.Unmarshal(env.GetPayload(), response); err != nil {
			return kafkaconsumer.Permanentf("decode push response: %v", err)
		}
		if response.GetVoipResult() == nil {
			return kafkaconsumer.Permanentf("push response has no VoIP result")
		}
		if err := h.service.HandlePushResult(ctx, response.GetVoipResult()); err != nil {
			if errors.Is(err, callservice.ErrInvalidInput) {
				return kafkaconsumer.Permanent(err)
			}
			return kafkaconsumer.Transient(err)
		}
	default:
		return kafkaconsumer.Permanentf("unexpected call envelope type: %s", env.GetType())
	}
	return kafkaconsumer.Success()
}
