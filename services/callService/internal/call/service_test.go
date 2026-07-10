package call

import (
	"context"
	"sync"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
)

type memoryStore struct {
	mu       sync.Mutex
	topics   map[int64]string
	sessions map[string]Session
}

func newMemoryStore() *memoryStore {
	return &memoryStore{topics: map[int64]string{}, sessions: map[string]Session{}}
}

func (s *memoryStore) Ping(context.Context) error { return nil }

func (s *memoryStore) UserTopic(_ context.Context, userID int64) (string, error) {
	topic := s.topics[userID]
	if topic == "" {
		return "", ErrUserOffline
	}
	return topic, nil
}

func (s *memoryStore) CreateSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.sessions {
		if existing.State == StateEnded {
			continue
		}
		if existing.CallerUserID == session.CallerUserID || existing.CalleeUserID == session.CallerUserID || existing.CallerUserID == session.CalleeUserID || existing.CalleeUserID == session.CalleeUserID {
			return ErrUserBusy
		}
	}
	s.sessions[session.ID] = session
	return nil
}

func (s *memoryStore) GetSession(_ context.Context, callID string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[callID]
	if !ok {
		return Session{}, ErrCallNotFound
	}
	return session, nil
}

func (s *memoryStore) AcceptSession(_ context.Context, callID string, userID int64, answer Description) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[callID]
	if !ok {
		return Session{}, ErrCallNotFound
	}
	if session.CalleeUserID != userID {
		return Session{}, ErrForbidden
	}
	if session.State != StateRinging {
		return Session{}, ErrInvalidState
	}
	now := time.Now().UTC()
	session.State = StateActive
	session.Answer = &answer
	session.AcceptedAt = &now
	s.sessions[callID] = session
	return session, nil
}

func (s *memoryStore) RejectSession(_ context.Context, callID string, userID int64, reason callpb.CallEndReason, message string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[callID]
	if !ok {
		return Session{}, ErrCallNotFound
	}
	if session.CalleeUserID != userID {
		return Session{}, ErrForbidden
	}
	if session.State != StateRinging {
		return Session{}, ErrInvalidState
	}
	endSession(&session, reason, message)
	s.sessions[callID] = session
	return session, nil
}

func (s *memoryStore) EndSession(_ context.Context, callID string, userID int64, reason callpb.CallEndReason, message string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[callID]
	if !ok {
		return Session{}, ErrCallNotFound
	}
	if _, err := session.Peer(userID); err != nil {
		return Session{}, err
	}
	if session.State == StateEnded {
		return Session{}, ErrInvalidState
	}
	endSession(&session, reason, message)
	s.sessions[callID] = session
	return session, nil
}

func (s *memoryStore) ExpireRinging(_ context.Context, now time.Time, _ int64) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []Session
	for id, session := range s.sessions {
		if session.State != StateRinging || session.RingDeadline.After(now) {
			continue
		}
		endSession(&session, callpb.CallEndReason_TIMEOUT, "call timed out")
		s.sessions[id] = session
		expired = append(expired, session)
	}
	return expired, nil
}

type publishedDelivery struct {
	topic    string
	delivery *callpb.Delivery
}

type memoryPublisher struct {
	deliveries []publishedDelivery
}

func (p *memoryPublisher) Publish(_ context.Context, topic string, delivery *callpb.Delivery) error {
	p.deliveries = append(p.deliveries, publishedDelivery{topic: topic, delivery: delivery})
	return nil
}

type testICE struct{}

func (testICE) Servers(_ int64, _ time.Time) []*callpb.IceServer {
	return []*callpb.IceServer{{Urls: []string{"turn:test"}, Username: "temporary"}}
}

func TestCallLifecycle(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	publisher := &memoryPublisher{}
	service := NewService(store, publisher, testICE{}, 45*time.Second)

	if err := service.Handle(context.Background(), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatalf("initiate call: %v", err)
	}
	if len(publisher.deliveries) != 2 {
		t.Fatalf("expected outgoing and incoming events, got %d", len(publisher.deliveries))
	}
	callID := publisher.deliveries[0].delivery.GetEvent().GetCallId()
	if callID == "" || publisher.deliveries[1].delivery.GetEvent().GetEventType() != callpb.CallEventType_INCOMING_CALL {
		t.Fatalf("invalid incoming event: %+v", publisher.deliveries[1].delivery.GetEvent())
	}

	accept := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Accept{Accept: &callpb.AcceptCall{
		CallId: callID, Answer: &callpb.SessionDescription{Type: "answer", Sdp: "answer-sdp"},
	}}}}
	if err := service.Handle(context.Background(), accept); err != nil {
		t.Fatalf("accept call: %v", err)
	}
	if len(publisher.deliveries) != 4 || publisher.deliveries[3].delivery.GetTargetUserId() != 1 {
		t.Fatalf("accepted call was not delivered to both participants")
	}

	ice := &callpb.InternalRequest{FromKafkaTopic: "df-a", UserId: 1, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_IceCandidate{IceCandidate: &callpb.SendIceCandidate{
		CallId: callID, Candidate: &callpb.IceCandidate{Candidate: "candidate:1"},
	}}}}
	if err := service.Handle(context.Background(), ice); err != nil {
		t.Fatalf("forward ICE: %v", err)
	}
	last := publisher.deliveries[len(publisher.deliveries)-1]
	if last.topic != "df-b" || last.delivery.GetTargetUserId() != 2 || last.delivery.GetEvent().GetPeerUserId() != 1 {
		t.Fatalf("ICE routed incorrectly: %+v", last)
	}

	hangup := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Hangup{Hangup: &callpb.HangupCall{CallId: callID}}}}
	if err := service.Handle(context.Background(), hangup); err != nil {
		t.Fatalf("hangup call: %v", err)
	}
	requester := publisher.deliveries[len(publisher.deliveries)-2]
	peer := publisher.deliveries[len(publisher.deliveries)-1]
	if requester.topic != "df-b" || requester.delivery.GetTargetUserId() != 2 || peer.topic != "df-a" || peer.delivery.GetTargetUserId() != 1 {
		t.Fatalf("hangup events routed incorrectly: requester=%+v peer=%+v", requester, peer)
	}
}

func TestOfflineAndUnauthorizedRequestsReturnErrors(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	publisher := &memoryPublisher{}
	service := NewService(store, publisher, testICE{}, time.Minute)

	if err := service.Handle(context.Background(), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatalf("offline request should produce a client error: %v", err)
	}
	if got := publisher.deliveries[0].delivery.GetEvent().GetErrorCode(); got != callpb.CallErrorCode_USER_OFFLINE {
		t.Fatalf("expected USER_OFFLINE, got %v", got)
	}

	store.topics[2] = "df-b"
	store.topics[3] = "df-c"
	if err := service.Handle(context.Background(), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	callID := publisher.deliveries[len(publisher.deliveries)-2].delivery.GetEvent().GetCallId()
	unauthorized := &callpb.InternalRequest{FromKafkaTopic: "df-c", UserId: 3, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Accept{Accept: &callpb.AcceptCall{
		CallId: callID, Answer: &callpb.SessionDescription{Type: "answer", Sdp: "answer"},
	}}}}
	if err := service.Handle(context.Background(), unauthorized); err != nil {
		t.Fatal(err)
	}
	if got := publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent().GetErrorCode(); got != callpb.CallErrorCode_FORBIDDEN {
		t.Fatalf("expected FORBIDDEN, got %v", got)
	}
}

func TestSweepExpiredNotifiesBothUsers(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	publisher := &memoryPublisher{}
	service := NewService(store, publisher, testICE{}, time.Second)
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	if err := service.Handle(context.Background(), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(2 * time.Second) }
	if err := service.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.deliveries) != 4 {
		t.Fatalf("expected two timeout events, got %d total deliveries", len(publisher.deliveries))
	}
	for _, delivery := range publisher.deliveries[2:] {
		if delivery.delivery.GetEvent().GetEndReason() != callpb.CallEndReason_TIMEOUT {
			t.Fatalf("expected TIMEOUT, got %v", delivery.delivery.GetEvent().GetEndReason())
		}
	}
}

func TestBusyUserCannotEnterSecondCall(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	store.topics[3] = "df-c"
	publisher := &memoryPublisher{}
	service := NewService(store, publisher, testICE{}, time.Minute)

	if err := service.Handle(context.Background(), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	if err := service.Handle(context.Background(), initiateRequest(3, 2, "df-c")); err != nil {
		t.Fatal(err)
	}
	last := publisher.deliveries[len(publisher.deliveries)-1]
	if last.topic != "df-c" || last.delivery.GetEvent().GetErrorCode() != callpb.CallErrorCode_USER_BUSY {
		t.Fatalf("expected USER_BUSY for second caller, got %+v", last)
	}
}

func TestICEProviderUsesTemporaryCredentials(t *testing.T) {
	provider := NewStaticICEProvider("stun:example.com", "turn:example.com", "secret", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	servers := provider.Servers(42, now)
	if len(servers) != 2 || servers[1].GetUsername() != "1700003600:42" || servers[1].GetCredential() == "" {
		t.Fatalf("unexpected ICE servers: %+v", servers)
	}
}

func initiateRequest(callerID, calleeID int64, topic string) *callpb.InternalRequest {
	return &callpb.InternalRequest{
		FromKafkaTopic: topic,
		UserId:         callerID,
		Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Initiate{Initiate: &callpb.InitiateCall{
			CalleeUserId: calleeID,
			CallType:     callpb.CallType_VIDEO,
			Offer:        &callpb.SessionDescription{Type: "offer", Sdp: "offer-sdp"},
		}}},
	}
}
