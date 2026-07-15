package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"errors"
	"gorm.io/gorm"
	"unicode/utf8"
)

func (h *FriendHandler) handleAddDirectFriend(req *friend.RequestMessage, payload *friend.AddDirectFriend) (*friend.ResponseMessage, error) {
	return h.handleAddDirectFriendWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleAddDirectFriendWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.AddDirectFriend) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 || payload.GetFriendId() <= 0 || payload.GetUserId() == payload.GetFriendId() || !validVerificationMessage(payload.GetMessage()) {
		return relationshipError(req, "create_friend_request", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	if user, err := db.GetUserByIDWithDB(database, payload.GetUserId()); err != nil {
		return nil, err
	} else if user == nil {
		return relationshipError(req, "create_friend_request", friend.FriendResult_RECORD_NOT_EXIST, nil), nil
	}
	if target, err := db.GetUserByIDWithDB(database, payload.GetFriendId()); err != nil {
		return nil, err
	} else if target == nil {
		return relationshipError(req, "create_friend_request", friend.FriendResult_RECORD_NOT_EXIST, nil), nil
	}
	request, created, err := db.CreateFriendRequestWithDB(database, payload.GetUserId(), payload.GetFriendId(), payload.GetMessage())
	if err != nil {
		if errors.Is(err, db.ErrAlreadyRelated) {
			return relationshipError(req, "create_friend_request", friend.FriendResult_ALREADY_FRIEND, nil), nil
		}
		return relationshipDBError(req, "create_friend_request", err)
	}
	result := friend.FriendResult_FRIEND_OK
	if !created {
		result = friend.FriendResult_REQUEST_PENDING
	}
	return relationshipOperation(req, "create_friend_request", result, request), nil
}

func (h *FriendHandler) handleQueryFriendRequests(req *friend.RequestMessage, payload *friend.QueryFriendRequests) (*friend.ResponseMessage, error) {
	return h.handleQueryFriendRequestsWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleQueryFriendRequestsWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryFriendRequests) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return relationshipList(req, friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	requests, err := db.ListFriendRequestsWithDB(database, payload.GetUserId(), payload.GetIncludeOutgoing())
	if err != nil {
		return nil, err
	}
	return relationshipList(req, friend.FriendResult_FRIEND_OK, requests), nil
}

func (h *FriendHandler) handleResolveFriendRequest(req *friend.RequestMessage, payload *friend.ResolveFriendRequest) (*friend.ResponseMessage, error) {
	return h.handleResolveFriendRequestWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleResolveFriendRequestWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.ResolveFriendRequest) (*friend.ResponseMessage, error) {
	decision, ok := decisionStatus(payload.GetDecision())
	if payload.GetUserId() <= 0 || payload.GetRequestId() <= 0 || !ok {
		return relationshipError(req, "resolve_friend_request", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	request, err := db.ResolveFriendRequestWithDB(database, payload.GetUserId(), payload.GetRequestId(), decision)
	if err != nil {
		return relationshipDBError(req, "resolve_friend_request", err)
	}
	return relationshipOperation(req, "resolve_friend_request", friend.FriendResult_FRIEND_OK, request), nil
}

func (h *FriendHandler) handleAddGroupMember(req *friend.RequestMessage, payload *friend.AddGroupMember) (*friend.ResponseMessage, error) {
	return h.handleAddGroupMemberWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleAddGroupMemberWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.AddGroupMember) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 || payload.GetGroupId() <= 0 || !validVerificationMessage(payload.GetMessage()) {
		return relationshipError(req, "create_group_join_request", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	if user, err := db.GetUserByIDWithDB(database, payload.GetUserId()); err != nil {
		return nil, err
	} else if user == nil {
		return relationshipError(req, "create_group_join_request", friend.FriendResult_RECORD_NOT_EXIST, nil), nil
	}
	request, created, err := db.CreateGroupJoinRequestWithDB(database, payload.GetUserId(), payload.GetGroupId(), payload.GetMessage())
	if err != nil {
		return relationshipDBError(req, "create_group_join_request", err)
	}
	result := friend.FriendResult_FRIEND_OK
	if !created {
		result = friend.FriendResult_REQUEST_PENDING
	}
	return relationshipOperation(req, "create_group_join_request", result, request), nil
}

func (h *FriendHandler) handleQueryGroupJoinRequests(req *friend.RequestMessage, payload *friend.QueryGroupJoinRequests) (*friend.ResponseMessage, error) {
	return h.handleQueryGroupJoinRequestsWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleQueryGroupJoinRequestsWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryGroupJoinRequests) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 {
		return relationshipList(req, friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	requests, err := db.ListGroupJoinRequestsWithDB(database, payload.GetRequestUserId(), payload.GetGroupId())
	if err != nil {
		result := relationshipResult(err)
		if result != friend.FriendResult_SERVICE_ERROR {
			return relationshipList(req, result, nil), nil
		}
		return nil, err
	}
	return relationshipList(req, friend.FriendResult_FRIEND_OK, requests), nil
}

func (h *FriendHandler) handleResolveGroupJoinRequest(req *friend.RequestMessage, payload *friend.ResolveGroupJoinRequest) (*friend.ResponseMessage, error) {
	return h.handleResolveGroupJoinRequestWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleResolveGroupJoinRequestWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.ResolveGroupJoinRequest) (*friend.ResponseMessage, error) {
	decision, ok := decisionStatus(payload.GetDecision())
	if payload.GetRequestUserId() <= 0 || payload.GetRequestId() <= 0 || !ok {
		return relationshipError(req, "resolve_group_join_request", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	request, err := db.ResolveGroupJoinRequestWithDB(database, payload.GetRequestUserId(), payload.GetRequestId(), decision)
	if err != nil {
		return relationshipDBError(req, "resolve_group_join_request", err)
	}
	return relationshipOperation(req, "resolve_group_join_request", friend.FriendResult_FRIEND_OK, request), nil
}

func (h *FriendHandler) handleInviteGroupMember(req *friend.RequestMessage, payload *friend.InviteGroupMember) (*friend.ResponseMessage, error) {
	return h.handleInviteGroupMemberWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleInviteGroupMemberWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.InviteGroupMember) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetUserId() <= 0 || payload.GetRequestUserId() == payload.GetUserId() || !validVerificationMessage(payload.GetMessage()) {
		return relationshipError(req, "invite_group_member", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	if target, err := db.GetUserByIDWithDB(database, payload.GetUserId()); err != nil {
		return nil, err
	} else if target == nil {
		return relationshipError(req, "invite_group_member", friend.FriendResult_RECORD_NOT_EXIST, nil), nil
	}
	request, created, err := db.CreateGroupInvitationWithDB(database, payload.GetRequestUserId(), payload.GetGroupId(), payload.GetUserId(), payload.GetMessage())
	if err != nil {
		return relationshipDBError(req, "invite_group_member", err)
	}
	result := friend.FriendResult_FRIEND_OK
	if !created {
		result = friend.FriendResult_REQUEST_PENDING
	}
	return relationshipOperation(req, "invite_group_member", result, request), nil
}

func (h *FriendHandler) handleQueryGroupInvitations(req *friend.RequestMessage, payload *friend.QueryGroupInvitations) (*friend.ResponseMessage, error) {
	return h.handleQueryGroupInvitationsWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleQueryGroupInvitationsWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.QueryGroupInvitations) (*friend.ResponseMessage, error) {
	if payload.GetUserId() <= 0 {
		return relationshipList(req, friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	requests, err := db.ListGroupInvitationsWithDB(database, payload.GetUserId(), payload.GetIncludeOutgoing())
	if err != nil {
		return nil, err
	}
	return relationshipList(req, friend.FriendResult_FRIEND_OK, requests), nil
}

func (h *FriendHandler) handleResolveGroupInvitation(req *friend.RequestMessage, payload *friend.ResolveGroupInvitation) (*friend.ResponseMessage, error) {
	return h.handleResolveGroupInvitationWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleResolveGroupInvitationWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.ResolveGroupInvitation) (*friend.ResponseMessage, error) {
	decision, ok := decisionStatus(payload.GetDecision())
	if payload.GetUserId() <= 0 || payload.GetInvitationId() <= 0 || !ok {
		return relationshipError(req, "resolve_group_invitation", friend.FriendResult_INVALID_ARGUMENT, nil), nil
	}
	database = h.resolveDatabase(database)
	request, err := db.ResolveGroupInvitationWithDB(database, payload.GetUserId(), payload.GetInvitationId(), decision)
	if err != nil {
		return relationshipDBError(req, "resolve_group_invitation", err)
	}
	return relationshipOperation(req, "resolve_group_invitation", friend.FriendResult_FRIEND_OK, request), nil
}

func (h *FriendHandler) handleKickGroupMember(req *friend.RequestMessage, payload *friend.KickGroupMember) (*friend.ResponseMessage, error) {
	return h.handleKickGroupMemberWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleKickGroupMemberWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.KickGroupMember) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetUserId() <= 0 || payload.GetRequestUserId() == payload.GetUserId() {
		return groupOperation(req, "kick_group_member", friend.FriendResult_INVALID_ARGUMENT, payload.GetGroupId(), payload.GetUserId(), "", ""), nil
	}
	database = h.resolveDatabase(database)
	updatedAt, err := db.KickGroupMemberByWithDB(database, payload.GetRequestUserId(), payload.GetGroupId(), payload.GetUserId())
	if err != nil {
		result := relationshipResult(err)
		if result != friend.FriendResult_SERVICE_ERROR {
			return groupOperation(req, "kick_group_member", result, payload.GetGroupId(), payload.GetUserId(), "", ""), nil
		}
		return nil, err
	}
	return groupOperation(req, "kick_group_member", friend.FriendResult_FRIEND_OK, payload.GetGroupId(), payload.GetUserId(), "", updatedAt), nil
}

func (h *FriendHandler) handleUpdateGroupMemberRole(req *friend.RequestMessage, payload *friend.UpdateGroupMemberRole) (*friend.ResponseMessage, error) {
	return h.handleUpdateGroupMemberRoleWithDB(h.database, req, payload)
}

func (h *FriendHandler) handleUpdateGroupMemberRoleWithDB(database *gorm.DB, req *friend.RequestMessage, payload *friend.UpdateGroupMemberRole) (*friend.ResponseMessage, error) {
	if payload.GetRequestUserId() <= 0 || payload.GetGroupId() <= 0 || payload.GetUserId() <= 0 {
		return groupOperation(req, "update_group_member_role", friend.FriendResult_INVALID_ARGUMENT, payload.GetGroupId(), payload.GetUserId(), payload.GetRole(), ""), nil
	}
	database = h.resolveDatabase(database)
	updatedAt, err := db.UpdateGroupMemberRoleByWithDB(database, payload.GetRequestUserId(), payload.GetGroupId(), payload.GetUserId(), payload.GetRole())
	if err != nil {
		result := relationshipResult(err)
		if result != friend.FriendResult_SERVICE_ERROR {
			return groupOperation(req, "update_group_member_role", result, payload.GetGroupId(), payload.GetUserId(), payload.GetRole(), ""), nil
		}
		return nil, err
	}
	return groupOperation(req, "update_group_member_role", friend.FriendResult_FRIEND_OK, payload.GetGroupId(), payload.GetUserId(), payload.GetRole(), updatedAt), nil
}

func decisionStatus(decision friend.RequestDecision) (string, bool) {
	switch decision {
	case friend.RequestDecision_REQUEST_ACCEPT:
		return db.RequestStatusAccepted, true
	case friend.RequestDecision_REQUEST_REJECT:
		return db.RequestStatusRejected, true
	case friend.RequestDecision_REQUEST_CANCEL:
		return db.RequestStatusCancelled, true
	default:
		return "", false
	}
}

func validVerificationMessage(message string) bool {
	return utf8.ValidString(message) && utf8.RuneCountInString(message) <= 255
}

func relationshipDBError(req *friend.RequestMessage, operation string, err error) (*friend.ResponseMessage, error) {
	result := relationshipResult(err)
	if result == friend.FriendResult_SERVICE_ERROR {
		return nil, err
	}
	return relationshipError(req, operation, result, nil), nil
}

func relationshipResult(err error) friend.FriendResult {
	switch {
	case errors.Is(err, db.ErrRelationshipNotFound):
		return friend.FriendResult_RECORD_NOT_EXIST
	case errors.Is(err, db.ErrRelationshipForbidden):
		return friend.FriendResult_FORBIDDEN
	case errors.Is(err, db.ErrRelationshipExpired):
		return friend.FriendResult_REQUEST_EXPIRED
	case errors.Is(err, db.ErrRelationshipInvalidState):
		return friend.FriendResult_INVALID_STATE
	case errors.Is(err, db.ErrAlreadyRelated):
		return friend.FriendResult_ALREADY_EXIST
	default:
		return friend.FriendResult_SERVICE_ERROR
	}
}

func relationshipError(req *friend.RequestMessage, operation string, result friend.FriendResult, request *db.RelationshipRequestView) *friend.ResponseMessage {
	return relationshipOperation(req, operation, result, request)
}

func relationshipOperation(req *friend.RequestMessage, operation string, result friend.FriendResult, request *db.RelationshipRequestView) *friend.ResponseMessage {
	return &friend.ResponseMessage{Result: result, TargetUserId: req.GetTargetUserId(), Payload: &friend.ResponseMessage_RelationshipOperationRsp{
		RelationshipOperationRsp: &friend.RelationshipOperationRsp{Operation: operation, Request: relationshipInfo(request)},
	}}
}

func relationshipList(req *friend.RequestMessage, result friend.FriendResult, requests []db.RelationshipRequestView) *friend.ResponseMessage {
	items := make([]*friend.RelationshipRequestInfo, 0, len(requests))
	for i := range requests {
		items = append(items, relationshipInfo(&requests[i]))
	}
	return &friend.ResponseMessage{Result: result, TargetUserId: req.GetTargetUserId(), Payload: &friend.ResponseMessage_RelationshipRequestListRsp{
		RelationshipRequestListRsp: &friend.RelationshipRequestListRsp{Requests: items},
	}}
}

func relationshipInfo(request *db.RelationshipRequestView) *friend.RelationshipRequestInfo {
	if request == nil {
		return nil
	}
	return &friend.RelationshipRequestInfo{
		RequestId: request.ID, RequestType: request.RequestType,
		RequesterUserId: request.RequesterUserID, RequesterName: request.RequesterName, RequesterAvatar: request.RequesterAvatar,
		TargetUserId: request.TargetUserID, TargetName: request.TargetName, TargetAvatar: request.TargetAvatar,
		GroupId: request.GroupID, GroupName: request.GroupName, GroupAvatar: request.GroupAvatar,
		Message: request.Message, Status: request.Status, CreatedAt: request.CreatedAt, ExpiresAt: request.ExpiresAt,
		ResolvedAt: request.ResolvedAt, ResolvedBy: request.ResolvedBy,
	}
}

func groupOperation(req *friend.RequestMessage, operation string, result friend.FriendResult, groupID, userID int64, role, updateTime string) *friend.ResponseMessage {
	return &friend.ResponseMessage{Result: result, TargetUserId: req.GetTargetUserId(), Payload: &friend.ResponseMessage_GroupOperationRsp{
		GroupOperationRsp: &friend.GroupOperationRsp{Operation: operation, GroupId: groupID, UserId: userID, Role: role, UpdateTime: updateTime},
	}}
}
