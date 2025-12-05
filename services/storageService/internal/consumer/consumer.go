package consumer

import (
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/logger"
	"storageService/internal/handler"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
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

		// 尝试解析为Envelope
		env := &envelope.Envelope{}
		payload := msg.Value
		if err := proto.Unmarshal(msg.Value, env); err == nil {
			// 成功解析为Envelope，根据类型处理
			sugar.Debugf("收到Envelope消息: type=%v", env.Type)
			switch env.Type {
			case envelope.MessageType_STORAGE_REQUEST:
				payload = env.Payload
				sugar.Debugf("提取STORAGE_REQUEST payload，长度: %d", len(payload))
			case envelope.MessageType_DF_REQUEST:
				// 可能是离线消息转发，尝试处理payload作为storage请求
				sugar.Debugf("收到DF_REQUEST类型Envelope，尝试处理payload作为storage请求")
				payload = env.Payload
			case envelope.MessageType_TEXT:
				sugar.Debugf("收到TEXT类型Envelope，内容: %s", string(env.Payload))
				// 文本消息，可能不需要处理
				continue
			default:
				sugar.Warnf("未知的Envelope类型: %v，跳过", env.Type)
				continue
			}
		} else {
			sugar.Debugf("消息不是Envelope格式，按原始消息处理")
		}

		// 处理消息
		err := h.handler.HandleMessage(session.Context(), payload)
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
