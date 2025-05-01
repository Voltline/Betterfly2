package consumer

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/handlers"
	"github.com/IBM/sarama"
	"regexp"
)

type KafkaConsumerGroupHandler struct{}

func (h *KafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *KafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// 实现samara的消费处理器协议
func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	sugar := logger.Sugar()

	for msg := range claim.Messages() {
		sugar.Infof("Kafka 收到消息: %s", string(msg.Value))
		// TODO: 或许有风险，需要改造
		match, regErr := regexp.Match("DELETE USER [0-9a-zA-Z.:]+", msg.Value)
		if regErr != nil {
			sugar.Errorf("正则匹配失败：%v", regErr)
			continue
		}

		// 收到关闭连接要求
		if match {
			re := regexp.MustCompile("DELETE USER ([0-9a-zA-Z.:]+)")
			matches := re.FindAllStringSubmatch(string(msg.Value), -1)
			for _, match := range matches[0] {
				sugar.Infof("info of match: %v", match)
			}
			if matches[0][1] != "" {
				handlers.StopClient(matches[0][1])
			}
			continue
		}

		requestMsg, err := handlers.HandleRequestData(msg.Value)
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			continue
		}

		err = handlers.RequestMessageHandler(requestMsg)
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			continue
		}

		session.MarkMessage(msg, "")
	}
	return nil
}
