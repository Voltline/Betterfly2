package call

import (
	"context"
	"errors"
	"time"

	callpb "Betterfly2/proto/call"
)

var (
	ErrRoomFull         = errors.New("group call is full")
	ErrMediaUnavailable = errors.New("group calls are not configured")
	ErrRoomConflict     = errors.New("group call changed concurrently")
)

type GroupRoom struct {
	ID              string            `json:"id"`
	GroupID         int64             `json:"group_id"`
	CreatorID       int64             `json:"creator_id"`
	CallType        callpb.CallType   `json:"call_type"`
	State           string            `json:"state"`
	MediaSID        string            `json:"media_sid"`
	MaxParticipants int               `json:"max_participants"`
	Participants    []RoomParticipant `json:"participants"`
	CreatedAt       time.Time         `json:"created_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Revision        int64             `json:"revision"`
}

type RoomParticipant struct {
	UserID       int64     `json:"user_id"`
	Identity     string    `json:"identity"`
	SID          string    `json:"sid,omitempty"`
	Connected    bool      `json:"connected"`
	JoinDeadline time.Time `json:"join_deadline"`
}

func (r GroupRoom) Name() string { return "bf2-group-" + r.ID }
func (r GroupRoom) Participant(userID int64) (RoomParticipant, bool) {
	for _, p := range r.Participants {
		if p.UserID == userID {
			return p, true
		}
	}
	return RoomParticipant{}, false
}
func (r GroupRoom) deadline() time.Time {
	deadline := r.ExpiresAt
	if len(r.Participants) == 0 && r.CreatedAt.Add(75*time.Second).Before(deadline) {
		deadline = r.CreatedAt.Add(75 * time.Second)
	}
	for _, p := range r.Participants {
		if !p.Connected && p.JoinDeadline.Before(deadline) {
			deadline = p.JoinDeadline
		}
	}
	return deadline
}

type GroupAccess struct{ Role, Name string }
type GroupAuthorizer interface {
	Access(context.Context, int64, int64) (GroupAccess, error)
	Members(context.Context, int64) ([]int64, error)
}

// GroupRoomStore shares the one-to-one busy keys and Redis event stream.
type GroupRoomStore interface {
	GetRoom(context.Context, string) (GroupRoom, error)
	GroupRoomID(context.Context, int64) (string, error)
	DueRooms(context.Context, time.Time, int64) ([]GroupRoom, error)
	CommitRoom(context.Context, *GroupRoom, GroupRoom, string, []PendingEvent) (bool, error)
}

type MediaServer interface {
	EnsureRoom(context.Context, GroupRoom) (string, error)
	Token(GroupRoom, RoomParticipant, time.Time) (string, time.Time, error)
	URL() string
	Execute(context.Context, string, []byte) error
	VerifyWebhook([]byte, string) (MediaWebhook, error)
}

type MediaWebhook struct {
	ID    string `json:"id"`
	Event string `json:"event"`
	Room  struct {
		Name string `json:"name"`
		SID  string `json:"sid"`
	} `json:"room"`
	Participant struct {
		Identity string `json:"identity"`
		SID      string `json:"sid"`
	} `json:"participant"`
}

type GroupCalls struct {
	store           GroupRoomStore
	access          GroupAuthorizer
	media           MediaServer
	maxParticipants int
	ttl             time.Duration
}

func (s *Service) EnableGroupCalls(store GroupRoomStore, access GroupAuthorizer, media MediaServer, maxParticipants int, ttl time.Duration) {
	if maxParticipants < 2 || maxParticipants > 64 {
		maxParticipants = 16
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	s.groups = &GroupCalls{store: store, access: access, media: media, maxParticipants: maxParticipants, ttl: ttl}
}
