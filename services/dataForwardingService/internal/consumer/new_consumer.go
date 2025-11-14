package consumer

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/handlers"
	"os"
	"regexp"

	"github.com/IBM/sarama"
)

// NewKafkaConsumerGroupHandler 新的Kafka消费者处理器
type NewKafkaConsumerGroupHandler struct {
	wsHandler *handlers.WebSocketHandler
}

// NewKafkaConsumerGroupHandlerWithHandler 创建带处理器的消费者处理器
func NewKafkaConsumerGroupHandlerWithHandler(wsHandler *handlers.WebSocketHandler) *NewKafkaConsumerGroupHandler {
	return &NewKafkaConsumerGroupHandler{
		wsHandler: wsHandler,
	}
}

func (h *NewKafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *NewKafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// ConsumeClaim 实现samara的消费处理器协议
func (h *NewKafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	sugar := logger.Sugar()

	for msg := range claim.Messages() {
		sugar.Debugf("Kafka 收到消息 - Topic: %s, Partition: %d, Offset: %d", msg.Topic, msg.Partition, msg.Offset)

		// 检查是否为关闭连接请求（Kafka降级方案）
		match, regErr := regexp.Match("DELETE USER \\d+ TARGET [a-zA-Z0-9]+", msg.Value)
		if regErr != nil {
			sugar.Errorf("正则匹配失败：%v", regErr)
			continue
		}

		// 收到关闭连接要求（降级方案）
		if match {
			re := regexp.MustCompile("DELETE USER (\\d+) TARGET ([a-zA-Z0-9]+)")
			matches := re.FindAllStringSubmatch(string(msg.Value), -1)
			if len(matches) > 0 && len(matches[0]) > 2 {
				userID := matches[0][1]
				targetContainerID := matches[0][2]

				// 获取容器标识符（使用HOSTNAME作为唯一标识）
				currentContainerID := os.Getenv("HOSTNAME")
				if currentContainerID == "" {
					currentContainerID = "local"
				}

				// 只有目标容器才处理踢出消息
				if targetContainerID == currentContainerID {
					sugar.Infof("收到Kafka降级踢出消息，执行强制登出: 用户 %s", userID)

					// 使用传入的WebSocket处理器
					if h.wsHandler != nil {
						h.wsHandler.StopClient(userID)
						sugar.Debugf("降级踢出操作完成: 用户 %s", userID)
					} else {
						sugar.Errorf("WebSocket处理器未设置，无法踢出用户: %s", userID)
					}
				} else {
					sugar.Debugf("收到踢出消息但非本容器目标，忽略: 用户 %s, 目标容器: %s, 当前容器: %s",
						userID, targetContainerID, currentContainerID)
				}
			}
			continue
		}

		requestMsg, err := handlers.HandleRequestData(msg.Value)
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			continue
		}

		if requestMsg.GetPost() == nil {
			sugar.Errorln("消费者收到非Post报文")
			continue
		}

		err = handlers.InplaceHandlePostMessage(requestMsg)
		if err != nil {
			sugar.Errorf("处理消息失败: %v", err)
			continue
		}

		session.MarkMessage(msg, "")
	}
	return nil
}
