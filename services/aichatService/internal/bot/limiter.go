package bot

import (
	"sync"
	"time"
)

// limiter 按用户限制每分钟与每天的提问次数，0表示不限。状态只在进程内，
// 多副本部署时每个副本各自计数；超限后每个窗口只提醒一次，之后静默。
type limiter struct {
	mu        sync.Mutex
	perMinute int
	perDay    int
	location  *time.Location
	users     map[int64]*userQuota
	calls     int
}

type userQuota struct {
	recent         []time.Time
	day            string
	dayCount       int
	minuteNotified bool
	dayNotified    bool
	lastSeen       time.Time
}

type limitDecision int

const (
	limitAllow limitDecision = iota
	limitNotify
	limitSilent
)

func newLimiter(perMinute, perDay int, location *time.Location) *limiter {
	if location == nil {
		location = time.UTC
	}
	return &limiter{perMinute: perMinute, perDay: perDay, location: location, users: map[int64]*userQuota{}}
}

func (l *limiter) allow(userID int64, now time.Time) limitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%1000 == 0 {
		l.prune(now)
	}
	quota := l.users[userID]
	if quota == nil {
		quota = &userQuota{}
		l.users[userID] = quota
	}
	quota.lastSeen = now

	day := now.In(l.location).Format(time.DateOnly)
	if quota.day != day {
		quota.day, quota.dayCount, quota.dayNotified = day, 0, false
	}
	cutoff := now.Add(-time.Minute)
	kept := quota.recent[:0]
	for _, at := range quota.recent {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	quota.recent = kept
	if len(kept) == 0 {
		quota.minuteNotified = false
	}

	if l.perDay > 0 && quota.dayCount >= l.perDay {
		return notifyOnce(&quota.dayNotified)
	}
	if l.perMinute > 0 && len(quota.recent) >= l.perMinute {
		return notifyOnce(&quota.minuteNotified)
	}
	quota.recent = append(quota.recent, now)
	quota.dayCount++
	return limitAllow
}

func notifyOnce(notified *bool) limitDecision {
	if *notified {
		return limitSilent
	}
	*notified = true
	return limitNotify
}

// prune 丢掉一天没出现的用户，避免长期运行后map只增不减。
func (l *limiter) prune(now time.Time) {
	for userID, quota := range l.users {
		if now.Sub(quota.lastSeen) > 24*time.Hour {
			delete(l.users, userID)
		}
	}
}

// isDayLimit 用于选择提醒文案。
func (l *limiter) isDayLimit(userID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	quota := l.users[userID]
	return quota != nil && l.perDay > 0 && quota.dayCount >= l.perDay
}
