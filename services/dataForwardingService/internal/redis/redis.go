package redisClient

import (
	"Betterfly2/shared/logger"
	"context"
	"errors"
	"fmt"
	"github.com/redis/go-redis/v9"
	"os"
)

var Rdb *redis.Client
var ctx = context.Background()

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
	sugar.Infof("当前 Redis: %s", addr)

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
	_, err = pipe.Exec(ctx)
	return err
}

func GetContainerByConnection(id string) string {
	result, err := Rdb.HGet(ctx, "ws_connection_mapping", id).Result()
	if err != nil {
		logger.Sugar().Warnf("GetContainerByConnection 错误: %v", err)
		return ""
	}
	return result
}
