package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
)

func init() {
	registerDFRequestModule(registerPushRequestModule)
}

func registerPushRequestModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, _ *pb.RequestMessage_PushRequest) (dfRequestResult, error) {
		payload, err := authenticatedPayload(ctx.fromID, ctx.message, "管理推送设备", "push_request", (*pb.RequestMessage).GetPushRequest)
		if err != nil {
			return dfRequestResult{}, err
		}
		request := &pushpb.RequestMessage{
			Payload: &pushpb.RequestMessage_ClientCommand{
				ClientCommand: &pushpb.ClientCommand{
					FromKafkaTopic: currentContainerTopic(),
					UserId:         ctx.fromID,
					Request:        payload,
				},
			},
		}
		if err := publishPushRequest(request); err != nil {
			return dfRequestResult{}, err
		}
		logger.Sugar().Debugf("推送设备请求已发送到pushService: user_id=%d", ctx.fromID)
		return dfRequestResult{}, nil
	})
}
