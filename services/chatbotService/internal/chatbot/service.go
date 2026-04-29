package chatbot

import (
	"Betterfly2/shared/db"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Audit(principal BotPrincipal, action, targetType, targetID string, err error) {
	record := AuditRecord{
		BotID:      principal.BotID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Status:     AuditStatusOK,
	}
	if err != nil {
		record.Status = AuditStatusError
		record.Error = err.Error()
	}
	_ = s.store.Audit(record)
}

func (s *Service) GetUser(userID int64) (UserInfo, error) {
	if userID <= 0 {
		return UserInfo{}, errors.New("user_id is required")
	}
	return s.store.GetUser(userID)
}

func (s *Service) GetGroup(groupID int64) (GroupInfo, error) {
	if groupID <= 0 {
		return GroupInfo{}, errors.New("group_id is required")
	}
	return s.store.GetGroup(groupID)
}

func (s *Service) GetGroupMembers(groupID int64) ([]db.GroupMemberContact, error) {
	if groupID <= 0 {
		return nil, errors.New("group_id is required")
	}
	return s.store.GetGroupMembers(groupID)
}

func (s *Service) GetRecentMessages(conversationType string, conversationID int64, requesterUserID int64, limit int) ([]MessageInfo, error) {
	conversationType = strings.TrimSpace(conversationType)
	if conversationID <= 0 {
		return nil, errors.New("conversation id is required")
	}
	switch conversationType {
	case "direct":
		if requesterUserID <= 0 {
			return nil, errors.New("user_id is required for direct conversation")
		}
		return s.store.GetRecentDirectMessages(requesterUserID, conversationID, limit)
	case "group":
		return s.store.GetRecentGroupMessages(conversationID, limit)
	default:
		return nil, fmt.Errorf("unsupported conversation type: %s", conversationType)
	}
}

func (s *Service) SendMessage(_ SendMessageRequest) error {
	return notImplemented("send_message")
}

func (s *Service) HandleOpenClawWebhook(req OpenClawWebhookRequest) (OpenClawWebhookResponse, error) {
	if req.Payload == nil {
		req.Payload = map[string]interface{}{}
	}
	return OpenClawWebhookResponse{
		Accepted: true,
		Message:  "OpenClaw webhook accepted; action execution will be wired in the next phase",
	}, nil
}

func TargetID(id int64) string {
	return strconv.FormatInt(id, 10)
}
