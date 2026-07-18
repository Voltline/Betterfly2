package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	auth "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/logger"
	"context"
	"time"
)

func init() { registerDFRequestModule(registerAccountSecurityModule) }

func registerAccountSecurityModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_ChangePassword) (dfRequestResult, error) {
		response := changePassword(ctx, payload.ChangePassword)
		return dfRequestResult{response: &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_AccountSecurityRsp{AccountSecurityRsp: response},
		}}, nil
	})
}

func changePassword(ctx dfRequestContext, request *pb.ChangePassword) *pb.AccountSecurityRsp {
	response := &pb.AccountSecurityRsp{
		Operation: "change_password",
		Result:    pb.AccountSecurityResult_ACCOUNT_SECURITY_SERVICE_ERROR,
	}
	if request == nil || ctx.fromID <= 0 || ctx.message.GetJwt() == "" {
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_JWT_ERROR
		return response
	}

	rpcClient, err := getAuthClient()
	if err != nil {
		return response
	}
	rpcCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authResponse, err := rpcClient.ChangePassword(rpcCtx, &auth.ChangePasswordReq{
		UserId:      ctx.fromID,
		Jwt:         ctx.message.GetJwt(),
		OldPassword: request.GetOldPassword(),
		NewPassword: request.GetNewPassword(),
	})
	if err != nil || authResponse == nil {
		logger.Sugar().Warnw("修改密码RPC失败", "user_id", ctx.fromID, "error", err)
		return response
	}

	switch authResponse.GetResult() {
	case auth.AuthResult_OK:
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_OK
		response.Jwt = authResponse.GetJwt()
	case auth.AuthResult_PASSWORD_ERROR:
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_OLD_PASSWORD_ERROR
	case auth.AuthResult_JWT_ERROR:
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_JWT_ERROR
	case auth.AuthResult_PASSWORD_TOO_SHORT:
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_PASSWORD_TOO_SHORT
	case auth.AuthResult_PASSWORD_TOO_LONG:
		response.Result = pb.AccountSecurityResult_ACCOUNT_SECURITY_PASSWORD_TOO_LONG
	}
	return response
}
