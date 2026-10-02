package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerFriendRequestModule(registerFriendGroupModule)
}

func registerFriendGroupModule(router *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]) {
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_CreateGroup) (*friend.ResponseMessage, error) {
		return ctx.handler.handleCreateGroupWithDB(ctx.database, ctx.request, payload.CreateGroup)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroup) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroupWithDB(ctx.database, ctx.request, payload.QueryGroup)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_AddGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleAddGroupMemberWithDB(ctx.database, ctx.request, payload.AddGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateGroupAvatar) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateGroupAvatarWithDB(ctx.database, ctx.request, payload.UpdateGroupAvatar)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroupMembers) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroupMembersWithDB(ctx.database, ctx.request, payload.QueryGroupMembers)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_RemoveGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleRemoveGroupMemberWithDB(ctx.database, ctx.request, payload.RemoveGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryJoinedGroups) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryJoinedGroupsWithDB(ctx.database, ctx.request, payload.QueryJoinedGroups)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateGroupName) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateGroupNameWithDB(ctx.database, ctx.request, payload.UpdateGroupName)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_TransferGroupOwner) (*friend.ResponseMessage, error) {
		return ctx.handler.handleTransferGroupOwnerWithDB(ctx.database, ctx.request, payload.TransferGroupOwner)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateGroupNotify) (*friend.ResponseMessage, error) {
		p := payload.UpdateGroupNotify
		actorID := ctx.request.GetTargetUserId()
		result := friend.FriendResult_INVALID_ARGUMENT
		var updatedAt string
		if actorID > 0 && p.GetGroupId() > 0 {
			var err error
			updatedAt, err = db.UpdateGroupNotifyWithDB(ctx.database, actorID, p.GetGroupId(), p.GetIsNotify())
			result = friend.FriendResult_FRIEND_OK
			if err != nil {
				result = relationshipResult(err)
			}
			if err != nil && result == friend.FriendResult_SERVICE_ERROR {
				return nil, err
			}
		}
		response := groupOperation(ctx.request, "update_group_notify", result, p.GetGroupId(), actorID, "", updatedAt)
		if result == friend.FriendResult_FRIEND_OK {
			response.GetGroupOperationRsp().NotificationsMuted = !p.GetIsNotify()
		}
		return response, nil
	})
}
