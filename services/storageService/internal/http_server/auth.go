package http_server

import (
	"Betterfly2/shared/logger"
	"net/http"
	"strconv"
	"strings"

	pb "Betterfly2/proto/server_rpc/auth"
	"storageService/internal/grpcClient"
)

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

		// 通过gRPC验证JWT
		checkJWTRsp, err := grpcClient.ValidateJWT(userID, jwt)
		if err != nil {
			sugar.Errorf("JWT验证失败: %v", err)
			http.Error(w, "JWT validation failed", http.StatusUnauthorized)
			return
		}

		if checkJWTRsp.Result != pb.AuthResult_OK {
			sugar.Warnf("JWT验证失败: result=%v, user_id=%d", checkJWTRsp.Result, userID)
			http.Error(w, "JWT validation failed", http.StatusUnauthorized)
			return
		}

		sugar.Debugf("JWT验证成功: user_id=%d, account=%s", checkJWTRsp.UserId, checkJWTRsp.Account)

		// 继续处理请求
		next.ServeHTTP(w, r)
	})
}
