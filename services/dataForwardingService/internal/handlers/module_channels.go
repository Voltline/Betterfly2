package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerDFRequestModule(func(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
		dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_ChannelRequest) (dfRequestResult, error) {
			request, err := authenticatedPayload(ctx.fromID, ctx.message, "操作频道", "channel_request", (*pb.RequestMessage).GetChannelRequest)
			if err != nil {
				return dfRequestResult{}, err
			}
			req := newFriendRequest(currentContainerTopic(), ctx.fromID)
			req.Payload = &friend.RequestMessage_ChannelRequest{ChannelRequest: request}
			return dfRequestResult{}, publishFriendRequest(req)
		})
		dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_QueryChannelHistory) (dfRequestResult, error) {
			request, err := authenticatedPayload(ctx.fromID, ctx.message, "查询频道历史", "query_channel_history", (*pb.RequestMessage).GetQueryChannelHistory)
			if err != nil {
				return dfRequestResult{}, err
			}
			req := newStorageRequest(currentContainerTopic(), ctx.fromID)
			req.Payload = &storage.RequestMessage_QueryChannelHistory{QueryChannelHistory: request}
			return dfRequestResult{}, publishStorageRequest(req)
		})
	})
}
