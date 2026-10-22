package handlers

import (
	"net/http/httptest"
	"testing"

	redisClient "data_forwarding_service/internal/redis"
)

func TestPasswordLoginBlockedAfterAccountFailures(t *testing.T) {
	server := postTestRedis(t)
	for i := 0; i < maxLoginFailuresPerAcc-1; i++ {
		recordPasswordLoginFailure("alice", "203.0.113.1", true)
	}
	if passwordLoginBlocked("alice", "203.0.113.9") {
		t.Fatal("blocked before reaching the account limit")
	}
	recordPasswordLoginFailure("alice", "203.0.113.2", true)
	// 换IP也挡住，防止分布式爆破同一个账号。
	if !passwordLoginBlocked("alice", "203.0.113.9") {
		t.Fatal("account not blocked after limit")
	}
	if passwordLoginBlocked("bob", "203.0.113.9") {
		t.Fatal("other account blocked")
	}
	server.FastForward(loginFailureWindow)
	if passwordLoginBlocked("alice", "203.0.113.9") {
		t.Fatal("account still blocked after window")
	}
}

func TestPasswordLoginSuccessClearsOnlyAccount(t *testing.T) {
	postTestRedis(t)
	for i := 0; i < maxLoginFailuresPerAcc; i++ {
		recordPasswordLoginFailure("alice", "203.0.113.1", true)
	}
	clearPasswordLoginFailures("alice")
	if passwordLoginBlocked("alice", "203.0.113.9") {
		t.Fatal("account still blocked after successful login")
	}
	if got := limitCount(redisClient.Rdb.Get(t.Context(), loginIPLimitKey("203.0.113.1")).Val()); got != maxLoginFailuresPerAcc {
		t.Fatalf("ip counter reset by successful login: %d", got)
	}
}

func TestPasswordLoginBlockedAfterIPFailures(t *testing.T) {
	postTestRedis(t)
	// 不存在的账号只计IP，避免攻击者用不存在账号刷满别人的计数。
	for i := 0; i < maxLoginFailuresPerIP; i++ {
		recordPasswordLoginFailure("ghost", "203.0.113.1", false)
	}
	if passwordLoginBlocked("ghost", "203.0.113.2") {
		t.Fatal("account counted for missing account failures")
	}
	if !passwordLoginBlocked("anyone", "203.0.113.1") {
		t.Fatal("ip not blocked after limit")
	}
}

func TestSignupAllowedPerIPWindow(t *testing.T) {
	server := postTestRedis(t)
	for i := 0; i < maxSignupsPerIP; i++ {
		if !signupAllowed("203.0.113.1") {
			t.Fatalf("signup %d rejected", i+1)
		}
	}
	if signupAllowed("203.0.113.1") {
		t.Fatal("signup allowed past limit")
	}
	if !signupAllowed("203.0.113.2") {
		t.Fatal("other ip rejected")
	}
	server.FastForward(signupWindow)
	if !signupAllowed("203.0.113.1") {
		t.Fatal("signup still rejected after window")
	}
}

func TestAuthLimitsFailOpenWithoutRedis(t *testing.T) {
	previous := redisClient.Rdb
	redisClient.Rdb = nil
	t.Cleanup(func() { redisClient.Rdb = previous })
	recordPasswordLoginFailure("alice", "203.0.113.1", true)
	clearPasswordLoginFailures("alice")
	if passwordLoginBlocked("alice", "203.0.113.1") || !signupAllowed("203.0.113.1") {
		t.Fatal("limits did not fail open without redis")
	}

	server := postTestRedis(t)
	server.Close()
	if passwordLoginBlocked("alice", "203.0.113.1") || !signupAllowed("203.0.113.1") {
		t.Fatal("limits did not fail open on redis error")
	}
}

func TestClientIPTrustsForwardedHeadersOnlyFromProxies(t *testing.T) {
	trusted := parseTrustedProxies(defaultTrustedProxyCIDRs + ", not-a-cidr")
	cases := []struct {
		name, remote, xff, realIP, want string
	}{
		{"direct client ignores spoofed headers", "198.51.100.7:5555", "1.2.3.4", "5.6.7.8", "198.51.100.7"},
		{"ingress forwards client", "10.0.0.5:5555", "203.0.113.4", "", "203.0.113.4"},
		{"spoofed left entries are skipped", "10.0.0.5:5555", "1.2.3.4, 203.0.113.4, 10.0.0.9", "", "203.0.113.4"},
		{"real ip fallback", "10.0.0.5:5555", "", "203.0.113.8", "203.0.113.8"},
		{"proxy without usable headers is unknown", "10.0.0.5:5555", "nonsense", "", ""},
		{"ipv4 mapped ipv6", "[::ffff:198.51.100.7]:5555", "", "", "198.51.100.7"},
		{"ipv6 client behind proxy", "[::1]:5555", "2001:db8::1", "", "2001:db8::1"},
	}
	for _, tc := range cases {
		request := httptest.NewRequest("GET", "/ws", nil)
		request.RemoteAddr = tc.remote
		if tc.xff != "" {
			request.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.realIP != "" {
			request.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := clientIP(request, trusted); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
