package consumer

import (
	"Betterfly2/shared/logger"

	"github.com/IBM/sarama"
)

type KafkaConsumerGroupHandler struct{}

func (h *KafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *KafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// ConsumeClaim 实现samara的消费处理器协议
func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	sugar := logger.Sugar()

	for msg := range claim.Messages() {
		// TODO: 还没实现功能
		sugar.Debugf("Kafka 收到消息: %s", string(msg.Value))
	}
	return nil
}
