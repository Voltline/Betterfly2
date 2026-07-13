package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/monitor"
	"errors"
	"strings"
)

func init() {
	registerDFRequestModule(registerFriendRequestModules)
}

func registerFriendRequestModules(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertContact) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertContact 消息")
		return dfRequestResult{}, handleInsertContact(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryContacts) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryContacts 消息")
		return dfRequestResult{}, handleQueryContacts(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_DeleteContact) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 DeleteContact 消息")
		return dfRequestResult{}, handleDeleteContact(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateContactAlias) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateContactAlias 消息")
		return dfRequestResult{}, handleUpdateContactAlias(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateContactNotify) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateContactNotify 消息")
		return dfRequestResult{}, handleUpdateContactNotify(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroup) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryGroup 消息")
		return dfRequestResult{}, handleQueryGroup(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertGroup) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertGroup 消息")
		return dfRequestResult{}, handleInsertGroup(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_InsertGroupUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 InsertGroupUser 消息")
		return dfRequestResult{}, handleInsertGroupUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryGroupMembers) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryGroupMembers 消息")
		return dfRequestResult{}, handleQueryGroupMembers(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryJoinedGroups) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 QueryJoinedGroups 消息")
		return dfRequestResult{}, handleQueryJoinedGroups(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_DeleteGroupUser) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 DeleteGroupUser 消息")
		return dfRequestResult{}, handleDeleteGroupUser(ctx.fromID, ctx.message)
	})
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_UpdateAvatar) (dfRequestResult, error) {
		logger.Sugar().Debugf("收到 UpdateAvatar 消息")
		return dfRequestResult{}, handleUpdateAvatar(ctx.fromID, ctx.message)
	})
}

func handleInsertContact(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "添加好友", "insert_contact", (*pb.RequestMessage).GetInsertContact)
	if err != nil {
		return err
	}
	if err := requirePositiveID("to_insert_user_id", payload.GetToInsertUserId()); err != nil {
		return err
	}
	if payload.GetToInsertUserId() == fromID {
		return errors.New("不能添加自己为好友")
	}
	if monitor.IsMonitorID(payload.GetToInsertUserId()) {
		return handleMonitorAddContact(fromID)
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertContactFriendRequestWithMessage(fromID, payload.GetToInsertUserId(), payload.GetMessage(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("添加好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToInsertUserId())
	return nil
}

func handleQueryContacts(fromID int64, message *pb.RequestMessage) error {
	if _, err := authenticatedPayload(fromID, message, "查询好友列表", "query_contacts", (*pb.RequestMessage).GetQueryContacts); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	friendReq := buildQueryContactsFriendRequest(fromID, currentContainerID)

	if err := publishFriendRequest(friendReq); err != nil {
		logger.Sugar().Errorf("发布QueryContacts请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryContactsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryFriendList{
		QueryFriendList: &friend.QueryFriendList{
			UserId: fromID,
		},
	}
	return req
}

func handleQueryGroup(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询群信息", "query_group", (*pb.RequestMessage).GetQueryGroup)
	if err != nil {
		return err
	}
	if err := requirePositiveID("to_query_group_id", payload.GetToQueryGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryGroupFriendRequest(fromID, payload.GetToQueryGroupId(), payload.GetClientNeedSave(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群信息查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetToQueryGroupId())
	return nil
}

func buildQueryGroupFriendRequest(fromID, groupID int64, clientNeedSave bool, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryGroup{
		QueryGroup: &friend.QueryGroup{
			RequestUserId:  fromID,
			GroupId:        groupID,
			ClientNeedSave: clientNeedSave,
		},
	}
	return req
}

func handleInsertGroup(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "创建群组", "insert_group", (*pb.RequestMessage).GetInsertGroup)
	if err != nil {
		return err
	}
	if payload.GetToBeCreatedGroupId() <= 0 || strings.TrimSpace(payload.GetToBeCreatedGroupName()) == "" {
		return errors.New("群组信息非法")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertGroupFriendRequest(fromID, payload.GetToBeCreatedGroupId(), payload.GetToBeCreatedGroupName(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroup请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("创建群组请求已发送到friendService: owner_id=%d, group_id=%d", fromID, payload.GetToBeCreatedGroupId())
	return nil
}

func buildInsertGroupFriendRequest(fromID, groupID int64, groupName, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_CreateGroup{
		CreateGroup: &friend.CreateGroup{
			OwnerUserId: fromID,
			GroupId:     groupID,
			GroupName:   groupName,
		},
	}
	return req
}

func handleInsertGroupUser(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "加入群组", "insert_group_user", (*pb.RequestMessage).GetInsertGroupUser)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildInsertGroupUserFriendRequestWithMessage(fromID, payload.GetTargetGroupId(), payload.GetMessage(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布InsertGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("加入群组请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildInsertGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	return buildInsertGroupUserFriendRequestWithMessage(fromID, groupID, "", currentContainerID)
}

func buildInsertGroupUserFriendRequestWithMessage(fromID, groupID int64, message, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_AddGroupMember{
		AddGroupMember: &friend.AddGroupMember{
			UserId:  fromID,
			GroupId: groupID,
			Message: message,
		},
	}
	return req
}

func handleQueryGroupMembers(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "查询群成员", "query_group_members", (*pb.RequestMessage).GetQueryGroupMembers)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryGroupMembersFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryGroupMembers请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群成员列表查询请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func handleQueryJoinedGroups(fromID int64, message *pb.RequestMessage) error {
	if _, err := authenticatedPayload(fromID, message, "查询已加入群列表", "query_joined_groups", (*pb.RequestMessage).GetQueryJoinedGroups); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildQueryJoinedGroupsFriendRequest(fromID, currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布QueryJoinedGroups请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("已加入群列表查询请求已发送到friendService: user_id=%d", fromID)
	return nil
}

func buildQueryJoinedGroupsFriendRequest(fromID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryJoinedGroups{
		QueryJoinedGroups: &friend.QueryJoinedGroups{
			UserId: fromID,
		},
	}
	return req
}

func buildQueryGroupMembersFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_QueryGroupMembers{
		QueryGroupMembers: &friend.QueryGroupMembers{
			RequestUserId: fromID,
			GroupId:       groupID,
		},
	}
	return req
}

func handleDeleteGroupUser(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "退出群组", "delete_group_user", (*pb.RequestMessage).GetDeleteGroupUser)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_group_id", payload.GetTargetGroupId()); err != nil {
		return err
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildDeleteGroupUserFriendRequest(fromID, payload.GetTargetGroupId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteGroupUser请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("退群请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetGroupId())
	return nil
}

func buildDeleteGroupUserFriendRequest(fromID, groupID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_RemoveGroupMember{
		RemoveGroupMember: &friend.RemoveGroupMember{
			RequestUserId: fromID,
			GroupId:       groupID,
			UserId:        fromID,
		},
	}
	return req
}

func handleUpdateAvatar(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新头像", "update_avatar", (*pb.RequestMessage).GetUpdateAvatar)
	if err != nil {
		return err
	}
	if !payload.GetIsGroup() {
		return errors.New("当前仅支持通过UpdateAvatar更新群头像")
	}
	if err := requirePositiveID("target_id", payload.GetTargetId()); err != nil {
		return errors.New("群头像更新请求非法")
	}
	if strings.TrimSpace(payload.GetAvatarHash()) == "" {
		return errors.New("群头像更新请求非法")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateGroupAvatarFriendRequest(fromID, payload.GetTargetId(), payload.GetAvatarHash(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateAvatar请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("群头像更新请求已发送到friendService: user_id=%d, group_id=%d", fromID, payload.GetTargetId())
	return nil
}

func buildUpdateGroupAvatarFriendRequest(fromID, groupID int64, avatarHash, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateGroupAvatar{
		UpdateGroupAvatar: &friend.UpdateGroupAvatar{
			RequestUserId: fromID,
			GroupId:       groupID,
			AvatarHash:    avatarHash,
		},
	}
	return req
}

func buildInsertContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	return buildInsertContactFriendRequestWithMessage(fromID, targetUserID, "", currentContainerID)
}

func buildInsertContactFriendRequestWithMessage(fromID, targetUserID int64, message, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_AddDirectFriend{
		AddDirectFriend: &friend.AddDirectFriend{
			UserId:   fromID,
			FriendId: targetUserID,
			Message:  message,
		},
	}
	return req
}

func handleDeleteContact(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "删除好友", "delete_contact", (*pb.RequestMessage).GetDeleteContact)
	if err != nil {
		return err
	}
	if err := requireNonSelfID("to_delete_user_id", payload.GetToDeleteUserId(), fromID); err != nil {
		return err
	}
	if monitor.IsMonitorID(payload.GetToDeleteUserId()) {
		return handleMonitorDeleteContact(fromID)
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildDeleteContactFriendRequest(fromID, payload.GetToDeleteUserId(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布DeleteContact请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("删除好友请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetToDeleteUserId())
	return nil
}

func buildDeleteContactFriendRequest(fromID, targetUserID int64, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_RemoveDirectFriend{
		RemoveDirectFriend: &friend.RemoveDirectFriend{
			UserId:   fromID,
			FriendId: targetUserID,
		},
	}
	return req
}

func handleUpdateContactAlias(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新好友备注", "update_contact_alias", (*pb.RequestMessage).GetUpdateContactAlias)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_user_id", payload.GetTargetUserId()); err != nil {
		return err
	}
	if monitor.IsMonitorID(payload.GetTargetUserId()) {
		if fromID != monitor.AdminUserID {
			return sendMonitorWarning(fromID, "目标用户不存在")
		}
		return sendMonitorWarning(fromID, "Monitor 虚拟联系人不支持修改备注")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateContactAliasFriendRequest(fromID, payload.GetTargetUserId(), payload.GetNewAlias(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactAlias请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友备注更新请求已发送到friendService: user_id=%d, friend_id=%d", fromID, payload.GetTargetUserId())
	return nil
}

func buildUpdateContactAliasFriendRequest(fromID, targetUserID int64, alias, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateFriendAlias{
		UpdateFriendAlias: &friend.UpdateFriendAlias{
			UserId:   fromID,
			FriendId: targetUserID,
			Alias:    alias,
		},
	}
	return req
}

func handleUpdateContactNotify(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "更新好友通知设置", "update_contact_notify", (*pb.RequestMessage).GetUpdateContactNotify)
	if err != nil {
		return err
	}
	if err := requirePositiveID("target_user_id", payload.GetTargetUserId()); err != nil {
		return err
	}
	if monitor.IsMonitorID(payload.GetTargetUserId()) {
		if fromID != monitor.AdminUserID {
			return sendMonitorWarning(fromID, "目标用户不存在")
		}
		return sendMonitorWarning(fromID, "Monitor 虚拟联系人不发送普通消息通知")
	}

	currentContainerID := currentContainerTopic()

	if err := publishFriendRequest(buildUpdateContactNotifyFriendRequest(fromID, payload.GetTargetUserId(), payload.GetIsNotify(), currentContainerID)); err != nil {
		logger.Sugar().Errorf("发布UpdateContactNotify请求到friend-service失败: %v", err)
		return err
	}

	logger.Sugar().Debugf("好友通知设置更新请求已发送到friendService: user_id=%d, friend_id=%d, is_notify=%v", fromID, payload.GetTargetUserId(), payload.GetIsNotify())
	return nil
}

func buildUpdateContactNotifyFriendRequest(fromID, targetUserID int64, isNotify bool, currentContainerID string) *friend.RequestMessage {
	req := newFriendRequest(currentContainerID, fromID)
	req.Payload = &friend.RequestMessage_UpdateFriendNotify{
		UpdateFriendNotify: &friend.UpdateFriendNotify{
			UserId:   fromID,
			FriendId: targetUserID,
			IsNotify: isNotify,
		},
	}
	return req
}
