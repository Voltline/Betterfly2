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

func routeLeaseKey(userID string) string { return "ws_route_lease:" + userID }

// InitRedis 初始化 Redis
func InitRedis() error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	Rdb = redis.NewClient(&redis.Options{
		Addr: addr,
		DB:   0,
	})

	sugar := logger.Sugar()
	sugar.Debugf("当前 Redis: %s", addr)

	_, err := Rdb.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("连接 Redis 失败: %v", err)
	}
	return nil
}

func RegisterConnection(id string, containerID string) error {
	pipe := Rdb.TxPipeline()
	pipe.HSet(ctx, "ws_connection_mapping", id, containerID)
	pipe.SAdd(ctx, "container_connections:"+containerID, id)
	pipe.SetEx(ctx, routeLeaseKey(id), containerID, routeLeaseTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func UnregisterConnection(id string, containerID string) error {
	// 检查当前记录是否匹配当前容器
	current, err := Rdb.HGet(ctx, "ws_connection_mapping", id).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil // 本来就没有
		}
		return err
	}
	if current != containerID {
		logger.Sugar().Warnf("尝试删除非本容器的连接: %s 属于 %s, 当前容器: %s", id, current, containerID)
		return nil // 不匹配则不删除
	}

	pipe := Rdb.TxPipeline()
	pipe.HDel(ctx, "ws_connection_mapping", id)
	pipe.SRem(ctx, fmt.Sprintf("container_connections:%s", containerID), id)
	pipe.Del(ctx, routeLeaseKey(id))
	_, err = pipe.Exec(ctx)
	return err
}

func RefreshConnection(id string, containerID string) error {
	current, err := Rdb.HGet(ctx, "ws_connection_mapping", id).Result()
	if err != nil {
		return err
	}
	if current != containerID {
		return fmt.Errorf("用户连接映射已变更: %s", id)
	}
	return Rdb.SetEx(ctx, routeLeaseKey(id), containerID, routeLeaseTTL).Err()
}

func GetContainerByConnection(id string) string {
	result, err := Rdb.HGet(ctx, "ws_connection_mapping", id).Result()
	logger.Sugar().Debugf("ws_connection_mapping 待查询id为: %s", id)
	if err != nil {
		logger.Sugar().Warnf("GetContainerByConnection 错误: %v", err)
		return ""
	}
	return result
}

func GetContainersByConnections(ids []string) map[string]string {
	result := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return result
	}

	values, err := Rdb.HMGet(ctx, "ws_connection_mapping", ids...).Result()
	if err != nil {
		logger.Sugar().Warnf("GetContainersByConnections 错误: %v", err)
		return result
	}

	for i, value := range values {
		containerID, ok := value.(string)
		if !ok || containerID == "" {
			continue
		}
		result[ids[i]] = containerID
	}
	return result
}
