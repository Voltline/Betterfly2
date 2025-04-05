package handlers

import (
	"data_forwarding_service/internal/logger_config"
	"data_forwarding_service/internal/publisher"
	"data_forwarding_service/internal/redis_client"
	"fmt"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"net/http"
	"os"
	"sync"
)

// Client 连接管理
type Client struct {
	conn     *websocket.Conn
	sendChan chan []byte
}

// 用于存储 WebSocket 连接的map
var (
	clients      = make(map[string]*Client) // {(用户ID: 客户端)
	clientsMutex sync.Mutex                 // 互斥锁
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// StartWebSocketServer 启动WebSocket服务器
func StartWebSocketServer() error {
	http.HandleFunc("/ws", handleConnection)
	port := os.Getenv("PORT")
	if port == "" {
		port = "54342"
	}
	return http.ListenAndServe(":"+port, nil)
}

// 请求处理
func handleConnection(w http.ResponseWriter, r *http.Request) {
	logger := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer logger.Sync()
	sugar := logger.Sugar()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		sugar.Errorf("连接错误: %s", err)
		return
	}

	// 建立连接时即从HTTP报文中获取user字段
	userID := r.URL.Query().Get("user")
	if userID == "" {
		userID = conn.RemoteAddr().String() // 没有则以IP替代ID
	}

	client := &Client{
		conn:     conn,
		sendChan: make(chan []byte, 256), // 待发送消息队列
	}

	// 存储连接
	clientsMutex.Lock()
	sugar.Infof("保存连接 %v, %v", userID, *client)
	clients[userID] = client
	clientsMutex.Unlock()

	// 从容器内读取HOSTNAME，若没有则默认为default-container
	containerID := os.Getenv("HOSTNAME")
	if containerID == "" {
		containerID = "message-topic"
	}

	err = redis_client.RegisterConnection(userID, containerID)
	if err != nil {
		sugar.Errorf("保存连接错误: %s", err)
		// 如果无法正确保存连接，应该删除连接对象
		clientsMutex.Lock()
		delete(clients, userID)
		clientsMutex.Unlock()
		return
	}

	sugar.Infof("已与 %v 建立连接", conn.RemoteAddr())
	sugar.Infof("收到的Request内容为: %v", *r)

	// 启动读取处理和发送消息两个协程
	// 读取处理协程
	go readProcess(client, userID)
	// 监听 channel 发送消息协程
	go writeToClient(client, userID)
}

// 读取处理协程
func readProcess(client *Client, userID string) {
	logger := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer logger.Sync()
	sugar := logger.Sugar()
	defer func() {
		clientsMutex.Lock()
		delete(clients, userID)
		clientsMutex.Unlock()
		client.conn.Close()

		containerID := os.Getenv("HOSTNAME")
		if containerID == "" {
			containerID = "message-topic"
		}
		redis_client.UnregisterConnection(userID, containerID)

		sugar.Infof("(%v, %v)连接已关闭", userID, client.conn.RemoteAddr())
	}()

	for {
		// 处理消息接收与转发
		_, p, err := client.conn.ReadMessage()
		if err != nil {
			sugar.Errorln("获取信息异常: ", err)
			break
		}
		if len(p) == 0 {
			// log.Warn.Println("接受到的消息为空，已跳过")
			continue
		}

		var targetTopic string
		targetTopic, err = redis_client.GetContainerByConnection(string(p))
		if err != nil {
			sugar.Warnf("%s 用户不在线", string(p))
			continue
		}
		err = publishMessage(p, targetTopic) // 将消息转发到消息队列
		if err != nil {
			sugar.Errorln("转发信息到消息队列异常: ", err)
			break
		}
		// TODO: DEBUG模式
		sugar.Infoln("收到WebSocket消息:", string(p))
	}
}

// 监听 channel 发送消息协程
func writeToClient(client *Client, userID string) {
	logger := zap.New(logger_config.CoreConfig, zap.AddCaller())
	defer logger.Sync()
	sugar := logger.Sugar()
	defer client.conn.Close()
	for msg := range client.sendChan {
		err := client.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			sugar.Errorln("发送消息错误: ", err)
		}
	}
}

// 调用消息队列发布接口完成消息发布
func publishMessage(message []byte, targetTopic string) error {
	return publisher.PublishMessage(string(message), targetTopic)
}

// SendMessage 外部发送消息接口
func SendMessage(userID string, message string) error {
	clientsMutex.Lock()
	client, ok := clients[userID]
	clientsMutex.Unlock()
	if !ok {
		return fmt.Errorf("客户端%v不存在", userID)
	}

	// 通过 channel 发送消息
	client.sendChan <- []byte(message)
	return nil
}
