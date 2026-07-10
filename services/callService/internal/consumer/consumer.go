package consumer

import (
	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
	callservice "callService/internal/call"
	"errors"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	service *callservice.Service
}

func NewHandler(service *callservice.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *Handler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *Handler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		env := &envelope.Envelope{}
		if err := proto.Unmarshal(message.Value, env); err != nil {
			logger.Sugar().Warnf("callService忽略无法解析的Envelope: %v", err)
			session.MarkMessage(message, "")
			continue
		}
		switch env.GetType() {
		case envelope.MessageType_CALL_REQUEST:
			request := &callpb.InternalRequest{}
			if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
				logger.Sugar().Warnf("callService无法解析请求: %v", err)
				session.MarkMessage(message, "")
				continue
			}
			if err := h.service.Handle(session.Context(), request); err != nil {
				logger.Sugar().Errorf("callService处理请求失败: %v", err)
				if errors.Is(err, callservice.ErrInvalidInput) {
					session.MarkMessage(message, "")
				}
				continue
			}
		case envelope.MessageType_PUSH_RESPONSE:
			response := &pushpb.ResponseMessage{}
			if err := proto.Unmarshal(env.GetPayload(), response); err != nil {
				logger.Sugar().Warnf("callService无法解析Push响应: %v", err)
				session.MarkMessage(message, "")
				continue
			}
			if err := h.service.HandlePushResult(session.Context(), response.GetVoipResult()); err != nil {
				logger.Sugar().Errorf("callService处理Push响应失败: %v", err)
				if errors.Is(err, callservice.ErrInvalidInput) {
					session.MarkMessage(message, "")
				}
				continue
			}
		default:
			logger.Sugar().Debugf("callService忽略非通话消息: %v", env.GetType())
			session.MarkMessage(message, "")
			continue
		}
		session.MarkMessage(message, "")
	}
	return nil
}
