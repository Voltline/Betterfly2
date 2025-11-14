package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/connection"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/router"
	"data_forwarding_service/internal/session"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WebSocketHandler 处理WebSocket连接和消息
type WebSocketHandler struct {
	connManager    *connection.ConnectionManager
	sessionManager *session.SessionManager
	router         *router.Router
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
	}

	// 订阅实时踢出通知
	handler.subscribeKickNotifications()

	logger.Sugar().Debugf("WebSocketHandler创建完成: %s", connManager.GetInstanceID())
	return handler
}

// StartWebSocketServer 启动WebSocket服务器
func (h *WebSocketHandler) StartWebSocketServer() error {
	http.HandleFunc("/ws", h.handleConnection)
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

	return http.ListenAndServeTLS(":"+port, certFile, keyFile, nil)
}

// handleConnection 处理WebSocket连接
func (h *WebSocketHandler) handleConnection(w http.ResponseWriter, r *http.Request) {
	sugar := logger.Sugar()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		sugar.Errorf("连接错误: %s", err)
		return
	}

	// 创建新连接
	connection := h.connManager.AddConnection(conn)

	sugar.Debugf("已与 %v 建立连接", conn.RemoteAddr())

	// 启动读写协程
	go h.readProcess(connection)
	go h.writeToClient(connection)
}

// readProcess 读取处理协程
func (h *WebSocketHandler) readProcess(conn *connection.Connection) {
	sugar := logger.Sugar()
	defer func() {
		// 连接关闭时清理
		h.connManager.RemoveConnection(conn.ID)
		if conn.LoggedIn && conn.UserID != "" {
			h.sessionManager.RemoveSession(conn.UserID)
		}
		sugar.Debugf("(%v, %v)连接已关闭", conn.ID, conn.Conn.RemoteAddr())
	}()

	for {
		// 处理消息接收
		_, p, err := conn.Conn.ReadMessage()

		if err != nil {
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
		if !conn.LoggedIn {
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
	logger.Sugar().Infof("登录响应: %s", rsp.String())

	if err != nil {
		logger.Sugar().Errorf("登录出现错误: %v", err)
		h.sendResponse(conn, rsp)
		return
	}

	// 登录成功，绑定用户ID
	userIDStr := strconv.FormatInt(realUserID, 10)
	err = h.connManager.Login(conn.ID, userIDStr)
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
	err = h.sessionManager.CreateSession(userIDStr, conn.ID)
	if err != nil {
		logger.Sugar().Errorf("创建会话失败: %v", err)
	}

	// 返回登录结果
	h.sendResponse(conn, rsp)
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
	defer func() {
		sugar.Debugf("连接关闭，写协程退出")
	}()

	for msg := range conn.SendChan {
		err := conn.Conn.WriteMessage(websocket.BinaryMessage, msg)
		if err != nil {
			sugar.Errorln("发送消息错误: ", err)
		}
	}
}

// sendResponse 发送响应消息
func (h *WebSocketHandler) sendResponse(conn *connection.Connection, rsp *pb.ResponseMessage) {
	rspBytes, _ := proto.Marshal(rsp)
	select {
	case conn.SendChan <- rspBytes:
		// 发送成功
	default:
		logger.Sugar().Errorf("发送通道已满，消息丢失: %s", conn.ID)
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
	err := dsm.SubscribeKickNotifications(containerID, func(userID string) {
		logger.Sugar().Infof("收到实时踢出通知，执行强制登出: 用户 %s", userID)
		h.StopClient(userID)
	})

	if err != nil {
		logger.Sugar().Errorf("订阅实时踢出通知失败: %v", err)
	}
}
