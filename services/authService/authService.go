package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"authService/config"
	"authService/internal/utils"
	"context"
	"crypto/rand"
	"golang.org/x/crypto/bcrypt"
	"strings"
	"time"
)

type AuthService struct {
	pb.AuthServiceServer
}

func (*AuthService) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	account := req.GetAccount()
	password := req.GetPassword()
	jwt := req.GetJwt()
	result := pb.AuthResult_OK

	logger.Sugar().Infof("RPC-LoginReq { account:%s, jwt:%s }", account, jwt)
	user := &db.User{}
	err := error(nil)
	userBriefInfo := ""

	user, err = db.GetUserByAccount(account)
	if err != nil {
		result = pb.AuthResult_SERVICE_ERROR
		goto RETURN
	}
	if user == nil {
		user = &db.User{}
		result = pb.AuthResult_ACCOUNT_NOT_EXIST
		goto RETURN
	}
	userBriefInfo = "user" + string(user.ID) + user.Account

	if jwt == "" { // 密码验证
		err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
		if err != nil {
			result = pb.AuthResult_PASSWORD_ERROR
			logger.Sugar().Warnln(userBriefInfo, "password error")
			goto RETURN
		}

		if len(user.JwtKey) == 0 { // 生成jwt key
			user.JwtKey = make([]byte, config.JwtKeyLength)
			_, err = rand.Read(user.JwtKey)
			if err != nil {
				logger.Sugar().Errorln(userBriefInfo, "failed to generate jwt key:", err)
				goto RETURN
			}
			db.UpdateUserJwtKeyById(user.ID, user.JwtKey)
		}

		// 生成jwt
		jwt, err = utils.GenerateJWT(user)
		if err != nil {
			logger.Sugar().Errorln(userBriefInfo, "failed to generate jwt with key:", err)
			jwt = ""
			goto RETURN
		}
		logger.Sugar().Infof(userBriefInfo, "login success with password")

	} else { // jwt验证
		_, err = utils.ValidateJWT(jwt, user.JwtKey)
		if err != nil {
			result = pb.AuthResult_JWT_ERROR
			jwt = ""
			logger.Sugar().Warnln(userBriefInfo, "failed to validate jwt:", err)
		}
		newJwt, err := utils.GenerateJWT(user)
		if err != nil {
			logger.Sugar().Errorln(userBriefInfo, "failed to generate jwt key:", err)
		} else {
			jwt = newJwt
		}
		logger.Sugar().Infof(userBriefInfo, "login success with jwt")
	}

RETURN:
	return &pb.LoginRsp{
		Result:  result,
		UserId:  user.ID,
		Account: account,
		Jwt:     jwt,
	}, nil
}

func (*AuthService) Signup(ctx context.Context, rsp *pb.SignupReq) (*pb.SignupRsp, error) {
	account := rsp.GetAccount()
	password := rsp.GetPassword()
	userName := rsp.GetUserName()
	result := pb.AuthResult_OK
	var id int64 = 0
	var passwordHash []byte

	user, err := db.GetUserByAccount(account)
	if err != nil {
		result = pb.AuthResult_SERVICE_ERROR
		goto RETURN
	}
	if user != nil {
		result = pb.AuthResult_ACCOUNT_EXIST
		goto RETURN
	}

	// 创建用户
	passwordHash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		result = pb.AuthResult_SERVICE_ERROR
		goto RETURN
	}
	user = &db.User{
		Account:      account,
		Name:         userName,
		UpdateTime:   time.Now().UTC().Format(time.RFC3339),
		PasswordHash: string(passwordHash),
	}
	err = db.AddUser(user)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			result = pb.AuthResult_ACCOUNT_EXIST
		} else {
			result = pb.AuthResult_SERVICE_ERROR
		}
		goto RETURN
	}

RETURN:
	return &pb.SignupRsp{
		Result:  result,
		UserId:  id,
		Account: account,
	}, nil
}
