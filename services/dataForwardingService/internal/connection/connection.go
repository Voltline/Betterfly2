package connection

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Connection 表示一个WebSocket连接
type Connection struct {
	ID         string
	UserID     string // 用户ID，登录前为空
	Conn       *websocket.Conn
	SendChan   chan []byte
	ShouldStop bool
	LoggedIn   bool
}

// ConnectionManager 管理所有WebSocket连接
type ConnectionManager struct {
	connections     *sync.Map // connectionID -> *Connection
	userConnections *sync.Map // userID -> connectionID
	mutex           sync.RWMutex
	instanceID      string // 实例标识符，用于调试
}

// NewConnectionManager 创建新的连接管理器
func NewConnectionManager() *ConnectionManager {
	instanceID := fmt.Sprintf("cm-%d", time.Now().UnixNano())
	logger.Sugar().Debugf("创建连接管理器: %s", instanceID)
	return &ConnectionManager{
		connections:     &sync.Map{},
		userConnections: &sync.Map{},
		instanceID:      instanceID,
	}
}

// AddConnection 添加新连接（未登录状态）
func (cm *ConnectionManager) AddConnection(conn *websocket.Conn) *Connection {
	connectionID := conn.RemoteAddr().String()

	connection := &Connection{
		ID:         connectionID,
		UserID:     "", // 未登录时为空
		Conn:       conn,
		SendChan:   make(chan []byte, 256),
		ShouldStop: false,
		LoggedIn:   false,
	}

	cm.connections.Store(connectionID, connection)
	logger.Sugar().Debugf("新连接建立: %s", connectionID)

	return connection
}

// Login 用户登录，绑定用户ID
func (cm *ConnectionManager) Login(connectionID string, userID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// 获取容器标识符（使用HOSTNAME作为唯一标识）
	containerID := os.Getenv("HOSTNAME")
	if containerID == "" {
		containerID = "local"
	}

	// 1. 获取分布式锁，防止并发登录
	dsm := &redisClient.DistributedSessionManager{}
	locked, err := dsm.AcquireUserLock(userID, 5*time.Second) // 减少锁时间，提高并发性
	if err != nil {
		return fmt.Errorf("获取分布式锁失败: %v", err)
	}
	if !locked {
		return fmt.Errorf("用户正在其他设备登录，请稍后重试")
	}
	defer dsm.ReleaseUserLock(userID)

	// 2. 检查全局会话状态
	existingSession, err := dsm.GetUserSession(userID)
	if err != nil {
		return fmt.Errorf("检查全局会话失败: %v", err)
	}

	// 3. 如果用户已在其他容器登录，发送踢出消息并等待
	if existingSession != "" {
		_, existingContainerID := dsm.ParseSessionData(existingSession)

		// 如果会话在不同容器，发送实时踢出通知
		if existingContainerID != containerID && existingContainerID != "" {
			// 使用Redis Pub/Sub发送实时踢出通知
			err := dsm.PublishKickNotification(userID, existingContainerID)
			if err != nil {
				logger.Sugar().Errorf("发送实时踢出通知失败: %v", err)
				// 降级到Kafka方案
				kickMessage := fmt.Sprintf("DELETE USER %s TARGET %s", userID, existingContainerID)
				if kafkaErr := publisher.PublishMessage(kickMessage, "user-kick-topic"); kafkaErr != nil {
					logger.Sugar().Errorf("Kafka降级方案也失败: %v", kafkaErr)
				}
			} else {
				logger.Sugar().Infof("检测到跨容器登录，发送实时踢出通知: %s -> %s", existingContainerID, containerID)
			}

			// 快速检查：立即尝试登录，如果失败则说明踢出尚未完成
			// 这样可以避免不必要的等待，让客户端快速重试
			logger.Sugar().Infof("已发送踢出通知，立即尝试登录，如果失败请客户端重试")
		}
	}

	// 4. 检查本地是否已有该用户的连接
	if oldConnectionID, exists := cm.userConnections.Load(userID); exists {
		// 强制登出本地旧连接
		if oldConn, ok := cm.connections.Load(oldConnectionID); ok {
			oldConnection := oldConn.(*Connection)
			oldConnection.Conn.Close()
			oldConnection.ShouldStop = true
			cm.connections.Delete(oldConnectionID)
			logger.Sugar().Debugf("强制登出本地旧连接: %s", oldConnectionID)
		}
	}

	// 5. 更新连接信息
	if conn, ok := cm.connections.Load(connectionID); ok {
		connection := conn.(*Connection)
		connection.UserID = userID
		connection.LoggedIn = true

		// 更新用户-连接映射
		cm.userConnections.Store(userID, connectionID)
		logger.Sugar().Debugf("[%s] Login: 设置用户连接映射 %s -> %s", cm.instanceID, userID, connectionID)

		// 6. 最终冲突检查：在设置会话前再次检查，防止竞态条件
		// 增加重试机制，等待踢出操作完成
		maxRetries := 3
		retryDelay := 100 * time.Millisecond
		var finalSession string
		var finalContainerID string

		for i := 0; i < maxRetries; i++ {
			finalSession, err = dsm.GetUserSession(userID)
			if err != nil {
				break
			}
			if finalSession == "" {
				// 会话已被清理，可以继续
				break
			}

			_, finalContainerID = dsm.ParseSessionData(finalSession)
			// 如果会话仍然在其他容器，等待后重试
			if finalContainerID != containerID && finalContainerID != "" {
				if i < maxRetries-1 {
					logger.Sugar().Debugf("登录冲突检测重试 %d/%d: 用户 %s 仍在容器 %s 登录，等待 %v 后重试",
						i+1, maxRetries, userID, finalContainerID, retryDelay)
					time.Sleep(retryDelay)
					continue
				} else {
					// 最后一次重试仍然失败，尝试强制清理会话
					logger.Sugar().Warnf("登录冲突检测：用户 %s 仍在容器 %s 登录，尝试强制清理会话", userID, finalContainerID)
					// 直接删除用户会话，因为踢出通知可能已发送但目标容器未处理
					if cleanupErr := dsm.RemoveUserSession(userID); cleanupErr != nil {
						logger.Sugar().Errorf("强制清理用户会话失败: %v", cleanupErr)
					} else {
						logger.Sugar().Infof("已强制清理用户 %s 的会话，重新检查", userID)
						// 清理后立即再次检查
						time.Sleep(50 * time.Millisecond) // 短暂等待确保Redis更新
						finalSession, err = dsm.GetUserSession(userID)
						if err == nil && finalSession == "" {
							// 会话已清理，可以继续
							break
						}
					}
					// 如果强制清理后仍然有会话，返回错误
					logger.Sugar().Warnf("登录冲突检测：用户 %s 仍在容器 %s 登录，踢出可能失败", userID, finalContainerID)
					return fmt.Errorf("登录冲突，请稍后重试")
				}
			} else {
				// 会话在当前容器或已清理，可以继续
				break
			}
		}

		// 7. 更新全局会话
		err = dsm.SetUserSession(userID, connectionID, containerID, 24*time.Hour)
		if err != nil {
			logger.Sugar().Errorf("更新全局会话失败: %v", err)
			// 不返回错误，继续执行
		}

		// 8. 注册连接映射
		err = redisClient.RegisterConnection(userID, containerID)
		if err != nil {
			logger.Sugar().Errorf("注册连接映射失败: %v", err)
			// 不返回错误，继续执行
		}

		logger.Sugar().Infof("用户登录成功: %s (容器: %s)", userID, containerID)
		return nil
	}

	return fmt.Errorf("连接不存在: %s", connectionID)
}

// RemoveConnection 移除连接
func (cm *ConnectionManager) RemoveConnection(connectionID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if conn, ok := cm.connections.Load(connectionID); ok {
		connection := conn.(*Connection)

		// 如果已登录，清理用户映射和全局会话
		if connection.LoggedIn && connection.UserID != "" {
			cm.userConnections.Delete(connection.UserID)

			// 清理全局会话（需要检查是否为当前会话）
			dsm := &redisClient.DistributedSessionManager{}
			existingSession, err := dsm.GetUserSession(connection.UserID)
			if err == nil && existingSession != "" {
				existingConnID, _ := dsm.ParseSessionData(existingSession)
				// 只有当当前连接是全局会话中的连接时才清理
				if existingConnID == connectionID {
					dsm.RemoveUserSession(connection.UserID)
					logger.Sugar().Debugf("清理全局会话: %s", connection.UserID)
				}
			}

			// 清理连接映射
			currentContainerID := os.Getenv("HOSTNAME")
			if currentContainerID == "" {
				currentContainerID = "local"
			}
			err = redisClient.UnregisterConnection(connection.UserID, currentContainerID)
			if err != nil {
				logger.Sugar().Warnf("清理连接映射失败: %v", err)
			}
		}

		// 关闭连接
		connection.Conn.Close()
		connection.ShouldStop = true

		// 安全关闭通道，避免重复关闭
		select {
		case <-connection.SendChan:
			// 通道已关闭，不做任何操作
		default:
			close(connection.SendChan)
		}

		// 从连接池中移除
		cm.connections.Delete(connectionID)

		logger.Sugar().Debugf("连接已移除: %s", connectionID)
	}
}

// GetConnectionByUserID 通过用户ID获取连接
func (cm *ConnectionManager) GetConnectionByUserID(userID string) (*Connection, bool) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	if connectionID, ok := cm.userConnections.Load(userID); ok {
		if conn, ok := cm.connections.Load(connectionID); ok {
			return conn.(*Connection), true
		}
	}

	return nil, false
}

// GetConnectionByID 通过连接ID获取连接
func (cm *ConnectionManager) GetConnectionByID(connectionID string) (*Connection, bool) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	if conn, ok := cm.connections.Load(connectionID); ok {
		return conn.(*Connection), true
	}

	return nil, false
}

// GetInstanceID 获取连接管理器实例ID
func (cm *ConnectionManager) GetInstanceID() string {
	return cm.instanceID
}

// SendMessageToUser 向指定用户发送消息
func (cm *ConnectionManager) SendMessageToUser(userID string, message []byte) error {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	logger.Sugar().Debugf("[%s] SendMessageToUser: 查找用户 %s", cm.instanceID, userID)

	// 调试：打印所有用户连接
	cm.userConnections.Range(func(key, value interface{}) bool {
		logger.Sugar().Debugf("[%s] 用户连接映射: %s -> %s", cm.instanceID, key, value)
		return true
	})

	if connectionID, ok := cm.userConnections.Load(userID); ok {
		logger.Sugar().Debugf("[%s] 找到用户连接: %s -> %s", cm.instanceID, userID, connectionID)
		if conn, ok := cm.connections.Load(connectionID); ok {
			connection := conn.(*Connection)
			select {
			case connection.SendChan <- message:
				logger.Sugar().Debugf("[%s] 消息发送成功: %s", cm.instanceID, userID)
				return nil
			default:
				return fmt.Errorf("发送通道已满: %s", userID)
			}
		}
	}

	logger.Sugar().Errorf("[%s] 用户未连接: %s", cm.instanceID, userID)
	return fmt.Errorf("用户未连接: %s", userID)
}

// IsUserLoggedIn 检查用户是否已登录
func (cm *ConnectionManager) IsUserLoggedIn(userID string) bool {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	_, exists := cm.userConnections.Load(userID)
	return exists
}

// GetConnectionCount 获取当前连接数
func (cm *ConnectionManager) GetConnectionCount() int {
	count := 0
	cm.connections.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// GetLoggedInUserCount 获取已登录用户数
func (cm *ConnectionManager) GetLoggedInUserCount() int {
	count := 0
	cm.userConnections.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
