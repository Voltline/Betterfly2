package call

import (
	"context"
	"errors"
	"time"

	callpb "Betterfly2/proto/call"
)

const (
	StateRinging = "ringing"
	StateActive  = "active"
	StateEnded   = "ended"
)

var (
	ErrUserOffline  = errors.New("user offline")
	ErrUserBusy     = errors.New("user busy")
	ErrCallNotFound = errors.New("call not found")
	ErrInvalidState = errors.New("invalid call state")
	ErrForbidden    = errors.New("call operation forbidden")
	ErrInvalidInput = errors.New("invalid call request")
)

type Description struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type Session struct {
	ID           string               `json:"id"`
	CallerUserID int64                `json:"caller_user_id"`
	CalleeUserID int64                `json:"callee_user_id"`
	CallType     callpb.CallType      `json:"call_type"`
	State        string               `json:"state"`
	Offer        Description          `json:"offer"`
	Answer       *Description         `json:"answer,omitempty"`
	EndReason    callpb.CallEndReason `json:"end_reason,omitempty"`
	EndMessage   string               `json:"end_message,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
	AcceptedAt   *time.Time           `json:"accepted_at,omitempty"`
	EndedAt      *time.Time           `json:"ended_at,omitempty"`
	RingDeadline time.Time            `json:"ring_deadline"`
}

func (s Session) Peer(userID int64) (int64, error) {
	switch userID {
	case s.CallerUserID:
		return s.CalleeUserID, nil
	case s.CalleeUserID:
		return s.CallerUserID, nil
	default:
		return 0, ErrForbidden
	}
}

type Store interface {
	Ping(context.Context) error
	UserTopic(context.Context, int64) (string, error)
	CreateSession(context.Context, Session) error
	GetSession(context.Context, string) (Session, error)
	AcceptSession(context.Context, string, int64, Description) (Session, error)
	RejectSession(context.Context, string, int64, callpb.CallEndReason, string) (Session, error)
	EndSession(context.Context, string, int64, callpb.CallEndReason, string) (Session, error)
	ExpireRinging(context.Context, time.Time, int64) ([]Session, error)
}

type Publisher interface {
	Publish(context.Context, string, *callpb.Delivery) error
}

type ICEProvider interface {
	Servers(int64, time.Time) []*callpb.IceServer
}
