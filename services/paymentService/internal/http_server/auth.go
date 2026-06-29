package http_server

import (
	"context"
	"errors"
	"net/http"
	"paymentService/internal/grpcClient"
	"strconv"
	"strings"

	pb "Betterfly2/proto/server_rpc/auth"
	"Betterfly2/shared/logger"
)

type userContextKey struct{}

type UserInfo struct {
	UserID  int64
	Account string
}

func (s *Server) requireJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userInfo, err := validateJWTFromRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, userInfo)))
	}
}

func validateJWTFromRequest(r *http.Request) (*UserInfo, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return nil, errors.New("missing authorization header")
	}
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, errors.New("invalid authorization header format")
	}

	userIDStr := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if userIDStr == "" {
		userIDStr = strings.TrimSpace(r.URL.Query().Get("user_id"))
	}
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil || userID <= 0 {
		return nil, errors.New("invalid user_id")
	}

	resp, err := grpcClient.ValidateJWT(userID, parts[1])
	if err != nil {
		logger.Sugar().Errorf("支付服务JWT验证失败: %v", err)
		return nil, errors.New("jwt validation failed")
	}
	if resp.Result != pb.AuthResult_OK {
		return nil, errors.New("jwt validation failed")
	}
	return &UserInfo{UserID: resp.UserId, Account: resp.Account}, nil
}

func currentUser(r *http.Request) (*UserInfo, error) {
	userInfo, ok := r.Context().Value(userContextKey{}).(*UserInfo)
	if !ok || userInfo == nil {
		return nil, errors.New("user info not found")
	}
	return userInfo, nil
}
