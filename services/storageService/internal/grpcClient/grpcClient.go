package grpcClient

import (
	"Betterfly2/shared/logger"
	"context"
	"os"
	"sync"
	"time"

	pb "Betterfly2/proto/server_rpc/auth"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	client  pb.AuthServiceClient
	conn    *grpc.ClientConn
	once    sync.Once
	initErr error
)

// GetAuthClient 单例获取 AuthServiceClient
func GetAuthClient() (pb.AuthServiceClient, error) {
	once.Do(func() {
		authRPCAddr := os.Getenv("AUTH_RPC_ADDR")
		if authRPCAddr == "" {
			authRPCAddr = "localhost:50051"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, initErr = grpc.DialContext(
			ctx,
			authRPCAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if initErr != nil {
			logger.Sugar().Errorf("gRPC 连接失败: %v", initErr)
			return
		}
		client = pb.NewAuthServiceClient(conn)
		logger.Sugar().Infof("gRPC客户端连接成功: %s", authRPCAddr)
	})
	return client, initErr
}

// CloseConn 关闭AuthServiceClient连接
func CloseConn() {
	if conn != nil {
		conn.Close()
		logger.Sugar().Info("gRPC连接已关闭")
	}
}

// ValidateJWT 验证JWT并返回用户信息
func ValidateJWT(userID int64, jwt string) (*pb.CheckJwtRsp, error) {
	rpcClient, err := GetAuthClient()
	if err != nil {
		return nil, err
	}

	checkJWTReq := &pb.CheckJwtReq{
		UserId: userID,
		Jwt:    jwt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	checkJWTRsp, err := rpcClient.CheckJwt(ctx, checkJWTReq)
	if err != nil {
		return nil, err
	}

	return checkJWTRsp, nil
}
