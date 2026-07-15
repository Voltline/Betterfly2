package redisClient

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type DistributedSessionManager struct{}

type SessionData struct {
	ConnectionID string
	ContainerID  string
	OwnerToken   string
}

var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

var claimSessionAndRouteScript = redis.NewScript(`
local previous_container = redis.call('HGET', KEYS[2], ARGV[1])
if previous_container and previous_container ~= ARGV[3] then
  redis.call('SREM', 'container_connections:' .. previous_container, ARGV[1])
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[5])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[3])
redis.call('SADD', 'container_connections:' .. ARGV[3], ARGV[1])
redis.call('SET', KEYS[3], ARGV[3] .. '|' .. ARGV[4], 'PX', ARGV[6])
return 1
`)

var refreshOwnedSessionAndRouteScript = redis.NewScript(`
local session = redis.call('GET', KEYS[1])
local mapped = redis.call('HGET', KEYS[2], ARGV[1])
local leased = redis.call('GET', KEYS[3])
if session ~= ARGV[2]
  or mapped ~= ARGV[3]
  or leased ~= ARGV[3] .. '|' .. ARGV[4] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[5])
redis.call('PEXPIRE', KEYS[3], ARGV[6])
return 1
`)

var removeOwnedSessionAndRouteScript = redis.NewScript(`
local removed = 0
if redis.call('GET', KEYS[1]) == ARGV[2] then
  redis.call('DEL', KEYS[1])
  removed = 1
end
if redis.call('GET', KEYS[3]) == ARGV[3] .. '|' .. ARGV[4]
  and redis.call('HGET', KEYS[2], ARGV[1]) == ARGV[3] then
  redis.call('DEL', KEYS[3])
  redis.call('HDEL', KEYS[2], ARGV[1])
  redis.call('SREM', 'container_connections:' .. ARGV[3], ARGV[1])
  removed = 1
end
return removed
`)

func sessionKey(userID string) string  { return "user_session:" + userID }
func userLockKey(userID string) string { return "user_lock:" + userID }

func encodeSession(data SessionData) string {
	return data.ConnectionID + "|" + data.ContainerID + "|" + data.OwnerToken
}

func (dsm *DistributedSessionManager) AcquireUserLock(ctx context.Context, userID, ownerToken string, ttl time.Duration) (bool, error) {
	return Rdb.SetNX(ctx, userLockKey(userID), ownerToken, ttl).Result()
}

func (dsm *DistributedSessionManager) ReleaseUserLock(ctx context.Context, userID, ownerToken string) error {
	return releaseLockScript.Run(ctx, Rdb, []string{userLockKey(userID)}, ownerToken).Err()
}

func (dsm *DistributedSessionManager) GetUserSession(ctx context.Context, userID string) (SessionData, bool, error) {
	value, err := Rdb.Get(ctx, sessionKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return SessionData{}, false, nil
	}
	if err != nil {
		return SessionData{}, false, err
	}
	return ParseSessionData(value), true, nil
}

func (dsm *DistributedSessionManager) ClaimSessionAndRoute(ctx context.Context, userID string, data SessionData, sessionTTL, routeTTL time.Duration) error {
	return claimSessionAndRouteScript.Run(ctx, Rdb,
		[]string{sessionKey(userID), "ws_connection_mapping", routeLeaseKey(userID)},
		userID, encodeSession(data), data.ContainerID, data.OwnerToken,
		sessionTTL.Milliseconds(), routeTTL.Milliseconds(),
	).Err()
}

func (dsm *DistributedSessionManager) RefreshOwnedSessionAndRoute(
	ctx context.Context,
	userID string,
	data SessionData,
	sessionTTL, routeTTL time.Duration,
) error {
	updated, err := refreshOwnedSessionAndRouteScript.Run(ctx, Rdb,
		[]string{sessionKey(userID), "ws_connection_mapping", routeLeaseKey(userID)},
		userID, encodeSession(data), data.ContainerID, data.OwnerToken,
		sessionTTL.Milliseconds(), routeTTL.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrSessionOwnershipLost
	}
	return nil
}

func (dsm *DistributedSessionManager) RemoveOwnedSessionAndRoute(ctx context.Context, userID string, data SessionData) error {
	return removeOwnedSessionAndRouteScript.Run(ctx, Rdb,
		[]string{sessionKey(userID), "ws_connection_mapping", routeLeaseKey(userID)},
		userID, encodeSession(data), data.ContainerID, data.OwnerToken,
	).Err()
}

func (dsm *DistributedSessionManager) PublishOwnedKickNotification(ctx context.Context, userID, targetContainerID, ownerToken string) error {
	channel := "user_kick:" + targetContainerID
	message := "KICK:" + userID + ":" + ownerToken
	return Rdb.Publish(ctx, channel, message).Err()
}

// PublishKickNotification keeps the administrative kick path compatible.
func (dsm *DistributedSessionManager) PublishKickNotification(userID, targetContainerID string) error {
	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Second)
	defer cancel()
	return Rdb.Publish(ctx, "user_kick:"+targetContainerID, "KICK:"+userID+":*").Err()
}

func (dsm *DistributedSessionManager) SubscribeKickNotifications(ctx context.Context, containerID string, handler func(userID, ownerToken string)) error {
	channel := "user_kick:" + containerID
	backoff := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return nil
		}
		pubsub := Rdb.Subscribe(ctx, channel)
		if _, err := pubsub.Receive(ctx); err != nil {
			_ = pubsub.Close()
			if !waitForSubscriptionRetry(ctx, backoff) {
				return nil
			}
			backoff = nextSubscriptionBackoff(backoff)
			continue
		}
		logger.Sugar().Infof("已订阅实时踢出通知: 容器 %s", containerID)
		backoff = 100 * time.Millisecond
		messages := pubsub.Channel()
		connected := true
		for connected {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
				return nil
			case msg, ok := <-messages:
				if !ok {
					connected = false
					continue
				}
				parts := strings.SplitN(msg.Payload, ":", 3)
				if len(parts) == 3 && parts[0] == "KICK" {
					handler(parts[1], parts[2])
				}
			}
		}
		_ = pubsub.Close()
		if !waitForSubscriptionRetry(ctx, backoff) {
			return nil
		}
		backoff = nextSubscriptionBackoff(backoff)
	}
}

func waitForSubscriptionRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextSubscriptionBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > 5*time.Second {
		return 5 * time.Second
	}
	return current
}

func ParseSessionData(value string) SessionData {
	parts := strings.Split(value, "|")
	if len(parts) >= 3 {
		return SessionData{ConnectionID: strings.Join(parts[:len(parts)-2], "|"), ContainerID: parts[len(parts)-2], OwnerToken: parts[len(parts)-1]}
	}
	// Read-only compatibility with connectionID:containerID records.
	if index := strings.LastIndex(value, ":"); index >= 0 {
		return SessionData{ConnectionID: value[:index], ContainerID: value[index+1:]}
	}
	return SessionData{ConnectionID: value}
}
