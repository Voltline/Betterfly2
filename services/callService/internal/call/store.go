package call

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"

	"github.com/redis/go-redis/v9"
)

const (
	deadlinesKey     = "call:ring_deadlines"
	callOutboxStream = "call:outbox"
)

var createSessionScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1]
  and redis.call('GET', KEYS[2]) == ARGV[1]
  and redis.call('EXISTS', KEYS[3]) == 1 then
  return 2
end
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[2])
redis.call('SET', KEYS[3], ARGV[4], 'EX', ARGV[3])
redis.call('ZADD', KEYS[4], ARGV[5], ARGV[1])
return 1
`)

var createSessionWithEventsScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[5]) == 1 then
  return 2
end
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
redis.call('SET', KEYS[3], ARGV[4], 'PX', ARGV[3])
redis.call('ZADD', KEYS[4], ARGV[5], ARGV[1])
redis.call('SET', KEYS[5], '1', 'PX', ARGV[6])
local count = tonumber(ARGV[8])
local index = 9
for i = 1, count do
  redis.call('XADD', KEYS[6], '*',
    'event_id', ARGV[index], 'operation_key', ARGV[index + 1],
    'topic', ARGV[index + 2], 'payload', ARGV[index + 3])
  index = index + 4
end
return 1
`)

var transitionSessionWithEventsScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[5]) == 1 then
  return 2
end
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
if ARGV[4] ~= '1' and (redis.call('GET', KEYS[2]) ~= ARGV[5] or redis.call('GET', KEYS[3]) ~= ARGV[5]) then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('ZREM', KEYS[4], ARGV[5])
if ARGV[4] == '1' then
  if redis.call('GET', KEYS[2]) == ARGV[5] then redis.call('DEL', KEYS[2]) end
  if redis.call('GET', KEYS[3]) == ARGV[5] then redis.call('DEL', KEYS[3]) end
else
  redis.call('PEXPIRE', KEYS[2], ARGV[3])
  redis.call('PEXPIRE', KEYS[3], ARGV[3])
end
redis.call('SET', KEYS[5], '1', 'PX', ARGV[6])
local count = tonumber(ARGV[8])
local index = 9
for i = 1, count do
  redis.call('XADD', KEYS[6], '*',
    'event_id', ARGV[index], 'operation_key', ARGV[index + 1],
    'topic', ARGV[index + 2], 'payload', ARGV[index + 3])
  index = index + 4
end
return 1
`)

type RedisStore struct {
	client        *redis.Client
	ringTTL       time.Duration
	activeTTL     time.Duration
	terminatedTTL time.Duration
	cleanupGrace  time.Duration
	operationTTL  time.Duration
}

func NewRedisStore(client *redis.Client, ringTTL, activeTTL time.Duration) *RedisStore {
	if ringTTL <= 0 {
		ringTTL = 45 * time.Second
	}
	if activeTTL <= 0 {
		activeTTL = 6 * time.Hour
	}
	replayWindow := callDurationEnv("KAFKA_MAX_REPLAY_WINDOW", 7*24*time.Hour)
	operationTTL := callDurationEnv("CALL_OPERATION_RETENTION", 30*24*time.Hour)
	if operationTTL <= replayWindow {
		operationTTL = replayWindow + 24*time.Hour
	}
	return &RedisStore{
		client: client, ringTTL: ringTTL, activeTTL: activeTTL,
		terminatedTTL: 5 * time.Minute, cleanupGrace: time.Minute, operationTTL: operationTTL,
	}
}

func callDurationEnv(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (s *RedisStore) CreateSessionWithEvents(ctx context.Context, session Session, operationKey string, events []PendingEvent) (bool, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return false, err
	}
	ttl := time.Until(session.RingDeadline)
	if ttl <= 0 {
		ttl = s.ringTTL
	}
	args := []any{
		session.ID, ttl.Milliseconds(), (ttl + s.cleanupGrace).Milliseconds(), string(payload),
		session.RingDeadline.Unix(), s.operationTTL.Milliseconds(), operationKey, len(events),
	}
	args = appendPendingEventArgs(args, events)
	result, err := createSessionWithEventsScript.Run(ctx, s.client, []string{
		userCallKey(session.CallerUserID), userCallKey(session.CalleeUserID), sessionKey(session.ID), deadlinesKey,
		callOperationKey(operationKey), callOutboxStream,
	}, args...).Int()
	if err != nil {
		return false, err
	}
	if result == 0 {
		return false, ErrUserBusy
	}
	return result == 2, nil
}

func (s *RedisStore) OperationCompleted(ctx context.Context, operationKey string) (bool, error) {
	count, err := s.client.Exists(ctx, callOperationKey(operationKey)).Result()
	return count > 0, err
}

func (s *RedisStore) TransitionSessionWithEvents(ctx context.Context, expected, updated Session, terminal bool, operationKey string, events []PendingEvent) (bool, error) {
	expectedPayload, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	updatedPayload, err := json.Marshal(updated)
	if err != nil {
		return false, err
	}
	ttl := s.activeTTL
	if updated.State == StateEnded {
		ttl = s.terminatedTTL
	} else if updated.State == StateRinging {
		ttl = time.Until(updated.RingDeadline) + s.cleanupGrace
	}
	terminalValue := "0"
	if terminal {
		terminalValue = "1"
	}
	args := []any{
		string(expectedPayload), string(updatedPayload), ttl.Milliseconds(), terminalValue,
		updated.ID, s.operationTTL.Milliseconds(), operationKey, len(events),
	}
	args = appendPendingEventArgs(args, events)
	result, err := transitionSessionWithEventsScript.Run(ctx, s.client, []string{
		sessionKey(updated.ID), userCallKey(updated.CallerUserID), userCallKey(updated.CalleeUserID), deadlinesKey,
		callOperationKey(operationKey), callOutboxStream,
	}, args...).Int()
	if err != nil {
		return false, err
	}
	if result == 0 {
		return false, ErrInvalidState
	}
	return result == 2, nil
}

func appendPendingEventArgs(args []any, events []PendingEvent) []any {
	for _, event := range events {
		args = append(args, event.EventID, event.OperationKey, event.Topic, event.Payload)
	}
	return args
}

func callOperationKey(operationKey string) string {
	digest := sha256.Sum256([]byte(operationKey))
	return fmt.Sprintf("call:operation:%x", digest[:])
}

func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) UserTopic(ctx context.Context, userID int64) (string, error) {
	userIDString := strconv.FormatInt(userID, 10)
	topic, err := s.client.HGet(ctx, "ws_connection_mapping", userIDString).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrUserOffline
	}
	if err != nil {
		return "", err
	}
	if topic == "" {
		return "", ErrUserOffline
	}
	lease, err := s.client.Get(ctx, "ws_route_lease:"+userIDString).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrUserOffline
	}
	if err != nil {
		return "", err
	}
	if lease == "" || !strings.HasPrefix(lease, topic+"|") {
		return "", ErrUserOffline
	}
	return topic, nil
}

func (s *RedisStore) CreateSession(ctx context.Context, session Session) error {
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	ttl := time.Until(session.RingDeadline)
	if ttl <= 0 {
		ttl = s.ringTTL
	}
	result, err := createSessionScript.Run(ctx, s.client, []string{
		userCallKey(session.CallerUserID),
		userCallKey(session.CalleeUserID),
		sessionKey(session.ID),
		deadlinesKey,
	},
		session.ID,
		maxInt64(1, int64(ttl.Seconds())),
		maxInt64(1, int64((ttl+s.cleanupGrace).Seconds())),
		string(payload),
		session.RingDeadline.Unix(),
	).Int()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrUserBusy
	}
	return nil
}

func (s *RedisStore) GetSession(ctx context.Context, callID string) (Session, error) {
	payload, err := s.client.Get(ctx, sessionKey(callID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrCallNotFound
	}
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(payload, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *RedisStore) AcceptSession(ctx context.Context, callID string, userID int64, answer Description) (Session, error) {
	return s.updateSession(ctx, callID, func(session *Session) (time.Duration, error) {
		if session.CalleeUserID != userID {
			return 0, ErrForbidden
		}
		if session.State == StateActive && session.Answer != nil && *session.Answer == answer {
			return s.activeTTL, nil
		}
		if session.State != StateRinging {
			return 0, ErrInvalidState
		}
		now := time.Now().UTC()
		if !now.Before(session.RingDeadline) {
			return 0, ErrInvalidState
		}
		session.State = StateActive
		session.Answer = &answer
		session.AcceptedAt = &now
		return s.activeTTL, nil
	}, false)
}

func (s *RedisStore) RejectSession(ctx context.Context, callID string, userID int64, reason callpb.CallEndReason, message string) (Session, error) {
	return s.updateSession(ctx, callID, func(session *Session) (time.Duration, error) {
		if session.CalleeUserID != userID {
			return 0, ErrForbidden
		}
		if session.State == StateEnded && session.EndReason == reason && session.EndMessage == message {
			return s.terminatedTTL, nil
		}
		if session.State != StateRinging {
			return 0, ErrInvalidState
		}
		endSession(session, reason, message)
		return s.terminatedTTL, nil
	}, true)
}

func (s *RedisStore) EndSession(ctx context.Context, callID string, userID int64, reason callpb.CallEndReason, message string) (Session, error) {
	return s.updateSession(ctx, callID, func(session *Session) (time.Duration, error) {
		if _, err := session.Peer(userID); err != nil {
			return 0, err
		}
		if session.State == StateEnded && session.EndReason == reason && session.EndMessage == message {
			return s.terminatedTTL, nil
		}
		if session.State != StateRinging && session.State != StateActive {
			return 0, ErrInvalidState
		}
		endSession(session, reason, message)
		return s.terminatedTTL, nil
	}, true)
}

func (s *RedisStore) ExpireRinging(ctx context.Context, now time.Time, limit int64) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	ids, err := s.client.ZRangeByScore(ctx, deadlinesKey, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.Unix(), 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}

	expired := make([]Session, 0, len(ids))
	for _, callID := range ids {
		session, updateErr := s.updateSession(ctx, callID, func(session *Session) (time.Duration, error) {
			if session.State != StateRinging || session.RingDeadline.After(now) {
				return 0, ErrInvalidState
			}
			endSession(session, callpb.CallEndReason_TIMEOUT, "call timed out")
			return s.terminatedTTL, nil
		}, true)
		if updateErr == nil {
			expired = append(expired, session)
			continue
		}
		if errors.Is(updateErr, ErrCallNotFound) || errors.Is(updateErr, ErrInvalidState) {
			_ = s.client.ZRem(ctx, deadlinesKey, callID).Err()
			continue
		}
		return expired, updateErr
	}
	return expired, nil
}

func (s *RedisStore) updateSession(ctx context.Context, callID string, mutate func(*Session) (time.Duration, error), terminal bool) (Session, error) {
	var updated Session
	for attempt := 0; attempt < 5; attempt++ {
		snapshot, err := s.GetSession(ctx, callID)
		if err != nil {
			return Session{}, err
		}
		callerKey := userCallKey(snapshot.CallerUserID)
		calleeKey := userCallKey(snapshot.CalleeUserID)
		err = s.client.Watch(ctx, func(tx *redis.Tx) error {
			payload, err := tx.Get(ctx, sessionKey(callID)).Bytes()
			if errors.Is(err, redis.Nil) {
				return ErrCallNotFound
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(payload, &updated); err != nil {
				return err
			}
			callerCallID, callerErr := tx.Get(ctx, callerKey).Result()
			if callerErr != nil && !errors.Is(callerErr, redis.Nil) {
				return callerErr
			}
			calleeCallID, calleeErr := tx.Get(ctx, calleeKey).Result()
			if calleeErr != nil && !errors.Is(calleeErr, redis.Nil) {
				return calleeErr
			}
			ttl, err := mutate(&updated)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, sessionKey(callID), encoded, ttl)
				pipe.ZRem(ctx, deadlinesKey, callID)
				if terminal {
					if callerCallID == callID {
						pipe.Del(ctx, callerKey)
					}
					if calleeCallID == callID {
						pipe.Del(ctx, calleeKey)
					}
				} else {
					if callerCallID != callID || calleeCallID != callID {
						return ErrInvalidState
					}
					pipe.Expire(ctx, callerKey, ttl)
					pipe.Expire(ctx, calleeKey, ttl)
				}
				return nil
			})
			return err
		}, sessionKey(callID), callerKey, calleeKey)
		if !errors.Is(err, redis.TxFailedErr) {
			return updated, err
		}
	}
	return Session{}, fmt.Errorf("update call session %s: %w", callID, redis.TxFailedErr)
}

func endSession(session *Session, reason callpb.CallEndReason, message string) {
	now := time.Now().UTC()
	session.State = StateEnded
	session.EndReason = reason
	session.EndMessage = message
	session.EndedAt = &now
}

func sessionKey(callID string) string { return "call:session:" + callID }
func userCallKey(userID int64) string { return fmt.Sprintf("call:user:%d", userID) }

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
