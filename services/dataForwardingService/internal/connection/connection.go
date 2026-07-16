package connection

import (
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"context"
	"crypto/rand"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Connection struct {
	ID            string
	UserID        string
	OwnerToken    string
	Conn          *websocket.Conn
	SendChan      chan []byte
	ShouldStop    bool
	LoggedIn      bool
	sendMu        sync.RWMutex
	closeOnce     sync.Once
	closed        atomic.Bool
	authenticated atomic.Bool
	done          chan struct{}
}

type ConnectionManager struct {
	connections         *sync.Map
	userConnections     *sync.Map
	mutex               sync.RWMutex
	instanceID          string
	connectionCount     int64
	loggedInCount       int64
	userLocks           *keyedLocker
	beforeExternalLogin func(context.Context, string) error
	sessionLeaseTTL     time.Duration
	routeLeaseTTL       time.Duration
}

func NewConnectionManager() *ConnectionManager {
	instanceID := fmt.Sprintf("cm-%d", time.Now().UnixNano())
	return &ConnectionManager{
		connections:     &sync.Map{},
		userConnections: &sync.Map{},
		instanceID:      instanceID,
		userLocks:       newKeyedLocker(),
		sessionLeaseTTL: 2 * time.Minute,
		routeLeaseTTL:   90 * time.Second,
	}
}

func (cm *ConnectionManager) ConfigureSessionLeases(sessionTTL, routeTTL time.Duration) {
	if sessionTTL > 0 {
		cm.sessionLeaseTTL = sessionTTL
	}
	if routeTTL > 0 {
		cm.routeLeaseTTL = routeTTL
	}
}

func (cm *ConnectionManager) AddConnection(conn *websocket.Conn) *Connection {
	connection := &Connection{
		ID:       conn.RemoteAddr().String(),
		Conn:     conn,
		SendChan: make(chan []byte, 256),
		done:     make(chan struct{}),
	}
	cm.connections.Store(connection.ID, connection)
	atomic.AddInt64(&cm.connectionCount, 1)
	metrics.RecordWebSocketConnectionOpened()
	return connection
}

// Login first claims distributed ownership and only then commits local state.
func (cm *ConnectionManager) Login(ctx context.Context, connectionID, rawUserID string) error {
	parsedUserID, err := strconv.ParseInt(strings.TrimSpace(rawUserID), 10, 64)
	if err != nil || parsedUserID <= 0 {
		return fmt.Errorf("无效用户ID: %q", rawUserID)
	}
	userID := strconv.FormatInt(parsedUserID, 10)
	unlock, err := cm.userLocks.Lock(ctx, userID)
	if err != nil {
		return err
	}
	defer unlock()

	if cm.beforeExternalLogin != nil {
		if err := cm.beforeExternalLogin(ctx, userID); err != nil {
			return err
		}
	}
	if connection, ok := cm.connections.Load(connectionID); !ok || connection.(*Connection).IsClosed() || connection.(*Connection).IsAuthenticated() {
		return fmt.Errorf("连接不存在或状态不允许登录: %s", connectionID)
	}

	ownerToken, err := newOwnerToken()
	if err != nil {
		return err
	}
	containerID := currentContainerID()
	dsm := &redisClient.DistributedSessionManager{}
	if err := acquireDistributedUserLock(ctx, dsm, userID, ownerToken); err != nil {
		return err
	}
	defer releaseDistributedUserLock(dsm, userID, ownerToken)

	previous, exists, err := dsm.GetUserSession(ctx, userID)
	if err != nil {
		return fmt.Errorf("读取现有会话失败: %w", err)
	}
	if exists && previous.ContainerID != "" && previous.OwnerToken != "" {
		if err := dsm.PublishOwnedKickNotification(ctx, userID, previous.ContainerID, previous.OwnerToken); err != nil {
			logger.Sugar().Warnw("发送旧会话踢出通知失败", "user_id", userID, "container_id", previous.ContainerID, "error", err)
			kick := fmt.Sprintf("DELETE USER %s TARGET %s OWNER %s", userID, previous.ContainerID, previous.OwnerToken)
			if kafkaErr := publisher.PublishRawMessageContext(ctx, []byte(kick), "user-kick-topic", nil); kafkaErr != nil {
				return fmt.Errorf("发布旧会话踢出通知失败: redis=%v kafka=%w", err, kafkaErr)
			}
		}
	}

	claimed := redisClient.SessionData{ConnectionID: connectionID, ContainerID: containerID, OwnerToken: ownerToken}
	if err := dsm.ClaimSessionAndRoute(ctx, userID, claimed, cm.sessionLeaseTTL, cm.routeLeaseTTL); err != nil {
		return fmt.Errorf("声明会话所有权失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		cleanupOwnedSession(dsm, userID, claimed)
		return err
	}
	oldConnection, err := cm.commitLocalLogin(connectionID, userID, ownerToken)
	if err != nil {
		cleanupOwnedSession(dsm, userID, claimed)
		return err
	}
	if oldConnection != nil {
		oldConnection.Close()
	}
	metrics.UpdateOnlineUsers(cm.GetLoggedInUserCount())
	logger.Sugar().Infof("用户登录成功: %s (容器: %s)", userID, containerID)
	return nil
}

func (cm *ConnectionManager) commitLocalLogin(connectionID, userID, ownerToken string) (*Connection, error) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	value, ok := cm.connections.Load(connectionID)
	if !ok || value.(*Connection).IsClosed() || value.(*Connection).IsAuthenticated() {
		return nil, fmt.Errorf("连接不存在或状态已变化: %s", connectionID)
	}
	var old *Connection
	if oldID, exists := cm.userConnections.Load(userID); exists && oldID.(string) != connectionID {
		if oldValue, found := cm.connections.LoadAndDelete(oldID.(string)); found {
			old = oldValue.(*Connection)
			atomic.AddInt64(&cm.connectionCount, -1)
			if old.IsAuthenticated() {
				atomic.AddInt64(&cm.loggedInCount, -1)
			}
		}
	}
	connection := value.(*Connection)
	connection.MarkAuthenticated(userID, ownerToken)
	cm.userConnections.Store(userID, connectionID)
	atomic.AddInt64(&cm.loggedInCount, 1)
	return old, nil
}

func acquireDistributedUserLock(ctx context.Context, dsm *redisClient.DistributedSessionManager, userID, ownerToken string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		locked, err := dsm.AcquireUserLock(ctx, userID, ownerToken, 30*time.Second)
		if err != nil {
			return fmt.Errorf("获取分布式用户锁失败: %w", err)
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func releaseDistributedUserLock(dsm *redisClient.DistributedSessionManager, userID, ownerToken string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = dsm.ReleaseUserLock(ctx, userID, ownerToken)
}

func cleanupOwnedSession(dsm *redisClient.DistributedSessionManager, userID string, data redisClient.SessionData) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = dsm.RemoveOwnedSessionAndRoute(ctx, userID, data)
}

func newOwnerToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func currentContainerID() string {
	if value := os.Getenv("HOSTNAME"); value != "" {
		return value
	}
	return "local"
}

func (cm *ConnectionManager) RemoveConnection(connectionID string) {
	connection, removeOwnership := cm.detachConnection(connectionID)
	if connection == nil {
		return
	}
	connection.Close()
	metrics.RecordWebSocketConnectionClosed()
	if removeOwnership {
		cleanupOwnedSession(&redisClient.DistributedSessionManager{}, connection.UserID, redisClient.SessionData{
			ConnectionID: connection.ID,
			ContainerID:  currentContainerID(),
			OwnerToken:   connection.OwnerToken,
		})
	}
	metrics.UpdateOnlineUsers(cm.GetLoggedInUserCount())
}

func (cm *ConnectionManager) detachConnection(connectionID string) (*Connection, bool) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	value, ok := cm.connections.LoadAndDelete(connectionID)
	if !ok {
		return nil, false
	}
	connection := value.(*Connection)
	atomic.AddInt64(&cm.connectionCount, -1)
	removeOwnership := false
	if connection.IsAuthenticated() && connection.UserID != "" {
		if mapped, exists := cm.userConnections.Load(connection.UserID); exists && mapped.(string) == connectionID {
			cm.userConnections.Delete(connection.UserID)
			atomic.AddInt64(&cm.loggedInCount, -1)
			removeOwnership = true
		}
	}
	return connection, removeOwnership
}

func (cm *ConnectionManager) GetConnectionByUserID(userID string) (*Connection, bool) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	if connectionID, ok := cm.userConnections.Load(userID); ok {
		if value, exists := cm.connections.Load(connectionID); exists {
			return value.(*Connection), true
		}
	}
	return nil, false
}

func (cm *ConnectionManager) GetConnectionByID(connectionID string) (*Connection, bool) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	value, ok := cm.connections.Load(connectionID)
	if !ok {
		return nil, false
	}
	return value.(*Connection), true
}

func (cm *ConnectionManager) GetInstanceID() string { return cm.instanceID }

func (cm *ConnectionManager) SendMessageToUser(userID string, message []byte) error {
	connection, ok := cm.GetConnectionByUserID(userID)
	if !ok {
		return fmt.Errorf("用户未连接: %s", userID)
	}
	return connection.EnqueueMessage(message)
}

func (cm *ConnectionManager) GetConnectionCount() int {
	return int(atomic.LoadInt64(&cm.connectionCount))
}
func (cm *ConnectionManager) GetLoggedInUserCount() int {
	return int(atomic.LoadInt64(&cm.loggedInCount))
}

func (cm *ConnectionManager) StopUserIfOwner(userID, ownerToken string) bool {
	connection, ok := cm.GetConnectionByUserID(userID)
	if !ok || ownerToken != "*" && connection.OwnerToken != ownerToken {
		return false
	}
	cm.RemoveConnection(connection.ID)
	return true
}

func (c *Connection) EnqueueMessage(message []byte) error {
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	if c.closed.Load() {
		return fmt.Errorf("连接已关闭: %s", c.ID)
	}
	select {
	case c.SendChan <- message:
		return nil
	default:
		return fmt.Errorf("发送通道已满: %s", c.ID)
	}
}

func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		c.sendMu.Lock()
		c.ShouldStop = true
		c.closed.Store(true)
		close(c.SendChan)
		if c.done != nil {
			close(c.done)
		}
		c.sendMu.Unlock()
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	})
}

func (c *Connection) IsClosed() bool { return c.closed.Load() }

func (c *Connection) MarkAuthenticated(userID, ownerToken string) {
	c.UserID = userID
	c.OwnerToken = ownerToken
	c.LoggedIn = true
	c.authenticated.Store(true)
}

func (c *Connection) IsAuthenticated() bool { return c.authenticated.Load() }
func (c *Connection) Done() <-chan struct{} { return c.done }
