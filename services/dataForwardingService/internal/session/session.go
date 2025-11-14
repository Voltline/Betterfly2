package session

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/connection"
	"fmt"
	"sync"
	"time"
)

// Session 表示用户会话
type Session struct {
	UserID       string
	ConnectionID string
	LoginTime    time.Time
	LastActive   time.Time
	IsActive     bool
}

// SessionManager 管理用户会话
type SessionManager struct {
	sessions *sync.Map // userID -> *Session
	mutex    sync.RWMutex
}

// NewSessionManager 创建新的会话管理器
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: &sync.Map{},
	}
}

// CreateSession 创建新会话
func (sm *SessionManager) CreateSession(userID string, connectionID string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// 检查是否已有会话
	if existing, exists := sm.sessions.Load(userID); exists {
		existingSession := existing.(*Session)
		if existingSession.IsActive {
			return fmt.Errorf("用户已有活跃会话: %s", userID)
		}
	}

	session := &Session{
		UserID:       userID,
		ConnectionID: connectionID,
		LoginTime:    time.Now(),
		LastActive:   time.Now(),
		IsActive:     true,
	}

	sm.sessions.Store(userID, session)
	logger.Sugar().Infof("新会话创建: %s -> %s", userID, connectionID)

	return nil
}

// GetSession 获取会话
func (sm *SessionManager) GetSession(userID string) (*Session, bool) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	if session, exists := sm.sessions.Load(userID); exists {
		return session.(*Session), true
	}

	return nil, false
}

// UpdateSessionActivity 更新会话活跃时间
func (sm *SessionManager) UpdateSessionActivity(userID string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if session, exists := sm.sessions.Load(userID); exists {
		session.(*Session).LastActive = time.Now()
		return nil
	}

	return fmt.Errorf("会话不存在: %s", userID)
}

// RemoveSession 移除会话
func (sm *SessionManager) RemoveSession(userID string) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if session, exists := sm.sessions.Load(userID); exists {
		session.(*Session).IsActive = false
		sm.sessions.Delete(userID)
		logger.Sugar().Infof("会话已移除: %s", userID)
		return nil
	}

	return fmt.Errorf("会话不存在: %s", userID)
}

// ForceLogout 强制登出用户
func (sm *SessionManager) ForceLogout(userID string, connManager *connection.ConnectionManager) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	logger.Sugar().Infof("开始强制登出用户: %s", userID)

	if session, exists := sm.sessions.Load(userID); exists {
		sessionObj := session.(*Session)
		logger.Sugar().Infof("找到用户会话，连接ID: %s", sessionObj.ConnectionID)

		// 移除连接
		connManager.RemoveConnection(sessionObj.ConnectionID)

		// 移除会话
		sessionObj.IsActive = false
		sm.sessions.Delete(userID)

		logger.Sugar().Infof("用户被强制登出: %s", userID)
		return nil
	}

	logger.Sugar().Warnf("会话不存在，无法强制登出: %s", userID)
	return fmt.Errorf("会话不存在: %s", userID)
}

// GetActiveSessions 获取所有活跃会话
func (sm *SessionManager) GetActiveSessions() []*Session {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()

	var activeSessions []*Session
	sm.sessions.Range(func(_, value interface{}) bool {
		session := value.(*Session)
		if session.IsActive {
			activeSessions = append(activeSessions, session)
		}
		return true
	})

	return activeSessions
}

// GetSessionCount 获取会话总数
func (sm *SessionManager) GetSessionCount() int {
	count := 0
	sm.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// CleanupInactiveSessions 清理非活跃会话
func (sm *SessionManager) CleanupInactiveSessions(timeout time.Duration, connManager *connection.ConnectionManager) int {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	cleaned := 0
	now := time.Now()

	sm.sessions.Range(func(key, value interface{}) bool {
		session := value.(*Session)
		if session.IsActive && now.Sub(session.LastActive) > timeout {
			// 移除连接
			connManager.RemoveConnection(session.ConnectionID)

			// 移除会话
			session.IsActive = false
			sm.sessions.Delete(key)
			cleaned++

			logger.Sugar().Infof("清理非活跃会话: %s", session.UserID)
		}
		return true
	})

	return cleaned
}
