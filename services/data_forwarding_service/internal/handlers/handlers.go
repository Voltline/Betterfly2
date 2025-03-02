package handlers

import (
	"data_forwarding_service/internal/logger"
	"data_forwarding_service/internal/publisher"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
)

var log = logger.NewLogger()

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
	return http.ListenAndServe(":54342", nil)
}

// 请求处理
func handleConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error.Println(err)
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
	log.Info.Printf("保存连接 %v : %v\n", userID, *client)
	clients[userID] = client
	clientsMutex.Unlock()

	log.Info.Printf("已与 %v 建立连接\n", conn.RemoteAddr())
	log.Info.Printf("收到的Request内容为: %v\n", *r)

	// 启动读取处理和发送消息两个协程
	// 读取处理协程
	go readProcess(client, userID)
	// 监听 channel 发送消息协程
	go writeToClient(client, userID)
}

// 读取处理协程
func readProcess(client *Client, userID string) {
	defer func() {
		clientsMutex.Lock()
		delete(clients, userID)
		clientsMutex.Unlock()
		client.conn.Close()
		log.Info.Printf("(%v, %v)连接已关闭\n", userID, client.conn.RemoteAddr())
	}()

	for {
		// 处理消息接收与转发
		_, p, err := client.conn.ReadMessage()
		if err != nil {
			log.Error.Println("获取信息异常: ", err)
			break
		}
		if len(p) == 0 {
			// log.Warn.Println("接受到的消息为空，已跳过")
			continue
		}
		err = publishMessage(p) // 将消息转发到消息队列
		if err != nil {
			log.Error.Println("转发信息到消息队列异常: ", err)
			break
		}
		// TODO: DEBUG模式
		log.Info.Println("收到WebSocket消息:", string(p))
	}
}

// 监听 channel 发送消息协程
func writeToClient(client *Client, userID string) {
	defer client.conn.Close()
	for msg := range client.sendChan {
		err := client.conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			log.Error.Printf("发送消息错误: %v\n", err)
		}
	}
}

// 调用消息队列发布接口完成消息发布
func publishMessage(message []byte) error {
	return publisher.PublishMessage(string(message))
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
