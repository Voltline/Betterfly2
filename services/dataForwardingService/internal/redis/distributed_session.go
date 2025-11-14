package redisClient

import (
	"errors"
	"fmt"
	"time"

	"Betterfly2/shared/logger"

	"github.com/redis/go-redis/v9"
)

// DistributedSessionManager 分布式会话管理器
type DistributedSessionManager struct{}

// AcquireUserLock 获取用户分布式锁
func (dsm *DistributedSessionManager) AcquireUserLock(userID string, timeout time.Duration) (bool, error) {
	lockKey := fmt.Sprintf("user_lock:%s", userID)

	// 使用SET NX EX实现分布式锁
	result, err := Rdb.SetNX(ctx, lockKey, "locked", timeout).Result()
	if err != nil {
		logger.Sugar().Errorf("获取用户锁失败: %v", err)
		return false, err
	}

	return result, nil
}

// ReleaseUserLock 释放用户分布式锁
func (dsm *DistributedSessionManager) ReleaseUserLock(userID string) error {
	lockKey := fmt.Sprintf("user_lock:%s", userID)
	_, err := Rdb.Del(ctx, lockKey).Result()
	if err != nil {
		logger.Sugar().Errorf("释放用户锁失败: %v", err)
		return err
	}
	return nil
}

// GetUserSession 获取用户全局会话
func (dsm *DistributedSessionManager) GetUserSession(userID string) (string, error) {
	sessionKey := fmt.Sprintf("user_session:%s", userID)
	sessionData, err := Rdb.Get(ctx, sessionKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil // 没有会话
		}
		logger.Sugar().Errorf("获取用户会话失败: %v", err)
		return "", err
	}
	return sessionData, nil
}

// SetUserSession 设置用户全局会话
func (dsm *DistributedSessionManager) SetUserSession(userID string, connectionID string, containerID string, ttl time.Duration) error {
	sessionKey := fmt.Sprintf("user_session:%s", userID)
	sessionData := fmt.Sprintf("%s:%s", connectionID, containerID)

	_, err := Rdb.SetEx(ctx, sessionKey, sessionData, ttl).Result()
	if err != nil {
		logger.Sugar().Errorf("设置用户会话失败: %v", err)
		return err
	}
	return nil
}

// RemoveUserSession 移除用户全局会话
func (dsm *DistributedSessionManager) RemoveUserSession(userID string) error {
	sessionKey := fmt.Sprintf("user_session:%s", userID)
	_, err := Rdb.Del(ctx, sessionKey).Result()
	if err != nil {
		logger.Sugar().Errorf("移除用户会话失败: %v", err)
		return err
	}
	return nil
}

// PublishKickNotification 发布踢出通知（使用Redis Pub/Sub实现实时通知）
func (dsm *DistributedSessionManager) PublishKickNotification(userID string, targetContainerID string) error {
	channel := fmt.Sprintf("user_kick:%s", targetContainerID)
	message := fmt.Sprintf("KICK:%s", userID)

	_, err := Rdb.Publish(ctx, channel, message).Result()
	if err != nil {
		logger.Sugar().Errorf("发布踢出通知失败: %v", err)
		return err
	}

	logger.Sugar().Infof("发布实时踢出通知: 用户 %s -> 容器 %s", userID, targetContainerID)
	return nil
}

// SubscribeKickNotifications 订阅踢出通知
func (dsm *DistributedSessionManager) SubscribeKickNotifications(containerID string, handler func(userID string)) error {
	channel := fmt.Sprintf("user_kick:%s", containerID)
	pubsub := Rdb.Subscribe(ctx, channel)

	// 验证订阅
	_, err := pubsub.Receive(ctx)
	if err != nil {
		logger.Sugar().Errorf("订阅踢出通知失败: %v", err)
		return err
	}

	// 在后台处理消息
	go func() {
		ch := pubsub.Channel()
		for msg := range ch {
			if len(msg.Payload) > 5 && msg.Payload[:5] == "KICK:" {
				userID := msg.Payload[5:]
				logger.Sugar().Infof("收到实时踢出通知: 用户 %s", userID)
				go handler(userID)
			}
		}
	}()

	logger.Sugar().Infof("已订阅实时踢出通知: 容器 %s", containerID)
	return nil
}

// ParseSessionData 解析会话数据
func (dsm *DistributedSessionManager) ParseSessionData(sessionData string) (connectionID string, containerID string) {
	if sessionData == "" {
		return "", ""
	}

	// 格式: connectionID:containerID
	for i := len(sessionData) - 1; i >= 0; i-- {
		if sessionData[i] == ':' {
			return sessionData[:i], sessionData[i+1:]
		}
	}
	return sessionData, "" // 没有容器ID的旧格式
}
