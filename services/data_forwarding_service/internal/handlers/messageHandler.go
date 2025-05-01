package handlers

import (
	pb "Betterfly2/proto/idl"
	"Betterfly2/shared/logger"
	"google.golang.org/protobuf/proto"
	"strconv"
)

func RequestMessageHandler(message *pb.RequestMessage) error {
	sugar := logger.Sugar()
	var err error = nil
	switch payload := message.Payload.(type) {
	case *pb.RequestMessage_Login:
		sugar.Infof("收到 Login 消息: %+v", payload.Login)
	case *pb.RequestMessage_Logout:
		sugar.Infof("收到 Logout 消息: %+v", payload.Logout)
	case *pb.RequestMessage_Signup:
		sugar.Infof("收到 Signup 消息: %+v", payload.Signup)
	case *pb.RequestMessage_Post:
		sugar.Infof("收到 Post 消息: %+v", payload.Post)
		fromId := strconv.FormatInt(payload.Post.GetFromId(), 10)
		toId := strconv.FormatInt(payload.Post.GetToId(), 10)
		msg := fromId + ": " + payload.Post.GetMsg()
		err = SendMessage(toId, msg)
		if err == nil {
			sugar.Infof("%s 成功向 %s 发送消息", fromId, toId)
		}
	case *pb.RequestMessage_QueryUser:
		sugar.Infof("收到 QueryUser 消息: %+v", payload.QueryUser)
	case *pb.RequestMessage_InsertContact:
		sugar.Infof("收到 InsertContact 消息: %+v", payload.InsertContact)
	case *pb.RequestMessage_QueryGroup:
		sugar.Infof("收到 QueryGroup 消息: %+v", payload.QueryGroup)
	case *pb.RequestMessage_InsertGroup:
		sugar.Infof("收到 InsertGroup 消息: %+v", payload.InsertGroup)
	case *pb.RequestMessage_InsertGroupUser:
		sugar.Infof("收到 InsertGroupUser 消息: %+v", payload.InsertGroupUser)
	case *pb.RequestMessage_FileRequest:
		sugar.Infof("收到 FileRequest 消息: %+v", payload.FileRequest)
	case *pb.RequestMessage_UpdateAvatar:
		sugar.Infof("收到 UpdateAvatar 消息: %+v", payload.UpdateAvatar)
	default:
		sugar.Warnf("收到未知Payload: %+v", payload)
	}
	return err
}

func HandleRequestData(data []byte) (*pb.RequestMessage, error) {
	req := &pb.RequestMessage{}
	err := proto.Unmarshal(data, req)
	if err != nil {
		// 反序列化失败了，说明数据不是有效的pb数据
		sugar := logger.Sugar()
		sugar.Errorf("反序列化失败: %v", err)
		return nil, err
	}

	return req, nil
}
