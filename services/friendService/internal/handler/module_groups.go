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
		return ctx.handler.handleCreateGroup(ctx.request, payload.CreateGroup)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroup) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroup(ctx.request, payload.QueryGroup)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_AddGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleAddGroupMember(ctx.request, payload.AddGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateGroupAvatar) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateGroupAvatar(ctx.request, payload.UpdateGroupAvatar)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroupMembers) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroupMembers(ctx.request, payload.QueryGroupMembers)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_RemoveGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleRemoveGroupMember(ctx.request, payload.RemoveGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryJoinedGroups) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryJoinedGroups(ctx.request, payload.QueryJoinedGroups)
	})
}
