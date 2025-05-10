package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"google.golang.org/grpc"
	"net"
	"os"
)

func main() {
	sugar := logger.Sugar()
	defer logger.Sync()
	sugar.Infoln("auth服务启动中...")
	db.DB(&db.User{}) // 初始化数据库

	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		sugar.Fatalln("监听端口失败:", port, ":", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterAuthServiceServer(grpcServer, &AuthService{})

	sugar.Infoln("auth服务启动成功！端口:", port)
	if err := grpcServer.Serve(lis); err != nil {
		sugar.Fatalln("服务中断:", err)
	}
}
