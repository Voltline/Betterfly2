package consumer

import (
	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/logger"
	callservice "callService/internal/call"

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
		if env.GetType() != envelope.MessageType_CALL_REQUEST {
			logger.Sugar().Debugf("callService忽略非CALL_REQUEST消息: %v", env.GetType())
			session.MarkMessage(message, "")
			continue
		}

		request := &callpb.InternalRequest{}
		if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
			logger.Sugar().Warnf("callService无法解析请求: %v", err)
			session.MarkMessage(message, "")
			continue
		}
		if err := h.service.Handle(session.Context(), request); err != nil {
			logger.Sugar().Errorf("callService处理请求失败: %v", err)
			continue
		}
		session.MarkMessage(message, "")
	}
	return nil
}
