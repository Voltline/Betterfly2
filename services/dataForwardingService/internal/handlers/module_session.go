package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"context"
	"time"
)

func init() {
	registerDFRequestModule(registerSessionRequestModule)
}

func registerSessionRequestModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_Logout) (dfRequestResult, error) {
		if payload.Logout.GetScope() == pb.LogoutScope_CURRENT_CONNECTION {
			logger.Sugar().Infof("用户登出当前连接: user_id=%d", ctx.fromID)
			return dfRequestResult{code: 1}, nil
		}
		if payload.Logout.GetScope() != pb.LogoutScope_ALL_SESSIONS {
			return logoutWarning("不支持的登出范围"), nil
		}
		if ctx.message.GetJwt() == "" {
			return logoutWarning("JWT验证失败，未撤销会话"), nil
		}

		rpcClient, err := getAuthClient()
		if err != nil {
			return logoutWarning("认证服务暂时不可用，未撤销会话"), nil
		}
		rpcCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := rpcClient.RevokeSessions(rpcCtx, &auth.RevokeSessionsReq{UserId: ctx.fromID, Jwt: ctx.message.GetJwt()})
		if err != nil || response == nil {
			logger.Sugar().Warnw("撤销全部会话失败", "user_id", ctx.fromID, "error", err)
			return logoutWarning("认证服务暂时不可用，未撤销会话"), nil
		}
		if response.GetResult() != auth.AuthResult_OK {
			message := "撤销全部会话失败"
			if response.GetResult() == auth.AuthResult_JWT_ERROR {
				message = "JWT验证失败，未撤销会话"
			}
			return logoutWarning(message), nil
		}
		logger.Sugar().Infof("用户撤销全部会话并登出当前连接: user_id=%d", ctx.fromID)
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

func logoutWarning(message string) dfRequestResult {
	return dfRequestResult{response: &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Warn{Warn: &pb.Warn{WarningMessage: message}},
	}}
}
