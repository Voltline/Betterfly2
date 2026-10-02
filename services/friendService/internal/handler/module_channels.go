package handler

import (
	channel "Betterfly2/proto/channel"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"errors"
	"strings"
)

func init() {
	registerFriendRequestModule(func(router *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]) {
		dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_ChannelRequest) (*friend.ResponseMessage, error) {
			return handleChannelRequest(ctx, payload.ChannelRequest)
		})
	})
}

func handleChannelRequest(ctx friendRequestContext, request *channel.ChannelRequest) (*friend.ResponseMessage, error) {
	response := &channel.ChannelResponse{RequestId: request.GetRequestId()}
	actorID := ctx.request.GetTargetUserId()
	var view *db.ChannelView
	var err error
	if request == nil || request.Payload == nil || actorID <= 0 || len(request.GetRequestId()) > 128 {
		err = db.ErrChannelInvalidArgument
	} else {
		switch payload := request.Payload.(type) {
		case *channel.ChannelRequest_Create:
			response.Operation = "create_channel"
			p := payload.Create
			if !validChannelVisibility(p.GetVisibility()) {
				err = db.ErrChannelInvalidArgument
				break
			}
			view, err = db.CreateChannelWithDB(ctx.database, actorID, p.GetChannelId(), p.GetName(), p.GetDescription(), p.GetAvatarHash(), p.GetUsername(), p.GetVisibility() == channel.Visibility_PUBLIC)
		case *channel.ChannelRequest_Update:
			response.Operation = "update_channel"
			p := payload.Update
			if p == nil {
				err = db.ErrChannelInvalidArgument
				break
			}
			groupUpdates, settingsUpdates := map[string]any{}, map[string]any{}
			if p.GetChannelId() <= 0 {
				err = db.ErrChannelInvalidArgument
			}
			if p.Name != nil {
				name := strings.TrimSpace(p.GetName())
				if name == "" || !db.ValidChannelText(name, 100) {
					err = db.ErrChannelInvalidArgument
				}
				groupUpdates["name"] = name
			}
			if p.Description != nil {
				if !db.ValidChannelText(p.GetDescription(), 1000) {
					err = db.ErrChannelInvalidArgument
				}
				settingsUpdates["description"] = strings.TrimSpace(p.GetDescription())
			}
			if p.AvatarHash != nil {
				if !db.ValidChannelText(p.GetAvatarHash(), 255) {
					err = db.ErrChannelInvalidArgument
				}
				groupUpdates["avatar"] = strings.TrimSpace(p.GetAvatarHash())
			}
			if p.Username != nil {
				var username string
				var usernameErr error
				username, usernameErr = db.NormalizeChannelUsername(p.GetUsername())
				if usernameErr != nil {
					err = usernameErr
				}
				settingsUpdates["username"] = nil
				if username != "" {
					settingsUpdates["username"] = username
				}
			}
			if p.Visibility != nil {
				if !validChannelVisibility(p.GetVisibility()) {
					err = db.ErrChannelInvalidArgument
				}
				settingsUpdates["is_public"] = p.GetVisibility() == channel.Visibility_PUBLIC
			}
			if len(groupUpdates)+len(settingsUpdates) == 0 {
				err = db.ErrChannelInvalidArgument
			}
			if err == nil {
				view, err = db.UpdateChannelWithDB(ctx.database, actorID, p.GetChannelId(), groupUpdates, settingsUpdates)
			}
		case *channel.ChannelRequest_Get:
			response.Operation = "get_channel"
			p := payload.Get
			var username string
			username, err = db.NormalizeChannelUsername(p.GetUsername())
			if err == nil {
				view, err = db.GetChannelWithDB(ctx.database, actorID, p.GetChannelId(), username)
			}
		case *channel.ChannelRequest_Search:
			response.Operation = "search_channels"
			p := payload.Search
			err = listChannelResponse(ctx, response, false, p.GetQuery(), p.GetCursorChannelId(), p.GetPageSize())
		case *channel.ChannelRequest_ListSubscribed:
			response.Operation = "list_subscribed_channels"
			p := payload.ListSubscribed
			err = listChannelResponse(ctx, response, true, "", p.GetCursorChannelId(), p.GetPageSize())
		case *channel.ChannelRequest_Subscribe:
			response.Operation = "subscribe_channel"
			view, err = db.SubscribeChannelWithDB(ctx.database, actorID, payload.Subscribe.GetChannelId())
		case *channel.ChannelRequest_Unsubscribe:
			response.Operation = "unsubscribe_channel"
			err = db.UnsubscribeChannelWithDB(ctx.database, actorID, payload.Unsubscribe.GetChannelId())
		case *channel.ChannelRequest_Delete:
			response.Operation = "delete_channel"
			err = db.DeleteChannelWithDB(ctx.database, actorID, payload.Delete.GetChannelId())
		case *channel.ChannelRequest_SetPin:
			response.Operation = "set_channel_pin"
			view, err = db.SetChannelPinWithDB(ctx.database, actorID, payload.SetPin.GetChannelId(), payload.SetPin.GetMessageId())
		case *channel.ChannelRequest_SetDiscussionGroup:
			response.Operation = "set_discussion_group"
			p := payload.SetDiscussionGroup
			view, err = db.SetDiscussionGroupWithDB(ctx.database, actorID, p.GetChannelId(), p.GetGroupId())
		case *channel.ChannelRequest_ListMembers:
			response.Operation = "list_channel_members"
			p := payload.ListMembers
			var size int
			size, err = db.ChannelPageSize(p.GetPageSize())
			if p.GetChannelId() <= 0 || p.GetCursorUserId() < 0 {
				err = db.ErrChannelInvalidArgument
			}
			if err == nil {
				var members []db.GroupMemberContact
				members, err = db.ListChannelMembersWithDB(ctx.database, actorID, p.GetChannelId(), p.GetCursorUserId(), size)
				if err == nil {
					response.HasMore = len(members) > size
					if response.HasMore {
						members = members[:size]
					}
					for _, member := range members {
						response.Members = append(response.Members, &channel.ChannelMember{UserId: member.UserID, Name: member.Name, AvatarHash: member.Avatar, Role: member.Role, JoinedAt: member.JoinedAt})
						response.NextCursorId = member.UserID
					}
				}
			}
		default:
			err = db.ErrChannelInvalidArgument
		}
	}
	if err != nil {
		var recognized bool
		response.Result, recognized = channelDomainResult(err)
		if !recognized {
			return nil, err // Infrastructure failures must roll back/retry, not cache SERVICE_ERROR.
		}
	} else if view != nil {
		response.Channel = channelInfo(view)
	}
	return &friend.ResponseMessage{Result: friend.FriendResult_FRIEND_OK, TargetUserId: actorID, Payload: &friend.ResponseMessage_ChannelResponse{ChannelResponse: response}}, nil
}

func validChannelVisibility(value channel.Visibility) bool {
	return value == channel.Visibility_PUBLIC || value == channel.Visibility_PRIVATE
}

func channelDomainResult(err error) (channel.ChannelResult, bool) {
	switch {
	case errors.Is(err, db.ErrChannelInvalidArgument):
		return channel.ChannelResult_CHANNEL_INVALID_ARGUMENT, true
	case errors.Is(err, db.ErrChannelNotFound):
		return channel.ChannelResult_CHANNEL_NOT_FOUND, true
	case errors.Is(err, db.ErrChannelForbidden):
		return channel.ChannelResult_CHANNEL_FORBIDDEN, true
	case errors.Is(err, db.ErrChannelAlreadyExists):
		return channel.ChannelResult_CHANNEL_ALREADY_EXISTS, true
	case errors.Is(err, db.ErrChannelInvalidState):
		return channel.ChannelResult_CHANNEL_INVALID_STATE, true
	default:
		return channel.ChannelResult_CHANNEL_SERVICE_ERROR, false
	}
}

func channelInfo(view *db.ChannelView) *channel.ChannelInfo {
	visibility := channel.Visibility_PRIVATE
	if view.IsPublic {
		visibility = channel.Visibility_PUBLIC
	}
	return &channel.ChannelInfo{ChannelId: view.GroupID, Name: view.Name, Description: view.Description, AvatarHash: view.Avatar, Username: view.Username, Visibility: visibility, OwnerUserId: view.OwnerUserID, SubscriberCount: view.SubscriberCount, Subscribed: view.Subscribed, MyRole: view.MyRole, UpdateTime: view.UpdateTime, PinnedMessageId: view.PinnedMessageID, NotificationsMuted: view.NotificationsMuted, DiscussionGroupId: view.DiscussionGroupID}
}

func listChannelResponse(ctx friendRequestContext, response *channel.ChannelResponse, subscribedOnly bool, search string, cursor int64, requestedSize int32) error {
	size, err := db.ChannelPageSize(requestedSize)
	if err != nil {
		return err
	}
	views, err := db.ListChannelsWithDB(ctx.database, ctx.request.GetTargetUserId(), subscribedOnly, search, cursor, size)
	if err != nil {
		return err
	}
	response.HasMore = len(views) > size
	if response.HasMore {
		views = views[:size]
	}
	for i := range views {
		response.Channels = append(response.Channels, channelInfo(&views[i]))
		response.NextCursorId = views[i].GroupID
	}
	return nil
}
