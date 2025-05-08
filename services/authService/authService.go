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
	"strconv"
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

	logger.Sugar().Debugf("RPC-LoginReq { account:%s, jwt:%s }", account, jwt)
	user := &db.User{}
	err := error(nil)

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

	if jwt == "" { // 密码验证
		err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
		if err != nil {
			logger.Sugar().Warnln(userBriefStr(user), "password error")
			result = pb.AuthResult_PASSWORD_ERROR
			goto RETURN
		}

		if len(user.JwtKey) == 0 { // 生成jwt key
			user.JwtKey = make([]byte, config.JwtKeyLength)
			_, err = rand.Read(user.JwtKey)
			if err != nil {
				logger.Sugar().Errorln(userBriefStr(user), "failed to generate jwt key:", err)
				result = pb.AuthResult_SERVICE_ERROR
				goto RETURN
			}
			db.UpdateUserJwtKeyById(user.ID, user.JwtKey)
		}

		// 生成jwt
		jwt, err = utils.GenerateJWT(user)
		if err != nil {
			logger.Sugar().Errorln(userBriefStr(user), "failed to generate jwt with key:", err)
			jwt = ""
			result = pb.AuthResult_SERVICE_ERROR
			goto RETURN
		}
		logger.Sugar().Infoln(userBriefStr(user), "login success with password")

	} else { // jwt验证
		_, err = utils.ValidateJWT(jwt, user.JwtKey)
		if err != nil {
			result = pb.AuthResult_JWT_ERROR
			jwt = ""
			logger.Sugar().Warnln(userBriefStr(user), "failed to validate jwt:", err)
		}
		newJwt, err := utils.GenerateJWT(user)
		if err != nil {
			logger.Sugar().Errorln(userBriefStr(user), "failed to generate jwt key:", err)
		} else {
			jwt = newJwt
		}
		logger.Sugar().Infoln(userBriefStr(user), "login success with jwt")
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
	var userID int64 = 0
	var passwordHash []byte
	var user *db.User
	var err error

	logger.Sugar().Debugf("RPC-SignupReq { account:%s }", account)

	if account == "" {
		result = pb.AuthResult_ACCOUNT_EMPTY
		goto RETURN
	}
	if password == "" {
		result = pb.AuthResult_PASSWORD_EMPTY
		goto RETURN
	}

	user, err = db.GetUserByAccount(account)
	if err != nil {
		logger.Sugar().Errorln("fail to get user:", err)
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
		logger.Sugar().Errorln("fail to get user:", err)
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
		if strings.Contains(err.Error(), "duplicate") {
			result = pb.AuthResult_ACCOUNT_EXIST
		} else {
			logger.Sugar().Errorln("fail to get user:", err)
			result = pb.AuthResult_SERVICE_ERROR
		}
		goto RETURN
	}

	userID = user.ID

RETURN:
	return &pb.SignupRsp{
		Result:  result,
		UserId:  userID,
		Account: account,
	}, nil
}

func (*AuthService) CheckJwt(ctx context.Context, rsp *pb.CheckJwtReq) (*pb.CheckJwtRsp, error) {
	result := pb.AuthResult_OK
	userID := rsp.GetUserId()
	jwt := rsp.GetJwt()
	account := ""
	var claim *utils.BetterflyClaims

	logger.Sugar().Debugf("RPC-CheckJwtReq { userID:%s, jwt:%s }", userID, jwt)

	// 获取用户
	user, err := db.GetUserById(userID)
	if err != nil {
		logger.Sugar().Errorln("fail to get user:", err)
		result = pb.AuthResult_SERVICE_ERROR
		goto RETURN
	}
	if user == nil {
		result = pb.AuthResult_ACCOUNT_NOT_EXIST
		goto RETURN
	}
	account = user.Account

	// 验证jwt
	claim, err = utils.ValidateJWT(jwt, user.JwtKey)
	if err != nil || claim.ID != userID || claim.Account != account {
		logger.Sugar().Warnln(userBriefStr(user), "failed to validate jwt:", err)
		result = pb.AuthResult_JWT_ERROR
		goto RETURN
	}

RETURN:
	return &pb.CheckJwtRsp{
		Result:  result,
		UserId:  userID,
		Account: account,
	}, nil

}

func userBriefStr(user *db.User) string {
	return "user" + strconv.FormatInt(user.ID, 10) + "[" + user.Account + "]"
}
