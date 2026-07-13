package redisClient

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var Rdb *redis.Client
var ctx = context.Background()

const routeLeaseTTL = 90 * time.Second

var ErrRouteNotFound = errors.New("无有效WebSocket路由")

func routeLeaseKey(userID string) string { return "ws_route_lease:" + userID }

var registerConnectionScript = redis.NewScript(`
local previous = redis.call('HGET', KEYS[1], ARGV[1])
if previous and previous ~= ARGV[2] then
  redis.call('SREM', 'container_connections:' .. previous, ARGV[1])
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('SADD', 'container_connections:' .. ARGV[2], ARGV[1])
redis.call('SET', KEYS[2], ARGV[2] .. '|' .. ARGV[3], 'PX', ARGV[4])
return 1
`)

var unregisterConnectionScript = redis.NewScript(`
local mapped = redis.call('HGET', KEYS[1], ARGV[1])
local leased = redis.call('GET', KEYS[2])
if mapped ~= ARGV[2] or leased ~= ARGV[2] .. '|' .. ARGV[3] then
  return 0
end
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('SREM', 'container_connections:' .. ARGV[2], ARGV[1])
redis.call('DEL', KEYS[2])
return 1
`)

var refreshConnectionScript = redis.NewScript(`
local mapped = redis.call('HGET', KEYS[1], ARGV[1])
local leased = redis.call('GET', KEYS[2])
if mapped ~= ARGV[2] or leased ~= ARGV[2] .. '|' .. ARGV[3] then
  return 0
end
redis.call('SET', KEYS[2], leased, 'PX', ARGV[4])
return 1
`)

var getValidRouteScript = redis.NewScript(`
local mapped = redis.call('HGET', KEYS[1], ARGV[1])
if not mapped then
  return nil
end
local leased = redis.call('GET', KEYS[2])
if leased and string.sub(leased, 1, string.len(mapped) + 1) == mapped .. '|' then
  return mapped
end
if redis.call('HGET', KEYS[1], ARGV[1]) == mapped then
  redis.call('HDEL', KEYS[1], ARGV[1])
  redis.call('SREM', 'container_connections:' .. mapped, ARGV[1])
end
return nil
`)

var getValidRoutesScript = redis.NewScript(`
local result = {}
for i, user_id in ipairs(ARGV) do
  local mapped = redis.call('HGET', KEYS[1], user_id)
  if mapped then
    local leased = redis.call('GET', 'ws_route_lease:' .. user_id)
    if leased and string.sub(leased, 1, string.len(mapped) + 1) == mapped .. '|' then
      table.insert(result, user_id)
      table.insert(result, mapped)
    elseif redis.call('HGET', KEYS[1], user_id) == mapped then
      redis.call('HDEL', KEYS[1], user_id)
      redis.call('SREM', 'container_connections:' .. mapped, user_id)
    end
  end
end
return result
`)

// InitRedis 初始化 Redis
func InitRedis() error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	Rdb = redis.NewClient(&redis.Options{Addr: addr, DB: 0})
	logger.Sugar().Debugf("当前 Redis: %s", addr)
	if _, err := Rdb.Ping(ctx).Result(); err != nil {
		return fmt.Errorf("连接 Redis 失败: %v", err)
	}
	return nil
}

func RegisterConnection(ctx context.Context, id, containerID, ownerToken string) error {
	if Rdb == nil {
		return errors.New("Redis客户端未初始化")
	}
	return registerConnectionScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)},
		id, containerID, ownerToken, routeLeaseTTL.Milliseconds(),
	).Err()
}

func UnregisterConnection(ctx context.Context, id, containerID, ownerToken string) error {
	if Rdb == nil {
		return errors.New("Redis客户端未初始化")
	}
	_, err := unregisterConnectionScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)},
		id, containerID, ownerToken,
	).Result()
	return err
}

func RefreshConnection(ctx context.Context, id, containerID, ownerToken string) error {
	if Rdb == nil {
		return errors.New("Redis客户端未初始化")
	}
	updated, err := refreshConnectionScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)},
		id, containerID, ownerToken, routeLeaseTTL.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if updated == 0 {
		return ErrRouteNotFound
	}
	return nil
}

func GetContainerByConnection(id string) (string, error) {
	if Rdb == nil {
		return "", errors.New("Redis客户端未初始化")
	}
	result, err := getValidRouteScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)}, id,
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", ErrRouteNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取WebSocket路由失败: %w", err)
	}
	return result, nil
}

func GetContainersByConnections(ids []string) (map[string]string, error) {
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if Rdb == nil {
		return nil, errors.New("Redis客户端未初始化")
	}

	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	values, err := getValidRoutesScript.Run(ctx, Rdb, []string{"ws_connection_mapping"}, args...).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("批量读取WebSocket路由失败: %w", err)
	}
	for i := 0; i+1 < len(values); i += 2 {
		result[values[i]] = values[i+1]
	}
	return result, nil
}
