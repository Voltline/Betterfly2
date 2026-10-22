package handlers

import (
	"Betterfly2/shared/logger"
	"context"
	"time"

	redisClient "data_forwarding_service/internal/redis"
	"github.com/redis/go-redis/v9"
)

// 密码登录与注册的按IP固定窗口限流，限制单一来源的撞库和批量注册。
// 按账号的失败限流在auth服务里，WebSocket与朋友圈网页登录共用同一计数。
// JWT登录无法被猜中，不计数也不拦截。
const (
	loginFailureWindow    = 15 * time.Minute
	maxLoginFailuresPerIP = 50
	signupWindow          = time.Hour
	maxSignupsPerIP       = 20
	authLimitTimeout      = time.Second
)

// 计数窗口从第一次失败开始，不会因后续失败而延长。
var incrementInWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func loginIPLimitKey(ip string) string { return "auth:login_fail:ip:" + ip }

func signupIPLimitKey(ip string) string { return "auth:signup:ip:" + ip }

// passwordLoginBlocked reports whether the source IP used up its failures.
// Redis errors fail open: an outage must not lock every user out.
// Successful logins never reset the IP count, so one valid account cannot launder failures against others.
func passwordLoginBlocked(ip string) bool {
	if redisClient.Rdb == nil || ip == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	count, err := redisClient.Rdb.Get(ctx, loginIPLimitKey(ip)).Int64()
	if err == redis.Nil {
		return false
	}
	if err != nil {
		logger.Sugar().Warnw("读取登录限流计数失败，放行", "error", err)
		return false
	}
	return count >= maxLoginFailuresPerIP
}

func recordPasswordLoginFailure(ip string) {
	if redisClient.Rdb == nil || ip == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	if err := incrementInWindowScript.Run(ctx, redisClient.Rdb, []string{loginIPLimitKey(ip)}, loginFailureWindow.Milliseconds()).Err(); err != nil {
		logger.Sugar().Warnw("记录IP登录失败次数失败", "error", err)
	}
}

// signupAllowed counts every attempt, successful or not, because each one costs a bcrypt hash.
func signupAllowed(ip string) bool {
	if redisClient.Rdb == nil || ip == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	count, err := incrementInWindowScript.Run(ctx, redisClient.Rdb, []string{signupIPLimitKey(ip)}, signupWindow.Milliseconds()).Int64()
	if err != nil {
		logger.Sugar().Warnw("记录注册次数失败，放行", "error", err)
		return true
	}
	return count <= maxSignupsPerIP
}
