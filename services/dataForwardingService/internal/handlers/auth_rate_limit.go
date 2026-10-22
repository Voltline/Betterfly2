package handlers

import (
	"Betterfly2/shared/logger"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	redisClient "data_forwarding_service/internal/redis"
	"github.com/redis/go-redis/v9"
)

// 密码登录与注册的固定窗口限流。按账号限制分布式爆破，按IP限制单一来源的爆破和批量注册。
// JWT登录无法被猜中，不计数也不拦截，避免攻击者锁定账号时把已登录设备的自动重连一起挡住。
const (
	loginFailureWindow     = 15 * time.Minute
	maxLoginFailuresPerAcc = 10
	maxLoginFailuresPerIP  = 50
	signupWindow           = time.Hour
	maxSignupsPerIP        = 20
	authLimitTimeout       = time.Second
)

// 计数窗口从第一次失败开始，不会因后续失败而延长。
var incrementInWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

func loginAccountLimitKey(account string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(account)))
	return fmt.Sprintf("auth:login_fail:account:%x", digest[:])
}

func loginIPLimitKey(ip string) string { return "auth:login_fail:ip:" + ip }

func signupIPLimitKey(ip string) string { return "auth:signup:ip:" + ip }

// passwordLoginBlocked reports whether the account or source IP used up its failures.
// Redis errors fail open: an outage must not lock every user out.
func passwordLoginBlocked(account, ip string) bool {
	if redisClient.Rdb == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	counts, err := redisClient.Rdb.MGet(ctx, loginAccountLimitKey(account), loginIPLimitKey(ip)).Result()
	if err != nil {
		logger.Sugar().Warnw("读取登录限流计数失败，放行", "error", err)
		return false
	}
	return limitCount(counts[0]) >= maxLoginFailuresPerAcc || (ip != "" && limitCount(counts[1]) >= maxLoginFailuresPerIP)
}

func recordPasswordLoginFailure(account, ip string, countAccount bool) {
	if redisClient.Rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	window := loginFailureWindow.Milliseconds()
	if countAccount {
		if err := incrementInWindowScript.Run(ctx, redisClient.Rdb, []string{loginAccountLimitKey(account)}, window).Err(); err != nil {
			logger.Sugar().Warnw("记录账号登录失败次数失败", "error", err)
		}
	}
	if ip != "" {
		if err := incrementInWindowScript.Run(ctx, redisClient.Rdb, []string{loginIPLimitKey(ip)}, window).Err(); err != nil {
			logger.Sugar().Warnw("记录IP登录失败次数失败", "error", err)
		}
	}
}

// clearPasswordLoginFailures only resets the account; the IP keeps its count so
// one valid login cannot launder failures against other accounts.
func clearPasswordLoginFailures(account string) {
	if redisClient.Rdb == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authLimitTimeout)
	defer cancel()
	if err := redisClient.Rdb.Del(ctx, loginAccountLimitKey(account)).Err(); err != nil {
		logger.Sugar().Warnw("清除账号登录失败次数失败", "error", err)
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

func limitCount(value any) int64 {
	text, ok := value.(string)
	if !ok {
		return 0
	}
	var count int64
	_, _ = fmt.Sscan(text, &count)
	return count
}

var defaultTrustedProxyCIDRs = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

func parseTrustedProxies(raw string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(item)); err == nil {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return prefixes
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP returns the peer address, or the forwarded client address when the
// peer is a trusted proxy (the ingress). Forwarded headers from anyone else are ignored.
// A trusted peer without usable headers (e.g. a Docker port-forward gateway) yields "",
// so per-IP limits are skipped instead of lumping every client into one bucket.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !isTrustedProxy(peer, trusted) {
		return peer.String()
	}
	// Walk X-Forwarded-For from the right: the first untrusted hop is the one our proxies saw.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = addr.Unmap()
		if !isTrustedProxy(addr, trusted) {
			return addr.String()
		}
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addr.Unmap().String()
	}
	return ""
}
