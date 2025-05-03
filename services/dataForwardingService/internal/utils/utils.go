package utils

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"context"
	"data_forwarding_service/internal/grpcClient"
	"errors"
	"strings"
)

// SplitBrokers 解析多个 Kafka broker 地址
func SplitBrokers(broker string) []string {
	// 将逗号分隔的 broker 地址拆分为数组
	return strings.Split(broker, ",")
}

// ValidateAndParseJWT 通过rpc验证并解析JWT
func ValidateAndParseJWT(fromID int64, jwt string) error {
	rpcClient, err := grpcClient.GetAuthClient()
	if err != nil {
		return err
	}

	checkJWTReq := &pb.CheckJwtReq{}
	checkJWTReq.Jwt = jwt
	checkJWTReq.UserId = fromID

	checkJWTRsp, err := rpcClient.CheckJwt(context.Background(), checkJWTReq)
	if err != nil {
		return err
	}

	switch checkJWTRsp.Result {
	case pb.AuthResult_OK:
		return nil
	case pb.AuthResult_JWT_ERROR:
		return errors.New("JWT验证失败")
	default:
		return errors.New("验证错误")
	}
}
