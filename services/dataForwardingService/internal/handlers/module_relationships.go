package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/dispatch"
	"errors"
)

func init() { registerDFRequestModule(registerRelationshipRequestModules) }

func registerRelationshipRequestModules(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryFriendRequests) (dfRequestResult, error) {
		return dfRequestResult{}, handleQueryFriendRequests(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_ResolveFriendRequest) (dfRequestResult, error) {
		return dfRequestResult{}, handleResolveFriendRequest(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroupJoinRequests) (dfRequestResult, error) {
		return dfRequestResult{}, handleQueryGroupJoinRequests(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_ResolveGroupJoinRequest) (dfRequestResult, error) {
		return dfRequestResult{}, handleResolveGroupJoinRequest(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InviteGroupMember) (dfRequestResult, error) {
		return dfRequestResult{}, handleInviteGroupMember(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroupInvitations) (dfRequestResult, error) {
		return dfRequestResult{}, handleQueryGroupInvitations(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_ResolveGroupInvitation) (dfRequestResult, error) {
		return dfRequestResult{}, handleResolveGroupInvitation(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_KickGroupMember) (dfRequestResult, error) {
		return dfRequestResult{}, handleKickGroupMember(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateGroupMemberRole) (dfRequestResult, error) {
		return dfRequestResult{}, handleUpdateGroupMemberRole(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateGroupName) (dfRequestResult, error) {
		return dfRequestResult{}, handleUpdateGroupName(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_TransferGroupOwner) (dfRequestResult, error) {
		return dfRequestResult{}, handleTransferGroupOwner(ctx.fromID, ctx.message)
	})
}

func handleQueryFriendRequests(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询好友申请", "query_friend_requests", (*pb.RequestMessage).GetQueryFriendRequests)
	if err != nil {
		return err
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_QueryFriendRequests{QueryFriendRequests: &friend.QueryFriendRequests{UserId: fromID, IncludeOutgoing: payload.GetIncludeOutgoing()}}
	return publishFriendRequest(req)
}

func handleResolveFriendRequest(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "处理好友申请", "resolve_friend_request", (*pb.RequestMessage).GetResolveFriendRequest)
	if err != nil {
		return err
	}
	decision, err := friendDecision(payload.GetDecision())
	if payload.GetRequestId() <= 0 || err != nil {
		return errors.New("好友申请处理参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_ResolveFriendRequest{ResolveFriendRequest: &friend.ResolveFriendRequest{UserId: fromID, RequestId: payload.GetRequestId(), Decision: decision}}
	return publishFriendRequest(req)
}

func handleQueryGroupJoinRequests(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询入群申请", "query_group_join_requests", (*pb.RequestMessage).GetQueryGroupJoinRequests)
	if err != nil || payload.GetTargetGroupId() <= 0 {
		if err == nil {
			err = errors.New("群ID非法")
		}
		return err
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_QueryGroupJoinRequests{QueryGroupJoinRequests: &friend.QueryGroupJoinRequests{RequestUserId: fromID, GroupId: payload.GetTargetGroupId()}}
	return publishFriendRequest(req)
}

func handleResolveGroupJoinRequest(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "处理入群申请", "resolve_group_join_request", (*pb.RequestMessage).GetResolveGroupJoinRequest)
	if err != nil {
		return err
	}
	decision, err := friendDecision(payload.GetDecision())
	if payload.GetRequestId() <= 0 || err != nil {
		return errors.New("入群申请处理参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_ResolveGroupJoinRequest{ResolveGroupJoinRequest: &friend.ResolveGroupJoinRequest{RequestUserId: fromID, RequestId: payload.GetRequestId(), Decision: decision}}
	return publishFriendRequest(req)
}

func handleInviteGroupMember(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "邀请群成员", "invite_group_member", (*pb.RequestMessage).GetInviteGroupMember)
	if err != nil {
		return err
	}
	if payload.GetTargetGroupId() <= 0 || payload.GetTargetUserId() <= 0 || payload.GetTargetUserId() == fromID {
		return errors.New("群邀请参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_InviteGroupMember{InviteGroupMember: &friend.InviteGroupMember{RequestUserId: fromID, GroupId: payload.GetTargetGroupId(), UserId: payload.GetTargetUserId(), Message: payload.GetMessage()}}
	return publishFriendRequest(req)
}

func handleQueryGroupInvitations(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询群邀请", "query_group_invitations", (*pb.RequestMessage).GetQueryGroupInvitations)
	if err != nil {
		return err
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_QueryGroupInvitations{QueryGroupInvitations: &friend.QueryGroupInvitations{UserId: fromID, IncludeOutgoing: payload.GetIncludeOutgoing()}}
	return publishFriendRequest(req)
}

func handleResolveGroupInvitation(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "处理群邀请", "resolve_group_invitation", (*pb.RequestMessage).GetResolveGroupInvitation)
	if err != nil {
		return err
	}
	decision, err := friendDecision(payload.GetDecision())
	if payload.GetInvitationId() <= 0 || err != nil {
		return errors.New("群邀请处理参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_ResolveGroupInvitation{ResolveGroupInvitation: &friend.ResolveGroupInvitation{UserId: fromID, InvitationId: payload.GetInvitationId(), Decision: decision}}
	return publishFriendRequest(req)
}

func handleKickGroupMember(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "移除群成员", "kick_group_member", (*pb.RequestMessage).GetKickGroupMember)
	if err != nil {
		return err
	}
	if payload.GetTargetGroupId() <= 0 || payload.GetTargetUserId() <= 0 || payload.GetTargetUserId() == fromID {
		return errors.New("移除群成员参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_KickGroupMember{KickGroupMember: &friend.KickGroupMember{RequestUserId: fromID, GroupId: payload.GetTargetGroupId(), UserId: payload.GetTargetUserId()}}
	return publishFriendRequest(req)
}

func handleUpdateGroupMemberRole(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "设置群成员角色", "update_group_member_role", (*pb.RequestMessage).GetUpdateGroupMemberRole)
	if err != nil {
		return err
	}
	if payload.GetTargetGroupId() <= 0 || payload.GetTargetUserId() <= 0 || (payload.GetRole() != "admin" && payload.GetRole() != "member") {
		return errors.New("群成员角色参数非法")
	}
	req := newFriendRequest(currentContainerTopic(), fromID)
	req.Payload = &friend.RequestMessage_UpdateGroupMemberRole{UpdateGroupMemberRole: &friend.UpdateGroupMemberRole{RequestUserId: fromID, GroupId: payload.GetTargetGroupId(), UserId: payload.GetTargetUserId(), Role: payload.GetRole()}}
	return publishFriendRequest(req)
}

func handleUpdateGroupName(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "修改群名称", "update_group_name", (*pb.RequestMessage).GetUpdateGroupName)
	if err != nil {
		return err
	}
	if payload.GetTargetGroupId() <= 0 {
		return errors.New("群名称修改参数非法")
	}
	return publishFriendRequest(buildUpdateGroupNameFriendRequest(fromID, payload.GetTargetGroupId(), payload.GetNewGroupName(), currentContainerTopic()))
}

func buildUpdateGroupNameFriendRequest(fromID, groupID int64, groupName, topic string) *friend.RequestMessage {
	req := newFriendRequest(topic, fromID)
	req.Payload = &friend.RequestMessage_UpdateGroupName{UpdateGroupName: &friend.UpdateGroupName{
		RequestUserId: fromID,
		GroupId:       groupID,
		GroupName:     groupName,
	}}
	return req
}

func handleTransferGroupOwner(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "转让群主", "transfer_group_owner", (*pb.RequestMessage).GetTransferGroupOwner)
	if err != nil {
		return err
	}
	if payload.GetTargetGroupId() <= 0 || payload.GetTargetUserId() <= 0 || payload.GetTargetUserId() == fromID {
		return errors.New("群主转让参数非法")
	}
	return publishFriendRequest(buildTransferGroupOwnerFriendRequest(fromID, payload.GetTargetGroupId(), payload.GetTargetUserId(), currentContainerTopic()))
}

func buildTransferGroupOwnerFriendRequest(fromID, groupID, targetUserID int64, topic string) *friend.RequestMessage {
	req := newFriendRequest(topic, fromID)
	req.Payload = &friend.RequestMessage_TransferGroupOwner{TransferGroupOwner: &friend.TransferGroupOwner{
		RequestUserId: fromID,
		GroupId:       groupID,
		UserId:        targetUserID,
	}}
	return req
}

func friendDecision(decision pb.RequestDecision) (friend.RequestDecision, error) {
	switch decision {
	case pb.RequestDecision_REQUEST_ACCEPT:
		return friend.RequestDecision_REQUEST_ACCEPT, nil
	case pb.RequestDecision_REQUEST_REJECT:
		return friend.RequestDecision_REQUEST_REJECT, nil
	case pb.RequestDecision_REQUEST_CANCEL:
		return friend.RequestDecision_REQUEST_CANCEL, nil
	default:
		return friend.RequestDecision_REQUEST_DECISION_UNSPECIFIED, errors.New("decision非法")
	}
}
