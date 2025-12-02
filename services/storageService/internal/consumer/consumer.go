package consumer

import (
	"Betterfly2/shared/logger"
	"storageService/internal/handler"

	"github.com/IBM/sarama"
)

type KafkaConsumerGroupHandler struct {
	handler *handler.StorageHandler
}

func (h *KafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *KafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// ConsumeClaim 实现samara的消费处理器协议
func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	sugar := logger.Sugar()

	// 初始化处理器
	if h.handler == nil {
		h.handler = handler.NewStorageHandler()
	}

	for msg := range claim.Messages() {
		sugar.Debugf("Kafka 收到消息, topic: %s, partition: %d, offset: %d",
			msg.Topic, msg.Partition, msg.Offset)

		// 处理消息
		err := h.handler.HandleMessage(session.Context(), msg.Value)
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			// 继续处理下一条消息，不终止消费循环
			continue
		}

		// 标记消息已消费
		session.MarkMessage(msg, "")
	}
	return nil
}
