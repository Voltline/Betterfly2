package call

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const groupDeadlinesKey = "call:group_deadlines"

// A long control-plane outage can expire room metadata before the sweeper runs.
// Keep SFU cleanup durable without deleting a newly recreated room's deadline.
var missingRoomScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
if not redis.call('ZSCORE', KEYS[2], ARGV[1]) then return 0 end
if redis.call('EXISTS', KEYS[3]) == 0 then
  redis.call('XADD', KEYS[4], '*', 'event_id', ARGV[3], 'operation_key', ARGV[4], 'topic', ARGV[5], 'payload', ARGV[6])
  redis.call('SET', KEYS[3], '1', 'PX', ARGV[2])
end
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`)

var commitRoomScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[4]) == 1 then return 2 end
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current or redis.call('EXISTS', KEYS[2]) == 1 then return 0 end
else
  if current ~= ARGV[1] then return 0 end
  if ARGV[5] == 'active' and redis.call('GET', KEYS[2]) ~= ARGV[4] then return 0 end
end
local users = cjson.decode(ARGV[8])
for i, user in ipairs(users) do
  local busy = redis.call('GET', KEYS[5+i])
  if user.keep and busy and busy ~= ARGV[4] then return -1 end
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
if ARGV[5] == 'active' then
  redis.call('SET', KEYS[2], ARGV[4], 'PX', ARGV[3])
  redis.call('ZADD', KEYS[3], ARGV[6], ARGV[4])
else
  if redis.call('GET', KEYS[2]) == ARGV[4] then redis.call('DEL', KEYS[2]) end
  redis.call('ZREM', KEYS[3], ARGV[4])
end
for i, user in ipairs(users) do
  if user.keep then
    redis.call('SET', KEYS[5+i], ARGV[4], 'PX', ARGV[3])
  elseif redis.call('GET', KEYS[5+i]) == ARGV[4] then
    redis.call('DEL', KEYS[5+i])
  end
end
redis.call('SET', KEYS[4], '1', 'PX', ARGV[7])
local index = 10
for i = 1, tonumber(ARGV[9]) do
  redis.call('XADD', KEYS[5], '*', 'event_id', ARGV[index],
    'operation_key', ARGV[index+1], 'topic', ARGV[index+2], 'payload', ARGV[index+3])
  index = index + 4
end
return 1
`)

func roomKey(id string) string     { return "call:group_session:" + id }
func groupRoomKey(id int64) string { return fmt.Sprintf("call:group:%d", id) }

func (s *RedisStore) GetRoom(ctx context.Context, id string) (GroupRoom, error) {
	payload, err := s.client.Get(ctx, roomKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return GroupRoom{}, ErrCallNotFound
	}
	if err != nil {
		return GroupRoom{}, err
	}
	var room GroupRoom
	err = json.Unmarshal(payload, &room)
	return room, err
}

func (s *RedisStore) GroupRoomID(ctx context.Context, groupID int64) (string, error) {
	id, err := s.client.Get(ctx, groupRoomKey(groupID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrCallNotFound
	}
	return id, err
}

func (s *RedisStore) CommitRoom(ctx context.Context, expected *GroupRoom, updated GroupRoom, operation string, events []PendingEvent) (bool, error) {
	if operation == "" || updated.ID == "" || updated.MaxParticipants < 2 || updated.MaxParticipants > 64 || len(updated.Participants) > updated.MaxParticipants {
		return false, ErrInvalidInput
	}
	oldJSON := ""
	users := make(map[int64]bool)
	if expected != nil {
		if expected.ID != updated.ID || expected.GroupID != updated.GroupID {
			return false, ErrInvalidInput
		}
		payload, err := json.Marshal(expected)
		if err != nil {
			return false, err
		}
		oldJSON = string(payload)
		for _, p := range expected.Participants {
			users[p.UserID] = false
		}
	}
	for _, p := range updated.Participants {
		users[p.UserID] = updated.State == StateActive
	}
	ids := make([]int64, 0, len(users))
	for id := range users {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	type ownership struct {
		Keep bool `json:"keep"`
	}
	ownerships := make([]ownership, 0, len(ids))
	keys := []string{roomKey(updated.ID), groupRoomKey(updated.GroupID), groupDeadlinesKey, callOperationKey(operation), callOutboxStream}
	for _, id := range ids {
		keys = append(keys, userCallKey(id))
		ownerships = append(ownerships, ownership{Keep: users[id]})
	}
	payload, err := json.Marshal(updated)
	if err != nil {
		return false, err
	}
	ownersJSON, _ := json.Marshal(ownerships)
	ttl := time.Until(updated.ExpiresAt) + s.cleanupGrace
	if updated.State == StateEnded {
		ttl = s.terminatedTTL
	}
	if ttl <= 0 {
		ttl = s.cleanupGrace
	}
	args := []any{oldJSON, string(payload), ttl.Milliseconds(), updated.ID, updated.State,
		updated.deadline().UnixMilli(), s.operationTTL.Milliseconds(), string(ownersJSON), len(events)}
	result, err := commitRoomScript.Run(ctx, s.client, keys, appendPendingEventArgs(args, events)...).Int()
	if err != nil {
		return false, err
	}
	if result == -1 {
		return false, ErrUserBusy
	}
	if result == 0 {
		return false, ErrRoomConflict
	}
	return result == 2, nil
}

func (s *RedisStore) DueRooms(ctx context.Context, now time.Time, limit int64) ([]GroupRoom, error) {
	ids, err := s.client.ZRangeByScore(ctx, groupDeadlinesKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Count: limit}).Result()
	if err != nil {
		return nil, err
	}
	rooms := make([]GroupRoom, 0, len(ids))
	for _, id := range ids {
		room, err := s.GetRoom(ctx, id)
		if errors.Is(err, ErrCallNotFound) {
			operation := "group-expired-metadata:" + id
			event := mediaEvent(operation, "DeleteRoom", "bf2-group-"+id, "")
			if err := missingRoomScript.Run(ctx, s.client, []string{roomKey(id), groupDeadlinesKey, callOperationKey(operation), callOutboxStream}, id, s.operationTTL.Milliseconds(), event.EventID, event.OperationKey, event.Topic, event.Payload).Err(); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, nil
}
