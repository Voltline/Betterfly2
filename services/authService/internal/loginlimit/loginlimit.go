// Package loginlimit 按账号限制密码校验失败次数。
// 放在auth里，WebSocket登录、朋友圈网页登录和修改密码共用同一个计数，换入口或换IP都绕不过去。
package loginlimit

import (
	"Betterfly2/shared/logger"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	Window       = 15 * time.Minute
	MaxFailures  = 10
	redisTimeout = time.Second
)

// 计数窗口从第一次失败开始，不会因后续失败而延长。
var incrementInWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// Limiter 为nil或未配置Redis时不限流。Redis出错时放行，故障不能把所有人锁在门外。
type Limiter struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// FromEnv 读取REDIS_ADDR，未配置时返回不限流的Limiter。
func FromEnv() *Limiter {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		logger.Sugar().Warnln("未配置REDIS_ADDR，密码校验不做账号级限流")
		return &Limiter{}
	}
	return New(redis.NewClient(&redis.Options{Addr: addr}))
}

func key(userID int64) string { return fmt.Sprintf("auth:password_fail:user:%d", userID) }

func (l *Limiter) enabled() bool { return l != nil && l.rdb != nil }

// Blocked 报告该用户是否已用完窗口内的失败次数。
func (l *Limiter) Blocked(userID int64) bool {
	if !l.enabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	count, err := l.rdb.Get(ctx, key(userID)).Int64()
	if err == redis.Nil {
		return false
	}
	if err != nil {
		logger.Sugar().Warnw("读取密码失败计数失败，放行", "user_id", userID, "error", err)
		return false
	}
	return count >= MaxFailures
}

func (l *Limiter) RecordFailure(userID int64) {
	if !l.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	if err := incrementInWindowScript.Run(ctx, l.rdb, []string{key(userID)}, Window.Milliseconds()).Err(); err != nil {
		logger.Sugar().Warnw("记录密码失败次数失败", "user_id", userID, "error", err)
	}
}

func (l *Limiter) Clear(userID int64) {
	if !l.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisTimeout)
	defer cancel()
	if err := l.rdb.Del(ctx, key(userID)).Err(); err != nil {
		logger.Sugar().Warnw("清除密码失败次数失败", "user_id", userID, "error", err)
	}
}
