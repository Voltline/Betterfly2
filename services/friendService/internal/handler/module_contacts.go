package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerFriendRequestModule(registerFriendContactModule)
}

func registerFriendContactModule(router *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]) {
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_AddDirectFriend) (*friend.ResponseMessage, error) {
		return ctx.handler.handleAddDirectFriendWithDB(ctx.database, ctx.request, payload.AddDirectFriend)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryFriendList) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryFriendListWithDB(ctx.database, ctx.request, payload.QueryFriendList)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_RemoveDirectFriend) (*friend.ResponseMessage, error) {
		return ctx.handler.handleRemoveDirectFriendWithDB(ctx.database, ctx.request, payload.RemoveDirectFriend)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateFriendAlias) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateFriendAliasWithDB(ctx.database, ctx.request, payload.UpdateFriendAlias)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateFriendNotify) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateFriendNotifyWithDB(ctx.database, ctx.request, payload.UpdateFriendNotify)
	})
}
