package redis_client

import (
	"context"
	"data_forwarding_service/internal/logger_config"
	"fmt"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"os"
)

var Rdb *redis.Client
var ctx = context.Background()

// 初始化 Redis
func InitRedis() error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	Rdb = redis.NewClient(&redis.Options{
		Addr: addr,
		DB:   0,
	})

	log := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer log.Sync()
	sugar := log.Sugar()
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
	pipe := Rdb.TxPipeline()
	pipe.HDel(ctx, "ws_connection_mapping", id)
	pipe.SRem(ctx, fmt.Sprintf("container_connections:%s", containerID), id)
	_, err := pipe.Exec(ctx)
	return err
}

func GetContainerByConnection(id string) (string, error) {
	return Rdb.HGet(ctx, "ws_connection_mapping", id).Result()
}
