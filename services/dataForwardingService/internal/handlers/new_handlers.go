package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/logger"
	"context"
	"data_forwarding_service/internal/connection"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/router"
	"data_forwarding_service/internal/session"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

// WebSocketHandler 处理WebSocket连接和消息
type WebSocketHandler struct {
	connManager    *connection.ConnectionManager
	sessionManager *session.SessionManager
	router         *router.Router
	config         websocketConfig
	upgrader       websocket.Upgrader
}

// NewWebSocketHandler 创建新的WebSocket处理器
func NewWebSocketHandler() *WebSocketHandler {
	logger.Sugar().Debugf("创建WebSocketHandler实例")
	connManager := connection.NewConnectionManager()
	sessionManager := session.NewSessionManager()
	router := router.NewRouter(connManager)

	handler := &WebSocketHandler{
		connManager:    connManager,
		sessionManager: sessionManager,
		router:         router,
		config:         loadWebSocketConfig(),
	}
	handler.upgrader.CheckOrigin = handler.config.checkOrigin

	// 订阅实时踢出通知
	handler.subscribeKickNotifications()

	logger.Sugar().Debugf("WebSocketHandler创建完成: %s", connManager.GetInstanceID())
	return handler
}

// StartWebSocketServer 启动WebSocket服务器
func (h *WebSocketHandler) StartWebSocketServer() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "54342"
	}

	certFile := os.Getenv("CERT_PATH")
	if certFile == "" {
		certFile = "./certs/cert.pem"
	}

	keyFile := os.Getenv("KEY_PATH")
	if keyFile == "" {
		keyFile = "./certs/key.pem"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.handleConnection)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: h.config.readHeaderTimeout,
		IdleTimeout:       h.config.idleTimeout,
		MaxHeaderBytes:    h.config.maxHeaderBytes,
	}
	return server.ListenAndServeTLS(certFile, keyFile)
}

// handleConnection 处理WebSocket连接
func (h *WebSocketHandler) handleConnection(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		sugar.Errorf("连接错误: %s", err)
		return
	}

	// 创建新连接
	connection := h.connManager.AddConnection(conn)
	conn.SetReadLimit(h.config.maxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(h.config.authTimeout))
	conn.SetPongHandler(func(string) error {
		if !connection.IsAuthenticated() {
			return nil
		}
		return conn.SetReadDeadline(time.Now().Add(h.config.pongWait))
	})

	sugar.Debugf("已与 %v 建立连接", conn.RemoteAddr())

	// 启动读写协程
	go h.readProcess(connection)
	go h.writeToClient(connection)
}

func (h *WebSocketHandler) refreshRouteLease(conn *connection.Connection, userID string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-conn.Done():
			return
		case <-ticker.C:
			containerID := os.Getenv("HOSTNAME")
			if containerID == "" {
				containerID = "local"
			}
			ctx, cancel := context.WithTimeout(context.Background(), h.config.writeTimeout)
			err := redisClient.RefreshConnection(ctx, userID, containerID, conn.OwnerToken)
			cancel()
			if err != nil {
				logger.Sugar().Warnf("刷新WebSocket路由租约失败: user_id=%s error=%v", userID, err)
			}
		}
	}
}

// readProcess 读取处理协程
func (h *WebSocketHandler) readProcess(conn *connection.Connection) {
	sugar := logger.Sugar()
	defer func() {
		// 连接关闭时清理
		wasCurrentSession := conn.IsAuthenticated() && conn.UserID != "" && h.connManager.IsCurrentUserConnection(conn.UserID, conn.ID)
		h.connManager.RemoveConnection(conn.ID)
		if wasCurrentSession {
			h.sessionManager.RemoveSession(conn.UserID)
		}
		sugar.Debugf("(%v, %v)连接已关闭", conn.ID, conn.Conn.RemoteAddr())
	}()

	for {
		// 处理消息接收
		_, p, err := conn.Conn.ReadMessage()

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() && !conn.IsAuthenticated() {
				_ = conn.Conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication timeout"),
					time.Now().Add(h.config.writeTimeout),
				)
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				sugar.Infof("连接关闭，读协程退出")
			} else {
				sugar.Errorln("获取信息异常: ", err)
			}
			// 不再在这里关闭通道，由RemoveConnection统一处理
			break
		}

		if len(p) == 0 {
			continue
		}

		requestMsg, err := HandleRequestData(p)
		if err != nil {
			sugar.Warnf("收到非标准化数据: %v", err)
			continue
		}

		// 根据登录状态处理消息
		if !conn.IsAuthenticated() {
			h.handleUnauthenticatedMessage(conn, requestMsg)
		} else {
			h.handleAuthenticatedMessage(conn, requestMsg)
		}

		sugar.Debugf("收到WebSocket消息: %d bytes", len(p))
	}
}

// handleUnauthenticatedMessage 处理未认证消息
func (h *WebSocketHandler) handleUnauthenticatedMessage(conn *connection.Connection, requestMsg *pb.RequestMessage) {
	switch requestMsg.Payload.(type) {
	case *pb.RequestMessage_Login:
		h.handleLogin(conn, requestMsg)
	case *pb.RequestMessage_Signup:
		h.handleSignup(conn, requestMsg)
	case *pb.RequestMessage_Logout:
		// 终止连接
		conn.Conn.Close()
	default:
		logger.Sugar().Errorln("未登录时不处理其他类型信息")
		h.sendRefusedResponse(conn)
	}
}

// handleAuthenticatedMessage 处理已认证消息
func (h *WebSocketHandler) handleAuthenticatedMessage(conn *connection.Connection, requestMsg *pb.RequestMessage) {
	userID, err := strconv.ParseInt(conn.UserID, 10, 64)
	if err != nil {
		logger.Sugar().Errorf("无法将 %s 转为int64: %v", conn.UserID, err)
		return
	}

	res, err := RequestMessageHandler(userID, requestMsg)
	if err != nil {
		logger.Sugar().Errorf("消息处理错误: %v", err)
	}

	if res == 1 {
		// 收到logout报文，需要断开连接
		conn.Conn.Close()
	}
}

// handleLogin 处理登录
func (h *WebSocketHandler) handleLogin(conn *connection.Connection, requestMsg *pb.RequestMessage) {
	rsp, realUserID, err := HandleLoginMessage(requestMsg)
	logger.Sugar().Infof("登录响应: result=%s user_id=%d", rsp.GetLogin().GetResult(), realUserID)

	if err != nil {
		logger.Sugar().Errorf("登录出现错误: %v", err)
		h.sendResponse(conn, rsp)
		return
	}
	if !loginResponseAllowsBinding(rsp, realUserID) {
		logger.Sugar().Infof("认证未通过，不创建用户会话: result=%s user_id=%d", rsp.GetLogin().GetResult(), realUserID)
		h.sendResponse(conn, rsp)
		return
	}

	// 登录成功，绑定用户ID
	userIDStr := strconv.FormatInt(realUserID, 10)
	loginCtx, cancel := context.WithTimeout(context.Background(), h.config.authTimeout)
	defer cancel()
	err = h.connManager.Login(loginCtx, conn.ID, userIDStr)
	if err != nil {
		logger.Sugar().Errorf("绑定用户ID失败: %v", err)
		rsp = &pb.ResponseMessage{
			Payload: &pb.ResponseMessage_Login{
				Login: &pb.LoginRsp{
					Result: pb.LoginResult_LOGIN_SVR_ERROR,
				},
			},
		}
		h.sendResponse(conn, rsp)
		return
	}

	// 创建会话
	h.sessionManager.ReplaceSession(userIDStr, conn.ID)
	go h.refreshRouteLease(conn, userIDStr)
	_ = conn.Conn.SetReadDeadline(time.Now().Add(h.config.pongWait))

	// 返回登录结果
	h.sendResponse(conn, rsp)
}

func loginResponseAllowsBinding(response *pb.ResponseMessage, userID int64) bool {
	return response != nil && response.GetLogin() != nil && response.GetLogin().GetResult() == pb.LoginResult_LOGIN_OK && userID > 0
}

// handleSignup 处理注册
func (h *WebSocketHandler) handleSignup(conn *connection.Connection, requestMsg *pb.RequestMessage) {
	rsp, err := HandleSignupMessage(requestMsg)
	logger.Sugar().Infof("注册响应: %s", rsp.String())

	if err != nil {
		logger.Sugar().Errorf("注册出现错误：: %v", err)
	}

	h.sendResponse(conn, rsp)
}

// writeToClient 监听 channel 发送消息协程
func (h *WebSocketHandler) writeToClient(conn *connection.Connection) {
	sugar := logger.Sugar()
	ticker := time.NewTicker(h.config.pingInterval)
	defer func() {
		ticker.Stop()
		sugar.Debugf("连接关闭，写协程退出")
	}()

	for {
		select {
		case <-conn.Done():
			return
		case msg, ok := <-conn.SendChan:
			if !ok {
				return
			}
			_ = conn.Conn.SetWriteDeadline(time.Now().Add(h.config.writeTimeout))
			if err := conn.Conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				sugar.Errorln("发送消息错误: ", err)
				conn.Close()
				return
			}
		case <-ticker.C:
			_ = conn.Conn.SetWriteDeadline(time.Now().Add(h.config.writeTimeout))
			if err := conn.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				sugar.Debugf("WebSocket ping失败，关闭连接: connection_id=%s error=%v", conn.ID, err)
				conn.Close()
				return
			}
		}
	}
}

// sendResponse 发送响应消息
func (h *WebSocketHandler) sendResponse(conn *connection.Connection, rsp *pb.ResponseMessage) {
	rspBytes, _ := proto.Marshal(rsp)
	if err := conn.EnqueueMessage(rspBytes); err != nil {
		logger.Sugar().Errorf("发送响应失败: %v", err)
	}
}

// sendRefusedResponse 发送拒绝响应
func (h *WebSocketHandler) sendRefusedResponse(conn *connection.Connection) {
	rsp := &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Refused{},
	}
	h.sendResponse(conn, rsp)
}

// SendMessage 外部发送消息接口
func (h *WebSocketHandler) SendMessage(userID string, message []byte) error {
	return h.router.RouteMessage(userID, message)
}

// StopClient 外部关闭特定连接
func (h *WebSocketHandler) StopClient(userID string) {
	// 强制登出用户
	h.sessionManager.ForceLogout(userID, h.connManager)
}

func (h *WebSocketHandler) StopClientIfOwner(userID, ownerToken string) {
	h.connManager.StopUserIfOwner(userID, ownerToken)
}

// GetConnectionStats 获取连接统计信息
func (h *WebSocketHandler) GetConnectionStats() (int, int) {
	return h.connManager.GetConnectionCount(), h.connManager.GetLoggedInUserCount()
}

// GetWebSocketHandler 获取全局WebSocket处理器实例
var globalWebSocketHandler *WebSocketHandler

func GetWebSocketHandler() *WebSocketHandler {
	if globalWebSocketHandler != nil && globalWebSocketHandler.connManager != nil {
		logger.Sugar().Debugf("GetWebSocketHandler返回，连接管理器: %s", globalWebSocketHandler.connManager.GetInstanceID())
	} else {
		logger.Sugar().Warnf("GetWebSocketHandler返回nil或connManager为nil")
	}
	return globalWebSocketHandler
}

// SetGlobalWebSocketHandler 设置全局WebSocket处理器实例
func SetGlobalWebSocketHandler(handler *WebSocketHandler) {
	globalWebSocketHandler = handler
}

// subscribeKickNotifications 订阅实时踢出通知
func (h *WebSocketHandler) subscribeKickNotifications() {
	// 获取容器标识符（使用HOSTNAME作为唯一标识）
	containerID := os.Getenv("HOSTNAME")
	if containerID == "" {
		containerID = "local"
	}

	dsm := &redisClient.DistributedSessionManager{}
	err := dsm.SubscribeKickNotifications(containerID, func(userID, ownerToken string) {
		logger.Sugar().Infof("收到实时踢出通知，执行所有权校验: 用户 %s", userID)
		h.connManager.StopUserIfOwner(userID, ownerToken)
	})

	if err != nil {
		logger.Sugar().Errorf("订阅实时踢出通知失败: %v", err)
	}
}
