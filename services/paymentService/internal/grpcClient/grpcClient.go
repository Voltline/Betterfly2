package grpcClient

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/logger"
	"context"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	client  pb.AuthServiceClient
	conn    *grpc.ClientConn
	once    sync.Once
	initErr error
)

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

func CloseConn() {
	if conn != nil {
		_ = conn.Close()
		logger.Sugar().Info("gRPC连接已关闭")
	}
}

func ValidateJWT(userID int64, jwt string) (*pb.CheckJwtRsp, error) {
	rpcClient, err := GetAuthClient()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return rpcClient.CheckJwt(ctx, &pb.CheckJwtReq{
		UserId: userID,
		Jwt:    jwt,
	})
}
