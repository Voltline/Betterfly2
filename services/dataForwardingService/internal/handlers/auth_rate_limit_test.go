package handlers

import (
	"testing"

	redisClient "data_forwarding_service/internal/redis"
)

func TestPasswordLoginBlockedAfterIPFailures(t *testing.T) {
	server := postTestRedis(t)
	for i := 0; i < maxLoginFailuresPerIP-1; i++ {
		recordPasswordLoginFailure("203.0.113.1")
	}
	if passwordLoginBlocked("203.0.113.1") {
		t.Fatal("blocked before reaching the ip limit")
	}
	recordPasswordLoginFailure("203.0.113.1")
	if !passwordLoginBlocked("203.0.113.1") {
		t.Fatal("ip not blocked after limit")
	}
	if passwordLoginBlocked("203.0.113.2") {
		t.Fatal("other ip blocked")
	}
	// 无法确定真实IP时不计数也不拦截，避免所有客户端挤进同一个桶。
	recordPasswordLoginFailure("")
	if passwordLoginBlocked("") {
		t.Fatal("unknown ip blocked")
	}
	server.FastForward(loginFailureWindow)
	if passwordLoginBlocked("203.0.113.1") {
		t.Fatal("ip still blocked after window")
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
	recordPasswordLoginFailure("203.0.113.1")
	if passwordLoginBlocked("203.0.113.1") || !signupAllowed("203.0.113.1") {
		t.Fatal("limits did not fail open without redis")
	}

	server := postTestRedis(t)
	server.Close()
	if passwordLoginBlocked("203.0.113.1") || !signupAllowed("203.0.113.1") {
		t.Fatal("limits did not fail open on redis error")
	}
}
