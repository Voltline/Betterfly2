package http_server

import (
	"Betterfly2/shared/logger"
	"context"
	"net/http"
	"strconv"
	"strings"

	pb "Betterfly2/proto/server_rpc/auth"
	"storageService/internal/grpcClient"
)

// validateJWT 测试可替换为不走gRPC的实现。
var validateJWT = grpcClient.ValidateJWT

// authRetryAfterSeconds 是auth不可用时建议客户端等待的秒数。
const authRetryAfterSeconds = "2"

func shouldBypassJWTAuth(path string) bool {
	switch path {
	case "/health":
		return true
	case "/ready":
		return true
	default:
		return false
	}
}

// JWTAuthMiddleware JWT验证中间件
func JWTAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sugar := logger.Sugar()

		if shouldBypassJWTAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// 从Header获取Authorization
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		// 解析Bearer token
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		jwt := parts[1]

		// 从Header或Query参数获取user_id
		userIDStr := r.Header.Get("X-User-ID")
		if userIDStr == "" {
			userIDStr = r.URL.Query().Get("user_id")
		}

		if userIDStr == "" {
			http.Error(w, "Missing user_id", http.StatusBadRequest)
			return
		}

		userID, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid user_id", http.StatusBadRequest)
			return
		}

		// 通过gRPC验证JWT；auth或数据库暂时不可用不是凭据问题，回503让客户端退避重试而不是重新登录
		checkJWTRsp, err := validateJWT(userID, jwt)
		if err != nil || checkJWTRsp == nil || checkJWTRsp.Result == pb.AuthResult_SERVICE_ERROR {
			sugar.Errorw("JWT验证暂时不可用", "user_id", userID, "result", checkJWTRsp.GetResult(), "error", err)
			w.Header().Set("Retry-After", authRetryAfterSeconds)
			http.Error(w, "Authentication temporarily unavailable", http.StatusServiceUnavailable)
			return
		}

		if checkJWTRsp.Result != pb.AuthResult_OK {
			sugar.Warnf("JWT验证失败: result=%v, user_id=%d", checkJWTRsp.Result, userID)
			http.Error(w, "JWT validation failed", http.StatusUnauthorized)
			return
		}

		sugar.Debugf("JWT验证成功: user_id=%d, account=%s", checkJWTRsp.UserId, checkJWTRsp.Account)

		// 继续处理请求
		next.ServeHTTP(w, r.WithContext(withAuthenticatedUserID(r.Context(), userID)))
	})
}

type authenticatedUserIDKey struct{}

func withAuthenticatedUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, authenticatedUserIDKey{}, userID)
}

// authenticatedUserID 返回中间件校验过JWT的用户ID；未经过中间件时返回false。
func authenticatedUserID(r *http.Request) (int64, bool) {
	userID, ok := r.Context().Value(authenticatedUserIDKey{}).(int64)
	return userID, ok && userID > 0
}
