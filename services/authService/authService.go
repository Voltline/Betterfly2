package main

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"authService/config"
	"authService/internal/utils"
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	pb.AuthServiceServer
}

func (*AuthService) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	account := req.GetAccount()
	password := req.GetPassword()
	jwt := req.GetJwt()
	result := pb.AuthResult_OK

	logger.Sugar().Debugf("RPC-LoginReq { account:%s, has_jwt:%t }", account, jwt != "")
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
			if err = db.UpdateUserJwtKeyById(user.ID, user.JwtKey); err != nil {
				logger.Sugar().Errorln(userBriefStr(user), "failed to persist jwt key:", err)
				user.JwtKey = nil
				result = pb.AuthResult_SERVICE_ERROR
				goto RETURN
			}
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
		if len(user.JwtKey) == 0 {
			result = pb.AuthResult_JWT_ERROR
			jwt = ""
			logger.Sugar().Warnln(userBriefStr(user), "rejected jwt login without a user signing key")
			goto RETURN
		}
		claim, validateErr := utils.ValidateJWT(jwt, user.JwtKey)
		if validateErr != nil || claim.ID != user.ID || claim.Account != user.Account {
			result = pb.AuthResult_JWT_ERROR
			jwt = ""
			logger.Sugar().Warnln(userBriefStr(user), "failed to validate jwt:", validateErr)
			goto RETURN
		}
		newJwt, err := utils.GenerateJWT(user)
		if err != nil {
			logger.Sugar().Errorln(userBriefStr(user), "failed to generate jwt key:", err)
			result = pb.AuthResult_SERVICE_ERROR
			jwt = ""
		} else {
			jwt = newJwt
			logger.Sugar().Infoln(userBriefStr(user), "login success with jwt")
		}
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
	if len(account) > db.MaxNameLen {
		result = pb.AuthResult_ACCOUNT_TOO_LONG
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
		PasswordHash: string(passwordHash),
	}
	err = db.AddUser(user)
	if err != nil {
		if errors.Is(err, db.ErrAccountExists) {
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

	logger.Sugar().Debugf("RPC-CheckJwtReq { userID:%d, has_jwt:%t }", userID, jwt != "")

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

func (*AuthService) ChangePassword(_ context.Context, req *pb.ChangePasswordReq) (*pb.ChangePasswordRsp, error) {
	user, result := authenticateUser(req.GetUserId(), req.GetJwt())
	if result != pb.AuthResult_OK {
		return &pb.ChangePasswordRsp{Result: result}, nil
	}

	newPassword := []byte(req.GetNewPassword())
	if len(newPassword) < 8 {
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_PASSWORD_TOO_SHORT}, nil
	}
	if len(newPassword) > 72 {
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_PASSWORD_TOO_LONG}, nil
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.GetOldPassword())) != nil {
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_PASSWORD_ERROR}, nil
	}

	passwordHash, err := bcrypt.GenerateFromPassword(newPassword, bcrypt.DefaultCost)
	if err != nil {
		logger.Sugar().Errorw("修改密码时生成哈希失败", "user_id", user.ID, "error", err)
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}
	jwtKey, err := newJWTKey()
	if err != nil {
		logger.Sugar().Errorw("修改密码时生成签名密钥失败", "user_id", user.ID, "error", err)
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}

	updated, err := db.UpdateUserCredentialsCAS(user.ID, user.PasswordHash, user.JwtKey, string(passwordHash), jwtKey)
	if err != nil {
		logger.Sugar().Errorw("修改密码时更新用户凭据失败", "user_id", user.ID, "error", err)
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}
	if !updated {
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_JWT_ERROR}, nil
	}

	newJWT, err := utils.GenerateJWT(&db.User{ID: user.ID, Account: user.Account, JwtKey: jwtKey})
	if err != nil {
		logger.Sugar().Errorw("修改密码后签发JWT失败", "user_id", user.ID, "error", err)
		return &pb.ChangePasswordRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}
	logger.Sugar().Infow("用户密码修改成功", "user_id", user.ID)
	return &pb.ChangePasswordRsp{Result: pb.AuthResult_OK, Jwt: newJWT}, nil
}

func (*AuthService) RevokeSessions(_ context.Context, req *pb.RevokeSessionsReq) (*pb.RevokeSessionsRsp, error) {
	user, result := authenticateUser(req.GetUserId(), req.GetJwt())
	if result != pb.AuthResult_OK {
		return &pb.RevokeSessionsRsp{Result: result}, nil
	}

	jwtKey, err := newJWTKey()
	if err != nil {
		logger.Sugar().Errorw("撤销会话时生成签名密钥失败", "user_id", user.ID, "error", err)
		return &pb.RevokeSessionsRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}
	updated, err := db.RotateUserJwtKeyCAS(user.ID, user.JwtKey, jwtKey)
	if err != nil {
		logger.Sugar().Errorw("撤销会话时更新签名密钥失败", "user_id", user.ID, "error", err)
		return &pb.RevokeSessionsRsp{Result: pb.AuthResult_SERVICE_ERROR}, nil
	}
	if !updated {
		return &pb.RevokeSessionsRsp{Result: pb.AuthResult_JWT_ERROR}, nil
	}
	logger.Sugar().Infow("用户会话已全部撤销", "user_id", user.ID)
	return &pb.RevokeSessionsRsp{Result: pb.AuthResult_OK}, nil
}

func authenticateUser(userID int64, jwt string) (*db.User, pb.AuthResult) {
	if userID <= 0 || jwt == "" {
		return nil, pb.AuthResult_JWT_ERROR
	}
	user, err := db.GetUserById(userID)
	if err != nil {
		logger.Sugar().Errorw("账号安全操作读取用户失败", "user_id", userID, "error", err)
		return nil, pb.AuthResult_SERVICE_ERROR
	}
	if user == nil || len(user.JwtKey) == 0 {
		return nil, pb.AuthResult_JWT_ERROR
	}
	claims, err := utils.ValidateJWT(jwt, user.JwtKey)
	if err != nil || claims.ID != user.ID || claims.Account != user.Account {
		return nil, pb.AuthResult_JWT_ERROR
	}
	return user, pb.AuthResult_OK
}

func newJWTKey() ([]byte, error) {
	key := make([]byte, config.JwtKeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func userBriefStr(user *db.User) string {
	return fmt.Sprintf("user%d[%s]", user.ID, user.Account)
}
