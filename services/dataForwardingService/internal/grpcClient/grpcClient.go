package grpcClient

import (
	"Betterfly2/shared/logger"
	"context"
	"google.golang.org/grpc/credentials/insecure"
	"os"
	"sync"
	"time"

	pb "Betterfly2/proto/server_rpc/auth"
	"google.golang.org/grpc"
)

var (
	client pb.AuthServiceClient
	conn   *grpc.ClientConn
	mu     sync.Mutex
	// dialTimeout 测试可缩短。
	dialTimeout = 5 * time.Second
)

// GetAuthClient 单例获取 AuthServiceClient。连接失败不缓存，下次调用重新连接，
// 否则首次调用时auth恰好不可用，之后的登录和请求都会失败到本服务重启。
func GetAuthClient() (pb.AuthServiceClient, error) {
	mu.Lock()
	defer mu.Unlock()
	if client != nil {
		return client, nil
	}
	authRPCAddr := os.Getenv("AUTH_RPC_ADDR")
	if authRPCAddr == "" {
		authRPCAddr = "localhost:50051"
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	dialed, err := grpc.DialContext(
		ctx,
		authRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		logger.Sugar().Errorf("gRPC 连接失败: %v", err)
		return nil, err
	}
	conn, client = dialed, pb.NewAuthServiceClient(dialed)
	return client, nil
}

// CloseConn 关闭AuthServiceClient
func CloseConn() {
	mu.Lock()
	defer mu.Unlock()
	if conn != nil {
		conn.Close()
		conn, client = nil, nil
	}
}
