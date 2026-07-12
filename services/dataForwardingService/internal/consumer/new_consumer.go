package consumer

import (
	callpb "Betterfly2/proto/call"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	pushpb "Betterfly2/proto/push"
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

// Pre-compiled regex patterns for efficient matching
var (
	deleteUserPatternMatch   = regexp.MustCompile(`DELETE USER \d+ TARGET [-a-zA-Z0-9]+`)
	deleteUserPatternCapture = regexp.MustCompile(`DELETE USER (\d+) TARGET ([-a-zA-Z0-9]+)`)
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

		// 检查是否为关闭连接请求（Kafka降级方案），使用预编译的正则表达式
		if deleteUserPatternMatch.Match(msg.Value) {
			matches := deleteUserPatternCapture.FindStringSubmatch(string(msg.Value))
			if len(matches) > 2 {
				userID := matches[1]
				targetContainerID := matches[2]

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
			case envelope.MessageType_FRIEND_RESPONSE:
				friendResp := &friend.ResponseMessage{}
				if err := proto.Unmarshal(env.Payload, friendResp); err != nil {
					sugar.Errorf("解析Envelope中的FRIEND_RESPONSE payload失败: %v", err)
					continue
				}
				if err := h.handleFriendResponse(friendResp); err != nil {
					sugar.Errorf("处理friend响应失败: %v", err)
				}
				session.MarkMessage(msg, "")
				continue
			case envelope.MessageType_CALL_RESPONSE:
				delivery := &callpb.Delivery{}
				if err := proto.Unmarshal(env.Payload, delivery); err != nil {
					sugar.Errorf("解析Envelope中的CALL_RESPONSE payload失败: %v", err)
					continue
				}
				if err := h.handleCallDelivery(delivery); err != nil {
					sugar.Errorf("处理call响应失败: %v", err)
				}
				session.MarkMessage(msg, "")
				continue
			case envelope.MessageType_PUSH_RESPONSE:
				response := &pushpb.ResponseMessage{}
				if err := proto.Unmarshal(env.Payload, response); err != nil {
					sugar.Errorf("解析Envelope中的PUSH_RESPONSE payload失败: %v", err)
					continue
				}
				if err := h.handlePushResponse(response); err != nil {
					sugar.Errorf("处理push响应失败: %v", err)
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
				if err := h.handleDFResponse(env.Payload); err != nil {
					sugar.Errorf("处理DF响应失败: %v", err)
				}
				session.MarkMessage(msg, "")
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

func (h *NewKafkaConsumerGroupHandler) handleCallDelivery(delivery *callpb.Delivery) error {
	if h.wsHandler == nil {
		return fmt.Errorf("WebSocket处理器未设置，无法转发通话事件")
	}
	if delivery.GetTargetUserId() <= 0 || delivery.GetEvent() == nil {
		return fmt.Errorf("通话投递报文不完整")
	}
	response := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_CallEvent{CallEvent: delivery.GetEvent()},
	}
	payload, err := proto.Marshal(response)
	if err != nil {
		return fmt.Errorf("序列化通话事件失败: %w", err)
	}
	return h.wsHandler.SendMessage(strconv.FormatInt(delivery.GetTargetUserId(), 10), payload)
}

func (h *NewKafkaConsumerGroupHandler) handlePushResponse(response *pushpb.ResponseMessage) error {
	if h.wsHandler == nil {
		return fmt.Errorf("WebSocket处理器未设置，无法转发推送响应")
	}
	delivery := response.GetClientDelivery()
	if delivery == nil || delivery.GetTargetUserId() <= 0 || delivery.GetEvent() == nil {
		return fmt.Errorf("推送响应不是有效的客户端投递报文")
	}
	dfResponse := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_PushEvent{PushEvent: delivery.GetEvent()},
	}
	payload, err := proto.Marshal(dfResponse)
	if err != nil {
		return fmt.Errorf("序列化推送响应失败: %w", err)
	}
	return h.wsHandler.SendMessage(strconv.FormatInt(delivery.GetTargetUserId(), 10), payload)
}

func (h *NewKafkaConsumerGroupHandler) handleFriendResponse(friendResp *friend.ResponseMessage) error {
	if h.wsHandler == nil {
		return fmt.Errorf("WebSocket处理器未设置，无法转发好友响应")
	}

	var dfResp *pb.ResponseMessage
	switch friendResp.Result {
	case friend.FriendResult_FRIEND_OK:
		switch payload := friendResp.Payload.(type) {
		case *friend.ResponseMessage_FriendListRsp:
			dfResp = buildContactListResponse(payload.FriendListRsp, friendResp.GetTargetUserId())
		case *friend.ResponseMessage_FriendOperationRsp:
			dfResp = buildFriendOperationResponse(payload.FriendOperationRsp, "操作成功")
		case *friend.ResponseMessage_GroupInfoRsp:
			dfResp = buildGroupInfoResponse(payload.GroupInfoRsp)
		case *friend.ResponseMessage_GroupOperationRsp:
			dfResp = buildGroupOperationResponse(payload.GroupOperationRsp, "操作成功")
		case *friend.ResponseMessage_GroupMemberListRsp:
			dfResp = buildGroupMembersResponse(payload.GroupMemberListRsp)
		case *friend.ResponseMessage_JoinedGroupListRsp:
			dfResp = buildJoinedGroupsResponse(payload.JoinedGroupListRsp)
		default:
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Server{
					Server: &pb.Server{
						ServerMsg: "添加好友成功",
					},
				},
			}
		}
	case friend.FriendResult_ALREADY_FRIEND:
		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Server{
				Server: &pb.Server{
					ServerMsg: "你们已经是好友了",
				},
			},
		}
	case friend.FriendResult_RECORD_NOT_EXIST:
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_FriendOperationRsp); ok {
			dfResp = buildFriendOperationResponse(payload.FriendOperationRsp, "记录不存在")
		} else if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupOperationRsp); ok {
			dfResp = buildGroupOperationResponse(payload.GroupOperationRsp, "记录不存在")
		} else if _, ok := friendResp.Payload.(*friend.ResponseMessage_GroupInfoRsp); ok {
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "群组不存在或你不在该群中",
					},
				},
			}
		} else if _, ok := friendResp.Payload.(*friend.ResponseMessage_GroupMemberListRsp); ok {
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "群组不存在或你不在该群中",
					},
				},
			}
		} else {
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "目标用户不存在",
					},
				},
			}
		}
	case friend.FriendResult_INVALID_ARGUMENT:
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_FriendOperationRsp); ok {
			dfResp = buildFriendOperationResponse(payload.FriendOperationRsp, "好友请求不合法")
		} else if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupOperationRsp); ok {
			dfResp = buildGroupOperationResponse(payload.GroupOperationRsp, "群组请求不合法")
		} else {
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "添加好友请求不合法",
					},
				},
			}
		}
	case friend.FriendResult_ALREADY_EXIST:
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupOperationRsp); ok {
			dfResp = buildGroupOperationResponse(payload.GroupOperationRsp, "已存在")
		} else {
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{
						WarningMessage: "目标已存在",
					},
				},
			}
		}
	default:
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_FriendListRsp); ok {
			dfResp = buildContactListResponse(payload.FriendListRsp, friendResp.GetTargetUserId())
			break
		}
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_FriendOperationRsp); ok {
			dfResp = buildFriendOperationResponse(payload.FriendOperationRsp, "好友服务处理失败")
			break
		}
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupInfoRsp); ok {
			dfResp = buildGroupInfoResponse(payload.GroupInfoRsp)
			break
		}
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupMemberListRsp); ok {
			dfResp = buildGroupMembersResponse(payload.GroupMemberListRsp)
			break
		}
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_JoinedGroupListRsp); ok {
			dfResp = buildJoinedGroupsResponse(payload.JoinedGroupListRsp)
			break
		}
		if payload, ok := friendResp.Payload.(*friend.ResponseMessage_GroupOperationRsp); ok {
			dfResp = buildGroupOperationResponse(payload.GroupOperationRsp, "群组服务处理失败")
			break
		}
		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Warn{
				Warn: &pb.Warn{
					WarningMessage: "好友服务处理失败",
				},
			},
		}
	}

	respBytes, err := proto.Marshal(dfResp)
	if err != nil {
		return fmt.Errorf("序列化好友响应消息失败: %v", err)
	}

	targetUserID := strconv.FormatInt(friendResp.TargetUserId, 10)
	if err := h.wsHandler.SendMessage(targetUserID, respBytes); err != nil {
		return fmt.Errorf("发送好友响应给用户 %s 失败: %v", targetUserID, err)
	}
	return nil
}

func buildGroupInfoResponse(groupInfo *friend.GroupInfoRsp) *pb.ResponseMessage {
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_GroupInfo{
			GroupInfo: &pb.GroupInfo{
				ClientNeedSave: groupInfo.GetClientNeedSave(),
				QueryGroupId:   groupInfo.GetGroupId(),
				QueryGroupName: groupInfo.GetGroupName(),
				Avatar:         groupInfo.GetAvatar(),
			},
		},
	}
}

func buildGroupMembersResponse(groupMembers *friend.GroupMemberListRsp) *pb.ResponseMessage {
	var members []*pb.GroupMemberInfo
	for _, member := range groupMembers.GetMembers() {
		members = append(members, &pb.GroupMemberInfo{
			UserId:     member.GetUserId(),
			Account:    member.GetAccount(),
			Name:       member.GetName(),
			Avatar:     member.GetAvatar(),
			Role:       member.GetRole(),
			UpdateTime: member.GetUpdateTime(),
		})
	}

	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_GroupMembersRsp{
			GroupMembersRsp: &pb.GroupMembersRsp{
				GroupId: groupMembers.GetGroupId(),
				Members: members,
			},
		},
	}
}

func buildJoinedGroupsResponse(groupList *friend.JoinedGroupListRsp) *pb.ResponseMessage {
	var groups []*pb.JoinedGroupInfo
	for _, group := range groupList.GetGroups() {
		groups = append(groups, &pb.JoinedGroupInfo{
			GroupId:     group.GetGroupId(),
			GroupName:   group.GetGroupName(),
			Avatar:      group.GetAvatar(),
			OwnerUserId: group.GetOwnerUserId(),
			UpdateTime:  group.GetUpdateTime(),
		})
	}

	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_JoinedGroupsRsp{
			JoinedGroupsRsp: &pb.JoinedGroupsRsp{
				Groups: groups,
			},
		},
	}
}

func buildContactListResponse(friendList *friend.FriendListRsp, targetUserID int64) *pb.ResponseMessage {
	var contacts []*pb.ContactInfo
	for _, contact := range friendList.GetContacts() {
		contacts = append(contacts, &pb.ContactInfo{
			UserId:     contact.GetUserId(),
			Account:    contact.GetAccount(),
			Name:       contact.GetName(),
			Avatar:     contact.GetAvatar(),
			Alias:      contact.GetAlias(),
			IsNotify:   contact.GetIsNotify(),
			UpdateTime: contact.GetUpdateTime(),
		})
	}
	contacts = handlers.DecorateMonitorContacts(targetUserID, contacts)
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_ContactListRsp{
			ContactListRsp: &pb.ContactListRsp{
				Contacts: contacts,
			},
		},
	}
}

func buildFriendOperationResponse(operation *friend.FriendOperationRsp, fallback string) *pb.ResponseMessage {
	message := fallback

	switch operation.GetOperation() {
	case "remove_direct_friend":
		if fallback == "操作成功" {
			message = "删除好友成功"
		} else if fallback == "记录不存在" {
			message = "好友关系不存在"
		} else if fallback == "好友请求不合法" {
			message = "删除好友请求不合法"
		}
	case "update_friend_alias":
		if fallback == "操作成功" {
			message = "好友备注更新成功"
		} else if fallback == "记录不存在" {
			message = "好友关系不存在，无法更新备注"
		} else if fallback == "好友请求不合法" {
			message = "好友备注更新请求不合法"
		}
	case "update_friend_notify":
		if fallback == "操作成功" {
			message = "好友通知设置更新成功"
		} else if fallback == "记录不存在" {
			message = "好友关系不存在，无法更新通知设置"
		} else if fallback == "好友请求不合法" {
			message = "好友通知设置请求不合法"
		}
	}

	if fallback == "操作成功" {
		return &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Server{
				Server: &pb.Server{
					ServerMsg: message,
				},
			},
		}
	}

	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Warn{
			Warn: &pb.Warn{
				WarningMessage: message,
			},
		},
	}
}

func buildGroupOperationResponse(operation *friend.GroupOperationRsp, fallback string) *pb.ResponseMessage {
	message := fallback

	switch operation.GetOperation() {
	case "create_group":
		if fallback == "操作成功" {
			message = "创建群组成功"
		} else if fallback == "记录不存在" {
			message = "创建群组失败，用户不存在"
		} else if fallback == "群组请求不合法" {
			message = "创建群组请求不合法"
		} else if fallback == "已存在" {
			message = "群组已存在"
		}
	case "add_group_member":
		if fallback == "操作成功" {
			message = "加入群组成功"
		} else if fallback == "记录不存在" {
			message = "群组不存在或用户不存在"
		} else if fallback == "群组请求不合法" {
			message = "加入群组请求不合法"
		} else if fallback == "已存在" {
			message = "你已经在该群中了"
		}
	case "update_group_avatar":
		if fallback == "操作成功" {
			message = "群头像更新成功"
		} else if fallback == "记录不存在" {
			message = "群组不存在或你不在该群中"
		} else if fallback == "群组请求不合法" {
			message = "群头像更新请求不合法"
		}
	case "remove_group_member":
		if fallback == "操作成功" {
			message = "退出群组成功"
		} else if fallback == "记录不存在" {
			message = "群组不存在或你不在该群中"
		} else if fallback == "群组请求不合法" {
			message = "退出群组请求不合法"
		}
	}

	if fallback == "操作成功" {
		return &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Server{
				Server: &pb.Server{
					ServerMsg: message,
				},
			},
		}
	}

	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Warn{
			Warn: &pb.Warn{
				WarningMessage: message,
			},
		},
	}
}

func (h *NewKafkaConsumerGroupHandler) handleDFResponse(payload []byte) error {
	if h.wsHandler == nil {
		return fmt.Errorf("WebSocket处理器未设置，无法转发DF响应")
	}

	internalDelivery := &pb.DFInternalDelivery{}
	if err := proto.Unmarshal(payload, internalDelivery); err == nil {
		switch delivery := internalDelivery.Payload.(type) {
		case *pb.DFInternalDelivery_GroupPostBatchDelivery:
			groupBatchDelivery := delivery.GroupPostBatchDelivery
			if groupBatchDelivery.GetPost() == nil || len(groupBatchDelivery.GetTargetUserIds()) == 0 {
				return fmt.Errorf("GroupPostBatchDelivery内容不完整")
			}
			return h.deliverGroupPostToUsers(groupBatchDelivery.GetPost(), groupBatchDelivery.GetTargetUserIds())
		case *pb.DFInternalDelivery_GroupPostDelivery:
			groupDelivery := delivery.GroupPostDelivery
			if groupDelivery.GetPost() == nil || groupDelivery.GetTargetUserId() <= 0 {
				return fmt.Errorf("GroupPostDelivery内容不完整")
			}
			return h.deliverGroupPostToUsers(groupDelivery.GetPost(), []int64{groupDelivery.GetTargetUserId()})
		}
	}

	// 兼容旧格式：历史版本直接把 GroupPostDelivery 作为 DF_RESPONSE payload。
	groupDelivery := &pb.GroupPostDelivery{}
	if err := proto.Unmarshal(payload, groupDelivery); err != nil {
		return fmt.Errorf("解析GroupPostDelivery失败: %v", err)
	}
	if groupDelivery.GetPost() == nil || groupDelivery.GetTargetUserId() <= 0 {
		return fmt.Errorf("GroupPostDelivery内容不完整")
	}

	return h.deliverGroupPostToUsers(groupDelivery.GetPost(), []int64{groupDelivery.GetTargetUserId()})
}

func (h *NewKafkaConsumerGroupHandler) deliverGroupPostToUsers(post *pb.Post, targetUserIDs []int64) error {
	resp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Post{
			Post: post,
		},
	}
	respBytes, err := proto.Marshal(resp)
	if err != nil {
		return fmt.Errorf("序列化群消息响应失败: %v", err)
	}

	for _, targetUserID := range targetUserIDs {
		targetUserIDStr := strconv.FormatInt(targetUserID, 10)
		if err := h.wsHandler.SendMessage(targetUserIDStr, respBytes); err != nil {
			return fmt.Errorf("转发群消息给用户 %s 失败: %v", targetUserIDStr, err)
		}
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
		sugar.Debugf("收到消息存储响应: message_id=%d", payload.StoreMsgRsp.GetMessageId())
		dfResp = buildPostAckResponse(payload.StoreMsgRsp)

	case *storage.ResponseMessage_MsgRsp:
		// 单条消息查询响应
		msg := payload.MsgRsp
		sugar.Debugf("收到单条消息查询响应: from=%d to=%d", msg.GetFromUserId(), msg.GetToUserId())

		dfResp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_MessageRsp{
				MessageRsp: &pb.MessageRsp{
					MessageId:    msg.GetMessageId(),
					FromUserId:   msg.GetFromUserId(),
					ToUserId:     msg.GetToUserId(),
					Content:      msg.GetContent(),
					Timestamp:    msg.GetTimestamp(),
					MsgType:      msg.GetMsgType(),
					IsGroup:      msg.GetIsGroup(),
					RealFileName: msg.GetRealFileName(),
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
				MessageId:    msg.GetMessageId(),
				FromUserId:   msg.GetFromUserId(),
				ToUserId:     msg.GetToUserId(),
				Content:      msg.GetContent(),
				Timestamp:    msg.GetTimestamp(),
				MsgType:      msg.GetMsgType(),
				IsGroup:      msg.GetIsGroup(),
				RealFileName: msg.GetRealFileName(),
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

func buildPostAckResponse(storeMsgRsp *storage.StoreMsgRsp) *pb.ResponseMessage {
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_PostAckRsp{
			PostAckRsp: &pb.PostAckRsp{
				MessageId: storeMsgRsp.GetMessageId(),
			},
		},
	}
}
