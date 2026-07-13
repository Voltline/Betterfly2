package handler

import (
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
	"context"
	"sync"

	"friendService/internal/publisher"

	"google.golang.org/protobuf/proto"
)

type friendRequestContext struct {
	handler *FriendHandler
	request *friend.RequestMessage
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

type FriendHandler struct{}

func NewFriendHandler() *FriendHandler {
	_ = db.DB(&db.User{}, &db.Friend{}, &db.Group{}, &db.GroupMember{}, &db.RelationshipRequest{})
	return &FriendHandler{}
}

func (h *FriendHandler) HandleMessage(_ context.Context, message []byte) error {
	req := &friend.RequestMessage{}
	if err := proto.Unmarshal(message, req); err != nil {
		return err
	}

	var (
		resp *friend.ResponseMessage
		err  error
	)

	resp, err = getFriendRequestRouter().Dispatch(friendRequestContext{
		handler: h,
		request: req,
	}, req.Payload)

	if err != nil {
		logger.Sugar().Errorf("处理friend请求失败: %v", err)
		resp = &friend.ResponseMessage{
			Result:       friend.FriendResult_SERVICE_ERROR,
			TargetUserId: req.TargetUserId,
		}
	}

	return h.sendResponse(req.FromKafkaTopic, resp)
}

func (h *FriendHandler) handleQueryFriendList(req *friend.RequestMessage, payload *friend.QueryFriendList) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	contacts, err := db.GetFriendList(payload.GetUserId())
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

func (h *FriendHandler) handleRemoveDirectFriend(req *friend.RequestMessage, payload *friend.RemoveDirectFriend) (*friend.ResponseMessage, error) {
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

	found, updateTime, err := db.RemoveDirectFriendPair(payload.GetUserId(), payload.GetFriendId())
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

func (h *FriendHandler) handleUpdateFriendAlias(req *friend.RequestMessage, payload *friend.UpdateFriendAlias) (*friend.ResponseMessage, error) {
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

	found, updateTime, err := db.UpdateFriendAlias(payload.GetUserId(), payload.GetFriendId(), payload.GetAlias())
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

func (h *FriendHandler) handleUpdateFriendNotify(req *friend.RequestMessage, payload *friend.UpdateFriendNotify) (*friend.ResponseMessage, error) {
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

	found, updateTime, err := db.UpdateFriendNotify(payload.GetUserId(), payload.GetFriendId(), payload.GetIsNotify())
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

func (h *FriendHandler) handleCreateGroup(req *friend.RequestMessage, payload *friend.CreateGroup) (*friend.ResponseMessage, error) {
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

	owner, err := db.GetUserById(payload.GetOwnerUserId())
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

	alreadyExists, updateTime, err := db.CreateGroupWithOwner(payload.GetOwnerUserId(), payload.GetGroupId(), payload.GetGroupName())
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

func (h *FriendHandler) handleQueryGroup(req *friend.RequestMessage, payload *friend.QueryGroup) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	group, err := db.GetGroupByID(payload.GetGroupId())
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

func (h *FriendHandler) handleUpdateGroupAvatar(req *friend.RequestMessage, payload *friend.UpdateGroupAvatar) (*friend.ResponseMessage, error) {
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

	_, isMember, err := db.RequireGroupManager(payload.GetGroupId(), payload.GetRequestUserId())
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

	updated, updateTime, err := db.UpdateGroupAvatar(payload.GetGroupId(), payload.GetAvatarHash())
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

func (h *FriendHandler) handleQueryGroupMembers(req *friend.RequestMessage, payload *friend.QueryGroupMembers) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	isMember, err := db.IsActiveGroupMember(payload.GetGroupId(), payload.GetRequestUserId())
	if err != nil {
		return nil, err
	}
	if !isMember {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	members, err := db.GetGroupMembers(payload.GetGroupId())
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

func (h *FriendHandler) handleQueryJoinedGroups(req *friend.RequestMessage, payload *friend.QueryJoinedGroups) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return &friend.ResponseMessage{
			Result:       friend.FriendResult_INVALID_ARGUMENT,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	groups, err := db.GetJoinedGroups(payload.GetUserId())
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

func (h *FriendHandler) handleRemoveGroupMember(req *friend.RequestMessage, payload *friend.RemoveGroupMember) (*friend.ResponseMessage, error) {
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

	groupExists, removed, updateTime, err := db.RemoveUserFromGroup(payload.GetGroupId(), payload.GetUserId())
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

func (h *FriendHandler) sendResponse(topic string, resp *friend.ResponseMessage) error {
	_, err := mq.PublishEnvelope(publisher.PublishMessage, topic, envelope.MessageType_FRIEND_RESPONSE, resp)
	return err
}
