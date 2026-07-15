package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/dispatch"
)

func init() { registerFriendRequestModule(registerRelationshipModule) }

func registerRelationshipModule(router *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]) {
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryFriendRequests) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryFriendRequestsWithDB(ctx.database, ctx.request, payload.QueryFriendRequests)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_ResolveFriendRequest) (*friend.ResponseMessage, error) {
		return ctx.handler.handleResolveFriendRequestWithDB(ctx.database, ctx.request, payload.ResolveFriendRequest)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroupJoinRequests) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroupJoinRequestsWithDB(ctx.database, ctx.request, payload.QueryGroupJoinRequests)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_ResolveGroupJoinRequest) (*friend.ResponseMessage, error) {
		return ctx.handler.handleResolveGroupJoinRequestWithDB(ctx.database, ctx.request, payload.ResolveGroupJoinRequest)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_InviteGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleInviteGroupMemberWithDB(ctx.database, ctx.request, payload.InviteGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_QueryGroupInvitations) (*friend.ResponseMessage, error) {
		return ctx.handler.handleQueryGroupInvitationsWithDB(ctx.database, ctx.request, payload.QueryGroupInvitations)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_ResolveGroupInvitation) (*friend.ResponseMessage, error) {
		return ctx.handler.handleResolveGroupInvitationWithDB(ctx.database, ctx.request, payload.ResolveGroupInvitation)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_KickGroupMember) (*friend.ResponseMessage, error) {
		return ctx.handler.handleKickGroupMemberWithDB(ctx.database, ctx.request, payload.KickGroupMember)
	})
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_UpdateGroupMemberRole) (*friend.ResponseMessage, error) {
		return ctx.handler.handleUpdateGroupMemberRoleWithDB(ctx.database, ctx.request, payload.UpdateGroupMemberRole)
	})
}
