package chatbot

import "Betterfly2/shared/db"

const (
	ScopeReadUser        = "read:user"
	ScopeReadGroup       = "read:group"
	ScopeReadMessages    = "read:messages"
	ScopeSendMessage     = "send:message"
	ScopeOpenClawWebhook = "openclaw:webhook"

	AuditStatusOK    = "ok"
	AuditStatusError = "error"
)

type BotPrincipal struct {
	BotID  string
	Scopes map[string]bool
}

func (p BotPrincipal) HasScope(scope string) bool {
	return p.Scopes[scope] || p.Scopes["*"]
}

type UserInfo struct {
	UserID     int64  `json:"user_id"`
	Account    string `json:"account"`
	Name       string `json:"name"`
	Avatar     string `json:"avatar"`
	UpdateTime string `json:"update_time"`
}

type GroupInfo struct {
	GroupID     int64  `json:"group_id"`
	Name        string `json:"name"`
	Avatar      string `json:"avatar"`
	OwnerUserID int64  `json:"owner_user_id"`
	UpdateTime  string `json:"update_time"`
}

type MessageInfo struct {
	MessageID    int64  `json:"message_id"`
	FromUserID   int64  `json:"from_user_id"`
	ToUserID     int64  `json:"to_user_id"`
	Content      string `json:"content"`
	Timestamp    string `json:"timestamp"`
	MessageType  string `json:"message_type"`
	RealFileName string `json:"real_file_name,omitempty"`
	IsGroup      bool   `json:"is_group"`
}

type SendMessageRequest struct {
	FromUserID   int64  `json:"from_user_id"`
	ToUserID     int64  `json:"to_user_id"`
	GroupID      int64  `json:"group_id,omitempty"`
	Content      string `json:"content"`
	MessageType  string `json:"message_type,omitempty"`
	RealFileName string `json:"real_file_name,omitempty"`
}

type OpenClawWebhookRequest struct {
	EventID string                 `json:"event_id,omitempty"`
	Type    string                 `json:"type,omitempty"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

type OpenClawWebhookResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
}

type AuditRecord struct {
	BotID      string
	Action     string
	TargetType string
	TargetID   string
	Status     string
	Error      string
}

func userFromModel(user *db.User) UserInfo {
	return UserInfo{
		UserID:     user.ID,
		Account:    user.Account,
		Name:       user.Name,
		Avatar:     user.Avatar,
		UpdateTime: user.UpdateTime,
	}
}

func groupFromModel(group *db.Group) GroupInfo {
	return GroupInfo{
		GroupID:     group.GroupID,
		Name:        group.Name,
		Avatar:      group.Avatar,
		OwnerUserID: group.OwnerUserID,
		UpdateTime:  group.UpdateTime,
	}
}

func messageFromModel(message db.Message) MessageInfo {
	return MessageInfo{
		MessageID:    message.MessageID,
		FromUserID:   message.FromUserID,
		ToUserID:     message.ToUserID,
		Content:      message.Content,
		Timestamp:    message.Timestamp,
		MessageType:  message.MessageType,
		RealFileName: message.RealFileName,
		IsGroup:      message.IsGroup,
	}
}
