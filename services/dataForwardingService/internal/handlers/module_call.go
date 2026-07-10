package handlers

import (
	callpb "Betterfly2/proto/call"
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
)

func init() {
	registerDFRequestModule(registerCallRequestModule)
}

func registerCallRequestModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_CallRequest) (dfRequestResult, error) {
		payload, err := authenticatedPayload(ctx.fromID, ctx.message, "操作通话", "call_request", (*pb.RequestMessage).GetCallRequest)
		if err != nil {
			return dfRequestResult{}, err
		}
		request := &callpb.InternalRequest{
			FromKafkaTopic: currentContainerTopic(),
			UserId:         ctx.fromID,
			Request:        payload,
		}
		if err := publishCallRequest(request); err != nil {
			return dfRequestResult{}, err
		}
		logger.Sugar().Debugf("通话请求已发送到callService: user_id=%d", ctx.fromID)
		return dfRequestResult{}, nil
	})
}
