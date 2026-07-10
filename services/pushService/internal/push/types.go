package push

import (
	"context"
	"errors"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
)

const PushTypeVoIP = "voip"

var (
	ErrInvalidRequest  = errors.New("invalid push request")
	ErrTokenNotFound   = errors.New("push token not found")
	ErrAPNSUnavailable = errors.New("APNs sender unavailable")
)

type Notification struct {
	Token        string
	Environment  pushpb.PushEnvironment
	CallID       string
	CallerUserID int64
	CalleeUserID int64
	CallType     string
	ExpiresAt    time.Time
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
	RegisterVoIPToken(context.Context, int64, string, string, string, string) error
	UnregisterVoIPToken(context.Context, int64, string, string) (bool, error)
	ListActiveVoIPTokens(context.Context, int64) ([]db.PushDeviceToken, error)
	DeactivateToken(context.Context, int64) error
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
