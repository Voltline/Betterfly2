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
	client pb.AuthServiceClient
	conn   *grpc.ClientConn
	mu     sync.Mutex
	// dialTimeout 测试可缩短。
	dialTimeout = 5 * time.Second
)

// GetAuthClient 单例获取 AuthServiceClient。连接失败不缓存，下次调用重新连接，
// 否则首次请求时auth恰好不可用，之后每个请求都会失败到本服务重启。
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
	logger.Sugar().Infof("gRPC客户端连接成功: %s", authRPCAddr)
	return client, nil
}

// CloseConn 关闭AuthServiceClient连接
func CloseConn() {
	mu.Lock()
	defer mu.Unlock()
	if conn != nil {
		conn.Close()
		conn, client = nil, nil
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
