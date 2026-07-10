package consumer

import (
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
	"errors"
	pushservice "pushService/internal/push"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type Handler struct {
	service *pushservice.Service
}

func NewHandler(service *pushservice.Service) *Handler { return &Handler{service: service} }

func (h *Handler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *Handler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *Handler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		env := &envelope.Envelope{}
		if err := proto.Unmarshal(message.Value, env); err != nil {
			logger.Sugar().Warnf("pushService忽略无法解析的Envelope: %v", err)
			session.MarkMessage(message, "")
			continue
		}
		if env.GetType() != envelope.MessageType_PUSH_REQUEST {
			logger.Sugar().Debugf("pushService忽略非PUSH_REQUEST消息: %v", env.GetType())
			session.MarkMessage(message, "")
			continue
		}
		request := &pushpb.RequestMessage{}
		if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
			logger.Sugar().Warnf("pushService无法解析请求: %v", err)
			session.MarkMessage(message, "")
			continue
		}
		if err := h.service.Handle(session.Context(), request); err != nil {
			logger.Sugar().Errorf("pushService处理请求失败: %v", err)
			if errors.Is(err, pushservice.ErrInvalidRequest) {
				session.MarkMessage(message, "")
			}
			continue
		}
		session.MarkMessage(message, "")
	}
	return nil
}
