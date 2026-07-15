package redisClient

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

var Rdb *redis.Client
var ctx = context.Background()

var ErrRouteNotFound = errors.New("无有效WebSocket路由")
var ErrSessionOwnershipLost = errors.New("WebSocket会话所有权已失效")

func routeLeaseKey(userID string) string { return "ws_route_lease:" + userID }

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
