package handler

import (
	friend "Betterfly2/proto/friend"
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
}
