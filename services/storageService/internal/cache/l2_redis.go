package cache

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

var (
	redisOnce      sync.Once
	redisCloseOnce sync.Once
	gobTypesOnce   sync.Once
	redisClient    *redis.Client
)

// L2Redis 实现Cache接口的Redis缓存
type L2Redis struct {
	client *redis.Client
	ctx    context.Context
}

// encode 使用gob编码对象
func encode(value interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(value); err != nil {
		return nil, fmt.Errorf("gob编码失败: %v", err)
	}
	return buf.Bytes(), nil
}

// decode 使用gob解码对象
func decode(data []byte) (interface{}, error) {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)

	// 尝试解码为注册的类型
	var value interface{}
	if err := dec.Decode(&value); err == nil {
		return value, nil
	}

	// 如果失败，尝试解码为db.Message（兼容旧格式）
	buf.Reset()
	buf.Write(data)
	dec = gob.NewDecoder(buf)
	var msg db.Message
	if err := dec.Decode(&msg); err == nil {
		return &msg, nil
	}

	// 如果失败，尝试解码为db.User（兼容旧格式）
	buf.Reset()
	buf.Write(data)
	dec = gob.NewDecoder(buf)
	var user db.User
	if err := dec.Decode(&user); err == nil {
		return &user, nil
	}

	// 如果失败，尝试解码为字符串
	buf.Reset()
	buf.Write(data)
	dec = gob.NewDecoder(buf)
	var str string
	if err := dec.Decode(&str); err == nil {
		return str, nil
	}

	return nil, errors.New("无法解码缓存数据")
}

// getRedisAddr 获取Redis地址，优先使用环境变量
func getRedisAddr() string {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	return addr
}

// initRedisClient 初始化Redis客户端
func initRedisClient() error {
	var initErr error
	redisOnce.Do(func() {
		sugar := logger.Sugar()
		addr := getRedisAddr()

		sugar.Infof("初始化L2 Redis缓存，地址: %s", addr)

		// 创建Redis客户端
		client := redis.NewClient(&redis.Options{
			Addr:         addr,
			Password:     "", // 没有密码
			DB:           0,  // 默认数据库
			DialTimeout:  10 * time.Second,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			PoolSize:     100, // 连接池大小
			MinIdleConns: 10,  // 最小空闲连接
		})

		// 测试连接
		ctx := context.Background()
		_, err := client.Ping(ctx).Result()
		if err != nil {
			initErr = fmt.Errorf("Redis连接测试失败: %v", err)
			return
		}

		redisClient = client
		sugar.Infof("L2 Redis缓存初始化成功")
	})
	return initErr
}

// NewL2Cache 创建新的L2缓存实例
func NewL2Cache() (*L2Redis, error) {
	if err := initRedisClient(); err != nil {
		return nil, err
	}

	// 在init中注册gob类型
	initGobTypes()

	return &L2Redis{
		client: redisClient,
		ctx:    context.Background(),
	}, nil
}

// initGobTypes 注册gob编码的类型（只执行一次）
func initGobTypes() {
	gobTypesOnce.Do(func() {
		// 注册可能被缓存的数据类型
		gob.Register(&db.Message{})
		gob.Register(&db.User{})
		gob.Register("")
	})
}

// Set 实现Cache接口的Set方法
func (c *L2Redis) Set(key string, value interface{}, ttl time.Duration) bool {
	if c.client == nil {
		logger.Sugar().Error("Redis客户端未初始化")
		return false
	}

	// 编码值
	encoded, err := encode(value)
	if err != nil {
		logger.Sugar().Errorf("编码缓存值失败: %v", err)
		return false
	}

	// 设置到Redis，带TTL
	err = c.client.Set(c.ctx, key, encoded, ttl).Err()
	if err != nil {
		logger.Sugar().Errorf("Redis设置失败: %v", err)
		return false
	}

	return true
}

// Get 实现Cache接口的Get方法
func (c *L2Redis) Get(key string) (interface{}, bool) {
	if c.client == nil {
		logger.Sugar().Error("Redis客户端未初始化")
		return nil, false
	}

	// 从Redis获取
	data, err := c.client.Get(c.ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			logger.Sugar().Errorf("Redis获取失败: %v", err)
		}
		return nil, false
	}

	// 解码值
	value, err := decode(data)
	if err != nil {
		logger.Sugar().Errorf("解码缓存值失败: %v", err)
		return nil, false
	}

	return value, true
}

// Del 实现Cache接口的Del方法
func (c *L2Redis) Del(key string) {
	if c.client == nil {
		logger.Sugar().Error("Redis客户端未初始化")
		return
	}

	err := c.client.Del(c.ctx, key).Err()
	if err != nil {
		logger.Sugar().Errorf("Redis删除失败: %v", err)
	}
}

// Close 实现Cache接口的Close方法
func (c *L2Redis) Close() {
	if c.client == nil {
		return
	}

	redisCloseOnce.Do(func() {
		err := c.client.Close()
		if err != nil {
			logger.Sugar().Errorf("关闭Redis连接失败: %v", err)
		} else {
			logger.Sugar().Info("L2 Redis缓存已关闭")
		}
	})
}
