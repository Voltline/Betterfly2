package grpcClient

import (
	"Betterfly2/shared/logger"
	"google.golang.org/grpc/credentials/insecure"
	"os"
	"sync"
	"time"

	pb "Betterfly2/proto/server_rpc/auth"
	"google.golang.org/grpc"
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
		conn, initErr = grpc.Dial(
			authRPCAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
			grpc.WithTimeout(5*time.Second),
		)
		if initErr != nil {
			logger.Sugar().Errorf("gRPC 连接失败: %v", initErr)
			return
		}
		client = pb.NewAuthServiceClient(conn)
	})
	return client, initErr
}

// CloseConn 关闭AuthServiceClient
func CloseConn() {
	if conn != nil {
		conn.Close()
	}
}
