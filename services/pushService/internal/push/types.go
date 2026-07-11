package push

import (
	"context"
	"errors"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
)

const (
	PushTypeVoIP = "voip"
	PushTypeAPNs = "apns"
)

type NotificationKind string

const (
	NotificationVoIP    NotificationKind = "voip"
	NotificationMessage NotificationKind = "message"
)

var (
	ErrInvalidRequest  = errors.New("invalid push request")
	ErrTokenNotFound   = errors.New("push token not found")
	ErrAPNSUnavailable = errors.New("APNs sender unavailable")
)

type Notification struct {
	Kind               NotificationKind
	Token              string
	Environment        pushpb.PushEnvironment
	CallID             string
	CallerUserID       int64
	CalleeUserID       int64
	CallType           string
	ExpiresAt          time.Time
	SenderUserID       int64
	TargetUserID       int64
	ConversationID     int64
	IsGroup            bool
	MessageType        string
	SentAt             time.Time
	Title              string
	Body               string
	CustomData         map[string]any
	SenderName         string
	SenderAvatar       string
	GroupName          string
	Avatar             string
	AvatarIsGroup      bool
	ConversationName   string
	ConversationAvatar string
}

type SendResult struct {
	APNSID string
}

type APNSError struct {
	StatusCode int
	Reason     string
	APNSID     string
}

func (e *APNSError) Error() string {
	if e.Reason == "" {
		return "APNs request failed"
	}
	return "APNs request failed: " + e.Reason
}

func (e *APNSError) InvalidatesToken() bool {
	return e.StatusCode == 410 || e.Reason == "BadDeviceToken" || e.Reason == "DeviceTokenNotForTopic" || e.Reason == "Unregistered"
}

func (e *APNSError) Retryable() bool {
	return e.StatusCode == 429 || e.StatusCode == 500 || e.StatusCode == 503
}

type Store interface {
	Ping(context.Context) error
	RegisterToken(context.Context, int64, string, string, string, string, string) error
	UnregisterToken(context.Context, int64, string, string, string) (bool, error)
	ListActiveTokens(context.Context, int64, string) ([]db.PushDeviceToken, error)
	MessageNotificationsEnabled(context.Context, int64, int64, bool) (bool, error)
	MessagePresentation(context.Context, int64, int64, bool) (MessagePresentation, error)
	FindTokens(context.Context, TokenFilter) ([]db.PushDeviceToken, error)
	GetToken(context.Context, int64) (db.PushDeviceToken, error)
	CreateDebugAudit(context.Context, *db.PushDebugAudit) error
	ListDebugAudits(context.Context, int) ([]db.PushDebugAudit, error)
	TokenSummary(context.Context) (TokenSummary, error)
	DeactivateToken(context.Context, int64) error
}

type TokenFilter struct {
	UserID      int64
	DeviceID    string
	Environment string
	PushType    string
	ActiveOnly  bool
	Limit       int
}

type TokenSummary struct {
	Total      int64 `json:"total"`
	Active     int64 `json:"active"`
	APNs       int64 `json:"apns"`
	VoIP       int64 `json:"voip"`
	Sandbox    int64 `json:"sandbox"`
	Production int64 `json:"production"`
}

type MessagePresentation struct {
	Title              string
	SenderName         string
	SenderAvatar       string
	GroupName          string
	Avatar             string
	AvatarIsGroup      bool
	ConversationName   string
	ConversationAvatar string
}

type Sender interface {
	Ready() error
	Send(context.Context, Notification) (SendResult, error)
}

type Publisher interface {
	Publish(context.Context, string, *pushpb.ResponseMessage) error
}

type UnavailableSender struct {
	Err error
}

func (s UnavailableSender) Ready() error {
	if s.Err != nil {
		return s.Err
	}
	return ErrAPNSUnavailable
}

func (s UnavailableSender) Send(context.Context, Notification) (SendResult, error) {
	return SendResult{}, s.Ready()
}
