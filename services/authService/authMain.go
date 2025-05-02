package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"authService/config"
	"google.golang.org/grpc"
	"net"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	sugar.Infoln("auth服务启动中...")
	db.DB() // 手动连接一次数据库 非必须

	lis, err := net.Listen("tcp", config.Port)
	if err != nil {
		sugar.Fatalln("监听端口失败", config.Port, ":", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterAuthServiceServer(grpcServer, &AuthService{})

	sugar.Infoln("auth服务启动成功！端口", config.Port)
	if err := grpcServer.Serve(lis); err != nil {
		sugar.Fatalln("服务中断:", err)
	}
}
