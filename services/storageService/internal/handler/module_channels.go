package handler

import (
	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"errors"
)

func init() {
	registerStorageRequestModule(func(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
		dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_QueryChannelHistory) (*storage.ResponseMessage, error) {
			request := payload.QueryChannelHistory
			response := &channel.ChannelResponse{RequestId: request.GetRequestId(), Operation: "query_channel_history"}
			size, err := db.ChannelPageSize(request.GetPageSize())
			if ctx.request.GetTargetUserId() <= 0 || request.GetChannelId() <= 0 || request.GetBeforeMessageId() < 0 || len(request.GetRequestId()) > 128 {
				err = db.ErrChannelInvalidArgument
			}
			if err == nil {
				_, messages, queryErr := db.GetChannelHistoryWithDB(ctx.database, ctx.request.GetTargetUserId(), request.GetChannelId(), request.GetBeforeMessageId(), size)
				err = queryErr
				if err == nil {
					response.HasMore = len(messages) > size
					if response.HasMore {
						messages = messages[:size]
					}
					for _, message := range messages {
						post := &channel.ChannelPost{MessageId: message.MessageID, AuthorUserId: message.FromUserID, Content: message.Content, Caption: message.Caption, MsgType: message.MessageType, RealFileName: message.RealFileName, Timestamp: message.Timestamp, IsRecalled: message.IsRecalled, RecalledAt: message.RecalledAt, RecalledBy: message.RecalledBy}
						if message.IsRecalled {
							post.Content, post.Caption, post.RealFileName = "", "", ""
						}
						response.Posts = append(response.Posts, post)
						response.NextBeforeMessageId = message.MessageID
					}
				}
			}
			if err != nil {
				switch {
				case errors.Is(err, db.ErrChannelInvalidArgument):
					response.Result = channel.ChannelResult_CHANNEL_INVALID_ARGUMENT
				case errors.Is(err, db.ErrChannelNotFound):
					response.Result = channel.ChannelResult_CHANNEL_NOT_FOUND
				default:
					return nil, err
				}
			}
			return &storage.ResponseMessage{Result: storage.StorageResult_OK, TargetUserId: ctx.request.GetTargetUserId(), Payload: &storage.ResponseMessage_ChannelResponse{ChannelResponse: response}}, nil
		})
	})
}
