package handler

import (
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"context"
	"errors"
	"sync"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type friendRequestContext struct {
	handler  *FriendHandler
	request  *friend.RequestMessage
	database *gorm.DB
}

type friendRequestModule func(*dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage])

var (
	friendRequestModules    []friendRequestModule
	friendRequestRouter     *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]
	friendRequestRouterOnce sync.Once
)

func registerFriendRequestModule(register friendRequestModule) {
	friendRequestModules = append(friendRequestModules, register)
}

func getFriendRequestRouter() *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage] {
	friendRequestRouterOnce.Do(func() {
		friendRequestRouter = newFriendRequestRouter()
	})
	return friendRequestRouter
}

func newFriendRequestRouter() *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage] {
	router := dispatch.NewOneofRouter[friendRequestContext, *friend.ResponseMessage]()
	for _, register := range friendRequestModules {
		register(router)
	}
	return router
}

type FriendHandler struct {
	database *gorm.DB
}

func NewFriendHandler() *FriendHandler {
	return &FriendHandler{database: db.DB()}
}

func (h *FriendHandler) requestDatabase() *gorm.DB {
	if h.database != nil {
		return h.database
	}
	return db.DB()
}

func (h *FriendHandler) resolveDatabase(database *gorm.DB) *gorm.DB {
	if database != nil {
		return database
	}
	return h.requestDatabase()
}

func (h *FriendHandler) HandleMessage(ctx context.Context, message []byte) error {
	operationKey, hasOperationKey := kafkaconsumer.OperationKeyFromContext(ctx)
	if !hasOperationKey {
		return errors.New("friend consumer operation key is required")
	}
	req := &friend.RequestMessage{}
	if err := proto.Unmarshal(message, req); err != nil {
		return err
	}

	_, err := db.ExecuteInboxOutbox(ctx, h.requestDatabase(), "friend", operationKey, func(tx *gorm.DB) ([]byte, []db.PendingOutboxEvent, error) {
		resp, dispatchErr := getFriendRequestRouter().Dispatch(friendRequestContext{
			handler: h, request: req, database: tx,
		}, req.Payload)
		if dispatchErr != nil {
			logger.Sugar().Errorw("处理friend请求暂时失败", "operation_key", operationKey, "error", dispatchErr)
			return nil, nil, dispatchErr
		}
		if resp == nil {
			return nil, nil, errors.New("friend dispatch returned nil response")
		}
		encoded, marshalErr := proto.Marshal(resp)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		envelopePayload, marshalErr := mq.MarshalEnvelope(envelope.MessageType_FRIEND_RESPONSE, resp)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		return encoded, []db.PendingOutboxEvent{{
			EventID: db.StableEventID("friend", operationKey, "response"),
			Topic:   req.GetFromKafkaTopic(), Payload: envelopePayload,
		}}, nil
	})
	return err
}

func (h *FriendHandler) handleQueryFriendListWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryFriendList) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}
	database = h.resolveDatabase(database)

	contacts, err := db.GetFriendListWithDB(database, payload.GetUserId())
	if err != nil {
		return nil, err
	}

	var friendContacts []*friend.FriendContact
	for _, contact := range contacts {
		friendContacts = append(friendContacts, &friend.FriendContact{
			UserId:     contact.UserID,
			Account:    contact.Account,
			Name:       contact.Name,
			Avatar:     contact.Avatar,
			Alias:      contact.Alias,
			IsNotify:   contact.IsNotify,
			UpdateTime: contact.UpdateTime,
		})
	}

	return &friend.ResponseMessage{
		Result:       friend.FriendResult_FRIEND_OK,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_FriendListRsp{
			FriendListRsp: &friend.FriendListRsp{
				Contacts: friendContacts,
			},
		},
	}, nil
}

func (h *FriendHandler) handleRemoveDirectFriendWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.RemoveDirectFriend) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 || payload.GetFriendId() <= 0 || payload.GetUserId() == payload.GetFriendId() {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_FriendOperationRsp{
				FriendOperationRsp: &friend.FriendOperationRsp{
					Operation: "remove_direct_friend",
					UserId:    payload.GetUserId(),
					FriendId:  payload.GetFriendId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	found, updateTime, err := db.RemoveDirectFriendPairWithDB(database, payload.GetUserId(), payload.GetFriendId())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if !found {
		result = friend.FriendResult_RECORD_NOT_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_FriendOperationRsp{
			FriendOperationRsp: &friend.FriendOperationRsp{
				Operation:  "remove_direct_friend",
				UserId:     payload.GetUserId(),
				FriendId:   payload.GetFriendId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}

func (h *FriendHandler) handleUpdateFriendAliasWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.UpdateFriendAlias) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 || payload.GetFriendId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_FriendOperationRsp{
				FriendOperationRsp: &friend.FriendOperationRsp{
					Operation: "update_friend_alias",
					UserId:    payload.GetUserId(),
					FriendId:  payload.GetFriendId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	found, updateTime, err := db.UpdateFriendAliasWithDB(database, payload.GetUserId(), payload.GetFriendId(), payload.GetAlias())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if !found {
		result = friend.FriendResult_RECORD_NOT_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_FriendOperationRsp{
			FriendOperationRsp: &friend.FriendOperationRsp{
				Operation:  "update_friend_alias",
				UserId:     payload.GetUserId(),
				FriendId:   payload.GetFriendId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}

func (h *FriendHandler) handleUpdateFriendNotifyWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.UpdateFriendNotify) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 || payload.GetFriendId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_FriendOperationRsp{
				FriendOperationRsp: &friend.FriendOperationRsp{
					Operation: "update_friend_notify",
					UserId:    payload.GetUserId(),
					FriendId:  payload.GetFriendId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	found, updateTime, err := db.UpdateFriendNotifyWithDB(database, payload.GetUserId(), payload.GetFriendId(), payload.GetIsNotify())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if !found {
		result = friend.FriendResult_RECORD_NOT_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_FriendOperationRsp{
			FriendOperationRsp: &friend.FriendOperationRsp{
				Operation:  "update_friend_notify",
				UserId:     payload.GetUserId(),
				FriendId:   payload.GetFriendId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}

func (h *FriendHandler) handleCreateGroupWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.CreateGroup) (*friend.ResponseMessage, error) {
	if payload.GetOwnerUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetGroupName() == "" {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "create_group",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetOwnerUserId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	owner, err := db.GetUserByIDWithDB(database, payload.GetOwnerUserId())
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "create_group",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetOwnerUserId(),
				},
			},
		}, nil
	}

	alreadyExists, updateTime, err := db.CreateGroupWithOwnerWithDB(database, payload.GetOwnerUserId(), payload.GetGroupId(), payload.GetGroupName())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if alreadyExists {
		result = friend.FriendResult_ALREADY_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_GroupOperationRsp{
			GroupOperationRsp: &friend.GroupOperationRsp{
				Operation:  "create_group",
				GroupId:    payload.GetGroupId(),
				UserId:     payload.GetOwnerUserId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}

func (h *FriendHandler) handleQueryGroupWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryGroup) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}
	database = h.resolveDatabase(database)

	group, err := db.GetGroupByIDWithDB(database, payload.GetGroupId())
	if err != nil {
		return nil, err
	}
	if group == nil {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	return &friend.ResponseMessage{
		Result:       friend.FriendResult_FRIEND_OK,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_GroupInfoRsp{
			GroupInfoRsp: &friend.GroupInfoRsp{
				GroupId:        group.GroupID,
				GroupName:      group.Name,
				Avatar:         group.Avatar,
				OwnerUserId:    group.OwnerUserID,
				UpdateTime:     group.UpdateTime,
				ClientNeedSave: payload.GetClientNeedSave(),
			},
		},
	}, nil
}

func (h *FriendHandler) handleUpdateGroupAvatarWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.UpdateGroupAvatar) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetAvatarHash() == "" {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "update_group_avatar",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetRequestUserId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	_, isMember, err := db.RequireGroupManagerWithDB(database, payload.GetGroupId(), payload.GetRequestUserId())
	if err != nil {
		return nil, err
	}
	if !isMember {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "update_group_avatar",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetRequestUserId(),
				},
			},
		}, nil
	}

	updated, updateTime, err := db.UpdateGroupAvatarWithDB(database, payload.GetGroupId(), payload.GetAvatarHash())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if !updated {
		result = friend.FriendResult_RECORD_NOT_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_GroupOperationRsp{
			GroupOperationRsp: &friend.GroupOperationRsp{
				Operation:  "update_group_avatar",
				GroupId:    payload.GetGroupId(),
				UserId:     payload.GetRequestUserId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}

func (h *FriendHandler) handleQueryGroupMembersWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryGroupMembers) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}
	database = h.resolveDatabase(database)

	isMember, err := db.IsActiveGroupMemberWithDB(database, payload.GetGroupId(), payload.GetRequestUserId())
	if err != nil {
		return nil, err
	}
	if !isMember {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	members, err := db.GetGroupMembersWithDB(database, payload.GetGroupId())
	if err != nil {
		return nil, err
	}

	var friendMembers []*friend.GroupMemberContact
	for _, member := range members {
		friendMembers = append(friendMembers, &friend.GroupMemberContact{
			UserId:     member.UserID,
			Account:    member.Account,
			Name:       member.Name,
			Avatar:     member.Avatar,
			Role:       member.Role,
			UpdateTime: member.UpdateTime,
		})
	}

	return &friend.ResponseMessage{
		Result:       friend.FriendResult_FRIEND_OK,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_GroupMemberListRsp{
			GroupMemberListRsp: &friend.GroupMemberListRsp{
				GroupId: payload.GetGroupId(),
				Members: friendMembers,
			},
		},
	}, nil
}

func (h *FriendHandler) handleQueryJoinedGroupsWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryJoinedGroups) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}
	database = h.resolveDatabase(database)

	groups, err := db.GetJoinedGroupsWithDB(database, payload.GetUserId())
	if err != nil {
		return nil, err
	}

	var joinedGroups []*friend.JoinedGroupContact
	for _, group := range groups {
		joinedGroups = append(joinedGroups, &friend.JoinedGroupContact{
			GroupId:     group.GroupID,
			GroupName:   group.GroupName,
			Avatar:      group.Avatar,
			OwnerUserId: group.OwnerUserID,
			UpdateTime:  group.UpdateTime,
		})
	}

	return &friend.ResponseMessage{
		Result:       friend.FriendResult_FRIEND_OK,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_JoinedGroupListRsp{
			JoinedGroupListRsp: &friend.JoinedGroupListRsp{
				Groups: joinedGroups,
			},
		},
	}, nil
}

func (h *FriendHandler) handleRemoveGroupMemberWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.RemoveGroupMember) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetUserId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "remove_group_member",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetUserId(),
				},
			},
		}, nil
	}
	if payload.GetRequestUserId() != payload.GetUserId() {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
			Payload: &friend.ResponseMessage_GroupOperationRsp{
				GroupOperationRsp: &friend.GroupOperationRsp{
					Operation: "remove_group_member",
					GroupId:   payload.GetGroupId(),
					UserId:    payload.GetUserId(),
				},
			},
		}, nil
	}
	database = h.resolveDatabase(database)

	groupExists, removed, updateTime, err := db.RemoveUserFromGroupWithDB(database, payload.GetGroupId(), payload.GetUserId())
	if err != nil {
		return nil, err
	}

	result := friend.FriendResult_FRIEND_OK
	if !groupExists || !removed {
		result = friend.FriendResult_RECORD_NOT_EXIST
	}

	return &friend.ResponseMessage{
		Result:       result,
		TargetUserId: req.TargetUserId,
		Payload: &friend.ResponseMessage_GroupOperationRsp{
			GroupOperationRsp: &friend.GroupOperationRsp{
				Operation:  "remove_group_member",
				GroupId:    payload.GetGroupId(),
				UserId:     payload.GetUserId(),
				UpdateTime: updateTime,
			},
		},
	}, nil
}
