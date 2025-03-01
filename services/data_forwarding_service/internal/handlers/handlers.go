package handlers

import (
	"common/logger"
	"data_forwarding_service/internal/publisher"
	"github.com/gorilla/websocket"
	"net/http"
)

var log = logger.NewLogger()

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func StartWebSocketServer() error {
	http.HandleFunc("/ws", handleConnection)
	return http.ListenAndServe(":54342", nil)
}

func handleConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error.Println(err)
		return
	}
	defer conn.Close()
	log.Info.Printf("已与 %v 建立连接\n", conn.RemoteAddr())

	for {
		// 处理消息接收与转发
		_, p, err := conn.ReadMessage()
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
		log.Info.Println("收到WebSocket消息: ", string(p))
	}
}

// 调用消息队列发布接口完成消息发布
func publishMessage(message []byte) error {
	return publisher.PublishMessage(string(message))
}
