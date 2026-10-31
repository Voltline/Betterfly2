package bot

import (
	"testing"
	"time"
)

func TestLimiterPerMinuteNotifiesOnce(t *testing.T) {
	l := newLimiter(2, 0, time.UTC)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	want := []limitDecision{limitAllow, limitAllow, limitNotify, limitSilent}
	for i, expected := range want {
		if got := l.allow(1, now.Add(time.Duration(i)*time.Second)); got != expected {
			t.Fatalf("call %d = %v, want %v", i, got, expected)
		}
	}
	if got := l.allow(2, now); got != limitAllow {
		t.Fatalf("other user = %v", got)
	}
	// 窗口过去后恢复，再次超限会重新提醒一次。
	later := now.Add(2 * time.Minute)
	for i, expected := range []limitDecision{limitAllow, limitAllow, limitNotify} {
		if got := l.allow(1, later.Add(time.Duration(i)*time.Second)); got != expected {
			t.Fatalf("later call %d = %v, want %v", i, got, expected)
		}
	}
}

func TestLimiterDailyLimitUsesLocalDate(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(0, 1, shanghai)
	evening := time.Date(2026, 10, 30, 15, 50, 0, 0, time.UTC) // 上海23:50
	if got := l.allow(1, evening); got != limitAllow {
		t.Fatalf("first = %v", got)
	}
	if got := l.allow(1, evening.Add(time.Minute)); got != limitNotify || !l.isDayLimit(1) {
		t.Fatalf("second = %v", got)
	}
	if got := l.allow(1, evening.Add(2*time.Minute)); got != limitSilent {
		t.Fatalf("third = %v", got)
	}
	if got := l.allow(1, evening.Add(15*time.Minute)); got != limitAllow {
		t.Fatalf("next local day = %v", got)
	}
}
