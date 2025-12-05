package consumer

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/handlers"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
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

		// 尝试解析为Envelope
		env := &envelope.Envelope{}
		if err := proto.Unmarshal(msg.Value, env); err == nil {
			// 成功解析为Envelope，根据类型处理
			sugar.Debugf("收到Envelope消息: type=%v", env.Type)
			switch env.Type {
			case envelope.MessageType_STORAGE_RESPONSE:
				storageResp := &storage.ResponseMessage{}
				if err := proto.Unmarshal(env.Payload, storageResp); err != nil {
					sugar.Errorf("解析Envelope中的STORAGE_RESPONSE payload失败: %v", err)
					continue
				}
				// 处理storage响应
				if err := h.handleStorageResponse(storageResp); err != nil {
					sugar.Errorf("处理storage响应失败: %v", err)
				}
				session.MarkMessage(msg, "")
				continue
			case envelope.MessageType_DF_REQUEST:
				requestMsg, err := handlers.HandleRequestData(env.Payload)
				if err != nil {
					sugar.Errorf("处理Envelope中的DF_REQUEST payload失败: %v", err)
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
				continue
			case envelope.MessageType_STORAGE_REQUEST:
				// storage请求应该由storage服务处理，这里可能不需要处理
				sugar.Warnf("收到STORAGE_REQUEST类型Envelope，但data forwarding服务不处理，忽略")
				continue
			case envelope.MessageType_DF_RESPONSE:
				// DF_RESPONSE可能由其他服务处理
				sugar.Warnf("收到DF_RESPONSE类型Envelope，但当前消费者不处理，忽略")
				continue
			case envelope.MessageType_TEXT:
				// 文本消息，可能用于降级方案，但已经在前面的正则匹配中处理
				sugar.Debugf("收到TEXT类型Envelope，内容: %s", string(env.Payload))
				continue
			default:
				sugar.Warnf("未知的Envelope类型: %v", env.Type)
				continue
			}
		}

		// 如果不是Envelope，继续旧逻辑
		// 先尝试解析为storage响应消息
		storageResp := &storage.ResponseMessage{}
		if err := proto.Unmarshal(msg.Value, storageResp); err == nil {
			// 成功解析为storage响应，处理这些消息
			sugar.Debugf("收到storage响应: result=%v, target_user=%d", storageResp.Result, storageResp.TargetUserId)

			// 处理storage响应并转发给客户端
			if err := h.handleStorageResponse(storageResp); err != nil {
				sugar.Errorf("处理storage响应失败: %v", err)
			}

			session.MarkMessage(msg, "")
			continue
		}

		// 不是storage响应，尝试解析为data forwarding请求
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

// handleStorageResponse 处理storage服务的响应并转发给客户端
func (h *NewKafkaConsumerGroupHandler) handleStorageResponse(storageResp *storage.ResponseMessage) error {
	sugar := logger.Sugar()

	if h.wsHandler == nil {
		return fmt.Errorf("WebSocket处理器未设置，无法转发响应")
	}

	// 构建data_forwarding响应消息
	var dfResp *pb.ResponseMessage
	var err error

	// 处理没有payload的响应（如更新操作）
	if storageResp.Payload == nil {
		switch storageResp.Result {
		case storage.StorageResult_OK:
			// 操作成功，发送简单确认消息
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Server{
					Server: &pb.Server{
						ServerMsg: "操作成功",
					},
				},
			}
		case storage.StorageResult_RECORD_NOT_EXIST:
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "记录不存在",
					},
				},
			}
		default:
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: fmt.Sprintf("操作失败: %v", storageResp.Result),
					},
				},
			}
		}
		// 发送响应并返回
		if dfResp != nil {
			// 序列化响应消息
			respBytes, err := proto.Marshal(dfResp)
			if err != nil {
				return fmt.Errorf("序列化响应消息失败: %v", err)
			}

			// 发送给目标用户
			targetUserID := strconv.FormatInt(storageResp.TargetUserId, 10)
			err = h.wsHandler.SendMessage(targetUserID, respBytes)
			if err != nil {
				return fmt.Errorf("发送消息给用户 %s 失败: %v", targetUserID, err)
			}

			sugar.Debugf("storage响应已转发给用户: target_user=%d", storageResp.TargetUserId)
		}
		return nil
	}

	switch payload := storageResp.Payload.(type) {
	case *storage.ResponseMessage_StoreMsgRsp:
		// 存储消息响应 - 目前客户端可能不需要，但可以发送确认
		sugar.Debugf("收到消息存储响应: message_id=%d", payload.StoreMsgRsp.GetMessageId())
		// 可以发送简单的确认消息，这里暂时不发送具体响应
		return nil

	case *storage.ResponseMessage_MsgRsp:
		// 单条消息查询响应
		msg := payload.MsgRsp
		sugar.Debugf("收到单条消息查询响应: from=%d to=%d", msg.GetFromUserId(), msg.GetToUserId())

		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_MessageRsp{
				MessageRsp: &pb.MessageRsp{
					FromUserId: msg.GetFromUserId(),
					ToUserId:   msg.GetToUserId(),
					Content:    msg.GetContent(),
					Timestamp:  msg.GetTimestamp(),
					MsgType:    msg.GetMsgType(),
					IsGroup:    msg.GetIsGroup(),
				},
			},
		}

	case *storage.ResponseMessage_SyncMsgsRsp:
		// 同步消息查询响应
		syncMsgs := payload.SyncMsgsRsp
		sugar.Debugf("收到同步消息查询响应: 消息数量=%d", len(syncMsgs.GetMsgs()))

		// 转换为data_forwarding的MessageRsp列表
		var dfMsgs []*pb.MessageRsp
		for _, msg := range syncMsgs.GetMsgs() {
			dfMsgs = append(dfMsgs, &pb.MessageRsp{
				FromUserId: msg.GetFromUserId(),
				ToUserId:   msg.GetToUserId(),
				Content:    msg.GetContent(),
				Timestamp:  msg.GetTimestamp(),
				MsgType:    msg.GetMsgType(),
				IsGroup:    msg.GetIsGroup(),
			})
		}

		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_SyncMsgsRsp{
				SyncMsgsRsp: &pb.SyncMessagesRsp{
					Msgs: dfMsgs,
				},
			},
		}

	case *storage.ResponseMessage_UserInfoRsp:
		// 用户信息查询响应
		userInfo := payload.UserInfoRsp
		sugar.Debugf("收到用户信息查询响应: user_id=%d, account=%s", userInfo.GetUserId(), userInfo.GetAccount())

		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_UserInfo{
				UserInfo: &pb.UserInfo{
					SendToUserId:  storageResp.TargetUserId, // 接收响应的用户ID
					QueryUserName: userInfo.GetName(),       // 查询到的用户名
					UserId:        userInfo.GetUserId(),
					Account:       userInfo.GetAccount(),
					Name:          userInfo.GetName(),
					Avatar:        userInfo.GetAvatar(),
					UpdateTime:    userInfo.GetUpdateTime(),
				},
			},
		}

	default:
		// 未知的响应类型
		sugar.Warnf("未知的storage响应类型: %T", payload)
		return nil
	}

	// 序列化响应消息
	respBytes, err := proto.Marshal(dfResp)
	if err != nil {
		return fmt.Errorf("序列化响应消息失败: %v", err)
	}

	// 发送给目标用户
	targetUserID := strconv.FormatInt(storageResp.TargetUserId, 10)
	err = h.wsHandler.SendMessage(targetUserID, respBytes)
	if err != nil {
		return fmt.Errorf("发送消息给用户 %s 失败: %v", targetUserID, err)
	}

	sugar.Debugf("storage响应已转发给用户: target_user=%d", storageResp.TargetUserId)
	return nil
}
