package loginlimit

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestLimiter(t *testing.T) (*Limiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client), server
}

func TestBlockedAfterMaxFailuresUntilWindowEnds(t *testing.T) {
	limiter, server := newTestLimiter(t)
	for i := 0; i < MaxFailures-1; i++ {
		limiter.RecordFailure(7)
	}
	if limiter.Blocked(7) {
		t.Fatal("blocked before reaching the limit")
	}
	limiter.RecordFailure(7)
	if !limiter.Blocked(7) {
		t.Fatal("not blocked at the limit")
	}
	if limiter.Blocked(8) {
		t.Fatal("other user blocked")
	}
	// 后续失败不延长窗口。
	server.FastForward(Window / 2)
	limiter.RecordFailure(7)
	server.FastForward(Window / 2)
	if limiter.Blocked(7) {
		t.Fatal("still blocked after the window")
	}
}

func TestClearResetsFailures(t *testing.T) {
	limiter, _ := newTestLimiter(t)
	for i := 0; i < MaxFailures; i++ {
		limiter.RecordFailure(7)
	}
	limiter.Clear(7)
	if limiter.Blocked(7) {
		t.Fatal("still blocked after clear")
	}
}

func TestFailsOpenWithoutRedis(t *testing.T) {
	var nilLimiter *Limiter
	for _, limiter := range []*Limiter{nilLimiter, {}} {
		limiter.RecordFailure(7)
		limiter.Clear(7)
		if limiter.Blocked(7) {
			t.Fatal("disabled limiter blocked")
		}
	}
	limiter, server := newTestLimiter(t)
	server.Close()
	limiter.RecordFailure(7)
	if limiter.Blocked(7) {
		t.Fatal("limiter did not fail open on redis error")
	}
}
