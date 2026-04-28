package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
)

func init() {
	registerDFRequestModule(registerSessionRequestModule)
}

func registerSessionRequestModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(_ dfRequestContext, _ *pb.RequestMessage_Logout) (dfRequestResult, error) {
		logger.Sugar().Infof("用户登出")
		return dfRequestResult{code: 1}, nil
	})
	dispatch.Register(router, func(_ dfRequestContext, payload *pb.RequestMessage_Login) (dfRequestResult, error) {
		logger.Sugar().Warnf("收到认证服务请求，不处理：%+v", payload)
		return dfRequestResult{}, nil
	})
	dispatch.Register(router, func(_ dfRequestContext, payload *pb.RequestMessage_Signup) (dfRequestResult, error) {
		logger.Sugar().Warnf("收到认证服务请求，不处理：%+v", payload)
		return dfRequestResult{}, nil
	})
}
