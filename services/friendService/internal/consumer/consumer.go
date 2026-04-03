package consumer

import (
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/logger"
	"friendService/internal/handler"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type KafkaConsumerGroupHandler struct {
	handler *handler.FriendHandler
}

func (h *KafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *KafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	if h.handler == nil {
		h.handler = handler.NewFriendHandler()
	}

	for msg := range claim.Messages() {
		env := &envelope.Envelope{}
		payload := msg.Value
		if err := proto.Unmarshal(msg.Value, env); err == nil {
			switch env.Type {
			case envelope.MessageType_FRIEND_REQUEST:
				payload = env.Payload
			default:
				logger.Sugar().Debugf("friendService忽略非FRIEND_REQUEST消息: type=%v", env.Type)
				session.MarkMessage(msg, "")
				continue
			}
		}

		if err := h.handler.HandleMessage(session.Context(), payload); err != nil {
			logger.Sugar().Errorf("处理friend消息失败: %v", err)
			continue
		}

		session.MarkMessage(msg, "")
	}
	return nil
}
