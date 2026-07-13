package consumer

import (
	callpb "Betterfly2/proto/call"
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	pushpb "Betterfly2/proto/push"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/handlers"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

// Pre-compiled regex patterns for efficient matching
var (
	deleteUserPatternCapture = regexp.MustCompile(`^DELETE USER (\d+) TARGET ([-a-zA-Z0-9]+)$`)
)

// NewKafkaConsumerGroupHandler 新的Kafka消费者处理器
type NewKafkaConsumerGroupHandler struct {
	wsHandler        *handlers.WebSocketHandler
	retryConfig      consumerRetryConfig
	processMessageFn func(*sarama.ConsumerMessage) error
	publishDLQFn     func(string, []byte) error
}

// NewKafkaConsumerGroupHandlerWithHandler 创建带处理器的消费者处理器
func NewKafkaConsumerGroupHandlerWithHandler(wsHandler *handlers.WebSocketHandler) *NewKafkaConsumerGroupHandler {
	return &NewKafkaConsumerGroupHandler{
		wsHandler:   wsHandler,
		retryConfig: loadConsumerRetryConfig(),
	}
}

func (h *NewKafkaConsumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *NewKafkaConsumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

// ConsumeClaim 实现samara的消费处理器协议
func (h *NewKafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		logger.Sugar().Debugw("Kafka收到消息", "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset)
		if err := h.processWithRetry(session.Context(), msg); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			logger.Sugar().Errorw("Kafka消息未确认，等待重新投递", "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset, "error", summarizeError(err))
			return err
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
	switch payload := friendResp.Payload.(type) {
	case *friend.ResponseMessage_RelationshipRequestListRsp:
		dfResp = buildRelationshipRequestListResponse(payload.RelationshipRequestListRsp)
	case *friend.ResponseMessage_RelationshipOperationRsp:
		dfResp = buildRelationshipOperationResponse(payload.RelationshipOperationRsp, friendResp.GetResult())
	case *friend.ResponseMessage_GroupOperationRsp:
		if payload.GroupOperationRsp.GetOperation() == "kick_group_member" || payload.GroupOperationRsp.GetOperation() == "update_group_member_role" {
			dfResp = buildGroupMemberOperationResponse(payload.GroupOperationRsp, friendResp.GetResult())
		}
	}
	if dfResp != nil {
		goto marshalFriendResponse
	}
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

marshalFriendResponse:
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

func buildRelationshipRequestListResponse(list *friend.RelationshipRequestListRsp) *pb.ResponseMessage {
	requests := make([]*pb.RelationshipRequestInfo, 0, len(list.GetRequests()))
	for _, request := range list.GetRequests() {
		requests = append(requests, buildRelationshipRequestInfo(request))
	}
	return &pb.ResponseMessage{Payload: &pb.ResponseMessage_RelationshipRequestListRsp{
		RelationshipRequestListRsp: &pb.RelationshipRequestListRsp{Requests: requests},
	}}
}

func buildRelationshipOperationResponse(operation *friend.RelationshipOperationRsp, result friend.FriendResult) *pb.ResponseMessage {
	return &pb.ResponseMessage{Payload: &pb.ResponseMessage_RelationshipOperationRsp{
		RelationshipOperationRsp: &pb.RelationshipOperationRsp{
			Operation: operation.GetOperation(), Result: result.String(), Request: buildRelationshipRequestInfo(operation.GetRequest()),
		},
	}}
}

func buildRelationshipRequestInfo(request *friend.RelationshipRequestInfo) *pb.RelationshipRequestInfo {
	if request == nil {
		return nil
	}
	return &pb.RelationshipRequestInfo{
		RequestId: request.GetRequestId(), RequestType: request.GetRequestType(),
		RequesterUserId: request.GetRequesterUserId(), RequesterName: request.GetRequesterName(), RequesterAvatar: request.GetRequesterAvatar(),
		TargetUserId: request.GetTargetUserId(), TargetName: request.GetTargetName(), TargetAvatar: request.GetTargetAvatar(),
		GroupId: request.GetGroupId(), GroupName: request.GetGroupName(), GroupAvatar: request.GetGroupAvatar(),
		Message: request.GetMessage(), Status: request.GetStatus(), CreatedAt: request.GetCreatedAt(), ExpiresAt: request.GetExpiresAt(),
		ResolvedAt: request.GetResolvedAt(), ResolvedBy: request.GetResolvedBy(),
	}
}

func buildGroupMemberOperationResponse(operation *friend.GroupOperationRsp, result friend.FriendResult) *pb.ResponseMessage {
	return &pb.ResponseMessage{Payload: &pb.ResponseMessage_GroupMemberOperationRsp{
		GroupMemberOperationRsp: &pb.GroupMemberOperationRsp{
			Operation: operation.GetOperation(), Result: result.String(), GroupId: operation.GetGroupId(),
			UserId: operation.GetUserId(), Role: operation.GetRole(), UpdateTime: operation.GetUpdateTime(),
		},
	}}
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
				return permanentError("GroupPostBatchDelivery内容不完整")
			}
			return h.deliverGroupPostToUsers(groupBatchDelivery.GetPost(), groupBatchDelivery.GetTargetUserIds())
		case *pb.DFInternalDelivery_GroupPostDelivery:
			groupDelivery := delivery.GroupPostDelivery
			if groupDelivery.GetPost() == nil || groupDelivery.GetTargetUserId() <= 0 {
				return permanentError("GroupPostDelivery内容不完整")
			}
			return h.deliverGroupPostToUsers(groupDelivery.GetPost(), []int64{groupDelivery.GetTargetUserId()})
		}
	}

	// 兼容旧格式：历史版本直接把 GroupPostDelivery 作为 DF_RESPONSE payload。
	groupDelivery := &pb.GroupPostDelivery{}
	if err := proto.Unmarshal(payload, groupDelivery); err != nil {
		return permanentError("解析GroupPostDelivery失败: %v", err)
	}
	if groupDelivery.GetPost() == nil || groupDelivery.GetTargetUserId() <= 0 {
		return permanentError("GroupPostDelivery内容不完整")
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
		case storage.StorageResult_FORBIDDEN:
			dfResp = &pb.ResponseMessage{
				Payload: &pb.ResponseMessage_Warn{
					Warn: &pb.Warn{WarningMessage: "无权访问该资源"},
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
		storeRsp := payload.StoreMsgRsp
		sugar.Debugf("收到消息存储响应: message_id=%d client_message_id=%s created=%t", storeRsp.GetMessageId(), storeRsp.GetClientMessageId(), storeRsp.GetCreated())
		if err := handlers.CompletePostIdempotency(context.Background(), storeRsp.GetFromUserId(), storeRsp.GetClientMessageId(), storeRsp.GetMessageId()); err != nil {
			sugar.Errorf("更新消息幂等ACK缓存失败: message_id=%d err=%v", storeRsp.GetMessageId(), err)
		}
		if storeRsp.GetCreated() {
			post := &pb.Post{
				FromId:          storeRsp.GetFromUserId(),
				ToId:            storeRsp.GetToUserId(),
				Msg:             storeRsp.GetContent(),
				MsgType:         storeRsp.GetMessageType(),
				IsGroup:         storeRsp.GetIsGroup(),
				RealFileName:    storeRsp.GetRealFileName(),
				Timestamp:       storeRsp.GetClientTimestamp(),
				ClientMessageId: storeRsp.GetClientMessageId(),
			}
			if err := handlers.DeliverStoredPost(storeRsp.GetMessageId(), post); err != nil {
				sugar.Errorf("存储成功后的消息投递失败: message_id=%d err=%v", storeRsp.GetMessageId(), err)
			}
		}
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
				MessageId:       storeMsgRsp.GetMessageId(),
				ClientMessageId: storeMsgRsp.GetClientMessageId(),
			},
		},
	}
}
