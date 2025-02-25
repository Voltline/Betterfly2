package Utils

import (
	"encoding/json"
	"fmt"
	"time"
)

type RequestType int

const (
	Login RequestType = iota
	Exit
	Post
	Key
	QueryUser
	InsertContact
	QueryGroup
	InsertGroup
	InsertGroupUser
	ReqFile
	APNsToken
	UpdateAvatar
)

type ResponseType int

const (
	Refused ResponseType = iota
	Server
	PostPlaceHolder
	ResFile
	Warn
	PubKey
	UserInfo
	GroupInfo
)

type RequestMessage struct {
	// General
	packetJson json.RawMessage
	Type       RequestType `json:"type"`
	FromID     int         `json:"from"`
	ToID       int         `json:"to"`
	Timestamp  DateTime    `json:"timestamp"`
	Msg        string      `json:"msg"`
	IsGroup    bool        `json:"is_group"`
	Name       string      `json:"name"`
	// Not General
	UserApnToken  string `json:"user_apn_token"`
	FileHash      string `json:"file_hash"`
	FileSuffix    string `json:"file_suffix"`
	FileOperation string `json:"operation"`
	ApnsToken     string `json:"apns_token"`
}

func NewRequestMessage(packet string) (*RequestMessage, error) {
	packetJson := json.RawMessage([]byte(packet))
	requestMessage := &RequestMessage{packetJson: packetJson}
	if err := json.Unmarshal([]byte(packet), requestMessage); err != nil {
		return nil, err
	}
	return requestMessage, nil
}

func (msg *RequestMessage) ToJsonString() string {
	return string(msg.packetJson)
}

func (msg *RequestMessage) ToJsonEncodedBytes() []byte {
	return Encode(msg.ToJsonString())
}

func (msg *RequestMessage) String() string {
	return fmt.Sprintf("packet_json: %v", string(msg.packetJson))
}

func MakeServerMessage(msg string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":      Server,
		"from":      -1,
		"msg":       msg,
		"from_name": "",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeRefusedMessage(msg string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":      Refused,
		"from":      -1,
		"msg":       msg,
		"from_name": "",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeUploadMessage(fileFullName, content string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":    ResFile,
		"from":    0,
		"msg":     fileFullName,
		"content": content,
		"file_op": "upload",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeDownloadMessage(fileFullName, content string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":    ResFile,
		"from":    0,
		"msg":     fileFullName,
		"content": content,
		"file_op": "download",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeWarnMessage(msg string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":      Server,
		"from":      -1,
		"msg":       msg,
		"from_name": "",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeUserInfoMessage(userId int, userInfo string) (string, error) {
	jsonDict := map[string]interface{}{
		"type":      UserInfo,
		"from":      0,
		"msg":       userInfo,
		"from_name": "",
		"to":        userId,
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeGroupInfoMessage(groupId int, groupInfo string, duringAdd bool) (string, error) {
	from_id := 0
	if duringAdd {
		from_id = -1
	}
	jsonDict := map[string]interface{}{
		"type":      GroupInfo,
		"from":      from_id,
		"msg":       groupInfo,
		"from_name": "",
		"to":        groupId,
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}

func MakeHelloMessage(FromUserID, ToID int, FromUserName string, isGroup bool, msg string) (string, error) {
	if msg == "" {
		msg = "Hello"
	}
	jsonDict := map[string]interface{}{
		"type":      Post,
		"from":      FromUserID,
		"msg":       msg,
		"from_name": FromUserName,
		"to":        ToID,
		"is_group":  isGroup,
		"timestamp": DateTime{time.Now()},
		"msg_type":  "text",
	}
	rawJson, err := json.Marshal(jsonDict)
	if err != nil {
		return "", err
	}
	return string(rawJson), nil
}
