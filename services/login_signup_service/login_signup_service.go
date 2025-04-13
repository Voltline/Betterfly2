package main

import (
	pb "Betterfly2/proto/server_rpc/login_signup"
	"Betterfly2/shared/db_op"
	"Betterfly2/shared/logger"
	"context"
	"golang.org/x/crypto/bcrypt"
)

type LoginSignupService struct {
	pb.LoginSignupServiceServer
}

func (*LoginSignupService) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	userId := req.GetUserId()
	password := req.GetPassword()
	jwt := req.GetJwt()
	result := pb.LoginResult_LOGIN_OK

	logger.Sugar().Infof("RPC-LoginReq { userId:%d, jwt:%s }", userId, jwt)

	for {
		user, err := db_op.GetUserById(userId)
		if err != nil {
			result = pb.LoginResult_ACCOUNT_NOT_EXIST
			break
		}

		// 密码验证
		if jwt == "" || user.JwtKey == "" {
			err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
			if err != nil {
				result = pb.LoginResult_PASSWORD_ERROR
				break
			}
			//TODO: 生成jwt
		}

		//TODO: jwt验证

		break
	}
	return &pb.LoginRsp{
		Result: result,
		UserId: userId,
		Jwt:    jwt,
	}, nil
}

func (*LoginSignupService) Signup(ctx context.Context, rsp *pb.SignupReq) (*pb.SignupRsp, error) {
	return nil, nil
}
