package handler

import (
	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"errors"
)

func channelPost(message *db.Message) *channel.ChannelPost {
	post := &channel.ChannelPost{MessageId: message.MessageID, AuthorUserId: message.FromUserID, Content: message.Content, Caption: message.Caption, MsgType: message.MessageType, RealFileName: message.RealFileName, Timestamp: message.Timestamp,
		IsRecalled: message.IsRecalled, RecalledAt: message.RecalledAt, RecalledBy: message.RecalledBy, ReplyToMessageId: message.ReplyToMessageID, DiscussionRootMessageId: message.DiscussionRootMessageID, SourceChannelMessageId: message.SourceChannelMessageID}
	if message.IsRecalled {
		post.Content, post.Caption, post.RealFileName = "", "", ""
	}
	return post
}

func discussionInfo(view *db.DiscussionView) *channel.DiscussionInfo {
	return &channel.DiscussionInfo{ChannelId: view.Post.ToUserID, PostMessageId: view.Post.MessageID, GroupId: view.Root.ToUserID, RootMessageId: view.Root.MessageID, Joined: view.Joined, CanComment: view.Joined && !view.Closed, Closed: view.Closed, ReplyCount: view.ReplyCount, Post: channelPost(&view.Post)}
}

func discussionResponse(ctx storageRequestContext, response *channel.ChannelResponse, err error) (*storage.ResponseMessage, error) {
	if err != nil {
		switch {
		case errors.Is(err, db.ErrChannelNotFound):
			response.Result = channel.ChannelResult_CHANNEL_NOT_FOUND
		case errors.Is(err, db.ErrChannelForbidden):
			response.Result = channel.ChannelResult_CHANNEL_FORBIDDEN
		case errors.Is(err, db.ErrChannelInvalidArgument):
			response.Result = channel.ChannelResult_CHANNEL_INVALID_ARGUMENT
		default:
			return nil, err
		}
	}
	return &storage.ResponseMessage{TargetUserId: ctx.request.GetTargetUserId(), Payload: &storage.ResponseMessage_ChannelResponse{ChannelResponse: response}}, nil
}

func init() {
	registerStorageRequestModule(func(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
		dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_GetDiscussion) (*storage.ResponseMessage, error) {
			r := payload.GetDiscussion
			response := &channel.ChannelResponse{RequestId: r.GetRequestId(), Operation: "get_discussion"}
			if ctx.request.GetTargetUserId() <= 0 || r.GetChannelId() <= 0 || r.GetPostMessageId() <= 0 || len(r.GetRequestId()) > 128 {
				return discussionResponse(ctx, response, db.ErrChannelInvalidArgument)
			}
			if _, err := db.GetChannelWithDB(ctx.database, ctx.request.GetTargetUserId(), r.GetChannelId(), ""); err != nil {
				return discussionResponse(ctx, response, err)
			}
			post, err := db.GetMessageByIDWithDB(ctx.database, r.GetPostMessageId())
			if err != nil {
				return nil, err
			}
			if post == nil || !post.IsGroup || post.ToUserID != r.GetChannelId() || post.DiscussionRootMessageID <= 0 {
				return discussionResponse(ctx, response, db.ErrChannelNotFound)
			}
			view, err := db.GetDiscussionWithDB(ctx.database, ctx.request.GetTargetUserId(), post.DiscussionRootMessageID)
			if err == nil {
				response.Discussion = discussionInfo(view)
			}
			return discussionResponse(ctx, response, err)
		})
		dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryDiscussionReplies) (*storage.ResponseMessage, error) {
			r := payload.QueryDiscussionReplies
			response := &channel.ChannelResponse{RequestId: r.GetRequestId(), Operation: "query_discussion_replies"}
			size, err := db.ChannelPageSize(r.GetPageSize())
			if err != nil || ctx.request.GetTargetUserId() <= 0 || r.GetRootMessageId() <= 0 || r.GetBeforeMessageId() < 0 || len(r.GetRequestId()) > 128 {
				return discussionResponse(ctx, response, db.ErrChannelInvalidArgument)
			}
			view, err := db.GetDiscussionWithDB(ctx.database, ctx.request.GetTargetUserId(), r.GetRootMessageId())
			if err != nil {
				return discussionResponse(ctx, response, err)
			}
			if !view.Joined {
				return discussionResponse(ctx, response, db.ErrChannelForbidden)
			}
			response.Discussion = discussionInfo(view)
			var messages []db.Message
			query := ctx.database.Where("to_user_id = ? AND is_group = TRUE AND discussion_root_message_id = ? AND source_channel_message_id = 0", view.Root.ToUserID, view.Root.MessageID)
			if r.GetBeforeMessageId() > 0 {
				query = query.Where("message_id < ?", r.GetBeforeMessageId())
			}
			if err := query.Order("message_id DESC").Limit(size + 1).Find(&messages).Error; err != nil {
				return nil, err
			}
			response.HasMore = len(messages) > size
			if response.HasMore {
				messages = messages[:size]
			}
			for _, m := range messages {
				response.Posts = append(response.Posts, channelPost(&m))
				response.NextBeforeMessageId = m.MessageID
			}
			return discussionResponse(ctx, response, nil)
		})
	})
}
