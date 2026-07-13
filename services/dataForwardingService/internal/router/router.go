package router

import (
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"Betterfly2/shared/mq"
	"data_forwarding_service/internal/connection"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"fmt"
	"os"
	"time"
)

// Router 负责消息路由
type Router struct {
	connManager *connection.ConnectionManager
}

var ErrUserOffline = errors.New("用户没有有效WebSocket路由")

// NewRouter 创建新的路由器
func NewRouter(connManager *connection.ConnectionManager) *Router {
	return &Router{
		connManager: connManager,
	}
}

// RouteMessage 路由消息到指定用户
func (r *Router) RouteMessage(toUserID string, message []byte) error {
	start := time.Now()
	sugar := logger.Sugar()

	// 1. 先尝试本地路由
	if r.routeLocally(toUserID, message) {
		sugar.Debugf("消息本地路由成功: %s", toUserID)
		metrics.RecordMessageRouted("local", start)
		return nil
	}

	// 2. 检查用户是否在其他容器在线
	targetContainerID, routeErr := redisClient.GetContainerByConnection(toUserID)
	if routeErr == nil {
		// 用户在其他容器在线，进行跨容器路由
		err := r.routeCrossContainer(toUserID, targetContainerID, message)
		if err != nil {
			metrics.RecordRoutingError()
		} else {
			metrics.RecordMessageRouted("cross_container", start)
		}
		return err
	}

	if !errors.Is(routeErr, redisClient.ErrRouteNotFound) {
		metrics.RecordRoutingError()
		return routeErr
	}

	// 离线消息已经由 storageService 持久化；实时投递失败必须显式返回，不能伪装成功。
	sugar.Infow("用户没有有效WebSocket路由", "user_id", toUserID)
	metrics.RecordRoutingError()
	return ErrUserOffline
}

// routeLocally 尝试本地路由
func (r *Router) routeLocally(toUserID string, message []byte) bool {
	// 检查用户是否在本地连接
	if _, exists := r.connManager.GetConnectionByUserID(toUserID); exists {
		// 发送消息到本地连接
		err := r.connManager.SendMessageToUser(toUserID, message)
		if err != nil {
			logger.Sugar().Errorf("本地消息发送失败: %v", err)
			return false
		}
		return true
	}
	return false
}

// routeCrossContainer 跨容器路由
func (r *Router) routeCrossContainer(toUserID string, targetContainerID string, message []byte) error {
	sugar := logger.Sugar()

	// 获取当前容器ID
	currentContainerID := os.Getenv("HOSTNAME")
	if currentContainerID == "" {
		currentContainerID = "local"
	}

	// 检查目标容器是否与当前容器相同
	if targetContainerID == currentContainerID {
		sugar.Warnf("Redis连接映射异常：用户 %s 应该在本地容器但本地路由失败，尝试重试本地路由", toUserID)
		// Redis数据可能过期，重试本地路由
		if r.routeLocally(toUserID, message) {
			sugar.Debugf("重试本地路由成功: %s", toUserID)
			return nil
		}
		sugar.Errorf("重试本地路由仍然失败: %s", toUserID)
		return fmt.Errorf("本地路由失败且Redis映射异常")
	}

	envBytes, err := mq.MarshalEnvelopeBytes(envelope.MessageType_DF_REQUEST, message)
	if err != nil {
		sugar.Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 通过Kafka转发到目标容器
	err = publisher.PublishMessage(string(envBytes), targetContainerID)
	if err != nil {
		sugar.Errorf("跨容器消息转发失败: %s -> %s, error: %v", toUserID, targetContainerID, err)
		metrics.RecordKafkaProcessingError()
		return err
	}

	sugar.Debugf("跨容器消息转发成功: %s (目标容器: %s)", toUserID, targetContainerID)
	metrics.RecordKafkaMessageProduced(targetContainerID)
	return nil
}

// BroadcastMessage 广播消息到多个用户
func (r *Router) BroadcastMessage(userIDs []string, message []byte) error {
	var failedUsers []string

	for _, userID := range userIDs {
		err := r.RouteMessage(userID, message)
		if err != nil {
			failedUsers = append(failedUsers, userID)
		}
	}

	if len(failedUsers) > 0 {
		return fmt.Errorf("部分用户消息发送失败: %v", failedUsers)
	}

	return nil
}

// CheckUserOnline 检查用户是否在线
func (r *Router) CheckUserOnline(userID string) bool {
	return r.connManager.IsUserLoggedIn(userID)
}

// GetOnlineUsers 获取在线用户列表
func (r *Router) GetOnlineUsers() []string {
	// 注意：这里只返回本地实例的在线用户
	// 如果需要全局在线用户，需要查询服务注册中心
	var onlineUsers []string

	// 这里需要实现遍历连接管理器获取所有已登录用户
	// 由于connection包的设计，暂时无法直接获取所有用户
	// 后续可以在connection包中添加相关方法

	return onlineUsers
}
