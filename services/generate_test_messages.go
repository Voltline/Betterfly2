package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	pb "Betterfly2/proto/data_forwarding"
	"google.golang.org/protobuf/proto"
)

func main() {
	msgType := flag.String("type", "all", "Message type: login, signup, logout, post, query_message, query_sync_messages, all")
	flag.Parse()

	switch *msgType {
	case "login":
		generateLogin()
	case "signup":
		generateSignup()
	case "logout":
		generateLogout()
	case "post":
		generatePost()
	case "query_message":
		generateQueryMessage()
	case "query_sync_messages":
		generateQuerySyncMessages()
	case "all":
		generateAll()
	default:
		log.Fatalf("Unknown message type: %s", *msgType)
	}
}

func generateLogin() {
	// 生成登录请求
	loginReq := &pb.LoginReq{
		Account:  "testuser",
		Password: "testpassword",
	}

	reqMsg := &pb.RequestMessage{
		Jwt: "",
		Payload: &pb.RequestMessage_Login{
			Login: loginReq,
		},
	}

	encodeAndPrint("登录请求", reqMsg)
}

func generateSignup() {
	// 生成注册请求
	signupReq := &pb.SignupReq{
		Account:  "newuser",
		Password: "newpassword",
		UserName: "New User",
	}

	reqMsg := &pb.RequestMessage{
		Jwt: "",
		Payload: &pb.RequestMessage_Signup{
			Signup: signupReq,
		},
	}

	encodeAndPrint("注册请求", reqMsg)
}

func generateLogout() {
	// 生成登出请求
	logoutReq := &pb.LogoutReq{}

	reqMsg := &pb.RequestMessage{
		Jwt: "example.jwt.token.here",
		Payload: &pb.RequestMessage_Logout{
			Logout: logoutReq,
		},
	}

	encodeAndPrint("登出请求", reqMsg)
}

func generatePost() {
	// 生成发送消息请求
	post := &pb.Post{
		FromId:  1001,
		ToId:    1002,
		Msg:     "Hello, this is a test message!",
		MsgType: "text",
		IsGroup: false,
	}

	reqMsg := &pb.RequestMessage{
		Jwt: "example.jwt.token.here",
		Payload: &pb.RequestMessage_Post{
			Post: post,
		},
	}

	encodeAndPrint("发送消息请求", reqMsg)
}

func generateQueryMessage() {
	// 生成查询单条消息请求
	queryMsg := &pb.QueryMessage{
		MessageId: 12345,
	}

	reqMsg := &pb.RequestMessage{
		Jwt: "example.jwt.token.here",
		Payload: &pb.RequestMessage_QueryMessage{
			QueryMessage: queryMsg,
		},
	}

	encodeAndPrint("查询单条消息请求", reqMsg)
}

func generateQuerySyncMessages() {
	// 生成同步消息查询请求
	querySync := &pb.QuerySyncMessages{
		ToUserId:  1002,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	reqMsg := &pb.RequestMessage{
		Jwt: "example.jwt.token.here",
		Payload: &pb.RequestMessage_QuerySyncMessages{
			QuerySyncMessages: querySync,
		},
	}

	encodeAndPrint("同步消息查询请求", reqMsg)
}

func generateAll() {
	fmt.Println("生成所有类型的测试消息:")
	fmt.Println(strings.Repeat("=", 50))

	generateLogin()
	fmt.Println()
	generateSignup()
	fmt.Println()
	generateLogout()
	fmt.Println()
	generatePost()
	fmt.Println()
	generateQueryMessage()
	fmt.Println()
	generateQuerySyncMessages()
}

func encodeAndPrint(label string, reqMsg *pb.RequestMessage) {
	// 序列化
	data, err := proto.Marshal(reqMsg)
	if err != nil {
		log.Fatalf("序列化失败: %v", err)
	}

	// base64编码
	encoded := base64.StdEncoding.EncodeToString(data)

	fmt.Printf("%s:\n", label)
	fmt.Printf("Base64: %s\n", encoded)
	fmt.Printf("Length: %d bytes\n", len(data))
	fmt.Printf("Hex: %x\n", data)
}
