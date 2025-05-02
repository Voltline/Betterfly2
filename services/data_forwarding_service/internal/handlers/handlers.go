package handlers

import (
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/publisher"
	"data_forwarding_service/internal/redis"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// Client 连接管理
type Client struct {
	conn       *websocket.Conn
	sendChan   chan []byte
	shouldStop bool // 当shouldStop为true时，读、写协程立刻退出工作
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
	sugar := logger.Sugar()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		sugar.Errorf("连接错误: %s", err)
		return
	}

	// 提取 userID，如果没有就用 IP 地址代替
	userID := r.URL.Query().Get("user")
	if userID == "" {
		userID = conn.RemoteAddr().String()
	}

	client := &Client{
		conn:       conn,
		sendChan:   make(chan []byte, 256),
		shouldStop: false,
	}

	// 统一处理本地冲突、Redis 注册、远程通知
	if err := checkAndResolveConflict(userID, client); err != nil {
		sugar.Errorf("连接初始化失败: %v", err)
		conn.Close()
		return
	}

	sugar.Infof("已与 %v 建立连接", conn.RemoteAddr())
	sugar.Infof("收到的Request内容为: %v", *r)

	// 启动两个 goroutine
	go readProcess(client, userID)
	go writeToClient(client, userID)
}

// 读取处理协程
func readProcess(client *Client, userID string) {
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
		redisClient.UnregisterConnection(userID, containerID)

		sugar.Infof("(%v, %v)连接已关闭", userID, client.conn.RemoteAddr())
	}()

	for {
		// 处理消息接收与转发
		_, p, err := client.conn.ReadMessage()

		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				sugar.Infof("连接关闭，读协程退出")
			} else {
				sugar.Errorln("获取信息异常: ", err)
			}
			close(client.sendChan)
			break
		}
		if len(p) == 0 {
			// log.Warn.Println("接受到的消息为空，已跳过")
			continue
		}

		requestMsg, err := HandleRequestData(p)
		if err != nil {
			sugar.Warnf("收到非标准化数据: %v", err)
			continue
		}

		post := requestMsg.GetPost()
		if post == nil {
			continue
		}

		var targetTopic string
		targetTopic = redisClient.GetContainerByConnection(strconv.FormatInt(post.GetToId(), 10))
		if targetTopic == "" {
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
	sugar := logger.Sugar()
	defer func() {
		sugar.Infof("连接关闭，写协程退出")
	}()
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

// StopClient 外部关闭特定连接
func StopClient(userID string) {
	clientsMutex.Lock()
	client, ok := clients[userID]
	clientsMutex.Unlock()
	if !ok {
		return
	}
	client.conn.Close()
	client.shouldStop = true
}

// checkAndResolveConflict 检验并解决连接冲突
func checkAndResolveConflict(userID string, client *Client) error {
	sugar := logger.Sugar()

	containerID := os.Getenv("HOSTNAME")
	if containerID == "" {
		containerID = "message-topic"
	}

	// 第一步：清理本地已有连接
	clientsMutex.Lock()
	if oldClient, ok := clients[userID]; ok {
		sugar.Infof("已有本地连接，关闭旧连接: %v", userID)
		oldClient.conn.Close()
		delete(clients, userID)
		if err := redisClient.UnregisterConnection(userID, containerID); err != nil {
			sugar.Warnf("本地Redis注销失败（忽略继续）: %v", err)
		}
	}
	clientsMutex.Unlock()

	// 第二步：检测是否远程已注册
	remoteContainer := redisClient.GetContainerByConnection(userID)
	sugar.Infof("远程容器: %v", remoteContainer)

	if remoteContainer != "" && remoteContainer != containerID {
		sugar.Infof("用户 %s 存在于其他容器 %s", userID, remoteContainer)

		// 注销旧连接
		if err := redisClient.UnregisterConnection(userID, remoteContainer); err != nil {
			return fmt.Errorf("注销 Redis 失败: %w", err)
		}

		// 通知旧容器断开连接
		if err := publishMessage([]byte(fmt.Sprintf("DELETE USER %s", userID)), remoteContainer); err != nil {
			return fmt.Errorf("通知远程容器失败: %w", err)
		}
	}

	// 第三步：注册本连接
	if err := redisClient.RegisterConnection(userID, containerID); err != nil {
		return fmt.Errorf("注册 Redis 失败: %w", err)
	}

	// 第四步：保存本地连接
	clientsMutex.Lock()
	clients[userID] = client
	clientsMutex.Unlock()

	sugar.Infof("连接 %s 注册并保存成功", userID)
	return nil
}
