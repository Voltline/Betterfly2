package call

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	"google.golang.org/protobuf/proto"
)

type memoryStore struct {
	mu        sync.Mutex
	topics    map[int64]string
	sessions  map[string]Session
	publisher *memoryPublisher
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

func (s *memoryStore) OperationCompleted(context.Context, string) (bool, error) { return false, nil }

func (s *memoryStore) CreateSessionWithEvents(ctx context.Context, session Session, _ string, events []PendingEvent) (bool, error) {
	s.mu.Lock()
	for _, existing := range s.sessions {
		if existing.State == StateEnded {
			continue
		}
		if existing.CallerUserID == session.CallerUserID || existing.CalleeUserID == session.CallerUserID || existing.CallerUserID == session.CalleeUserID || existing.CalleeUserID == session.CalleeUserID {
			s.mu.Unlock()
			return false, ErrUserBusy
		}
	}
	s.sessions[session.ID] = session
	s.mu.Unlock()
	return false, s.publishEvents(ctx, events)
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

func (s *memoryStore) TransitionSessionWithEvents(ctx context.Context, expected, updated Session, _ bool, _ string, events []PendingEvent) (bool, error) {
	s.mu.Lock()
	if current, ok := s.sessions[expected.ID]; !ok || !reflect.DeepEqual(current, expected) {
		s.mu.Unlock()
		return false, ErrInvalidState
	}
	s.sessions[updated.ID] = updated
	s.mu.Unlock()
	return false, s.publishEvents(ctx, events)
}

func (s *memoryStore) ExpiredRinging(_ context.Context, now time.Time, _ int64) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []Session
	for _, session := range s.sessions {
		if session.State != StateRinging || session.RingDeadline.After(now) {
			continue
		}
		expired = append(expired, session)
	}
	return expired, nil
}

func (s *memoryStore) publishEvents(ctx context.Context, events []PendingEvent) error {
	if s.publisher == nil {
		return nil
	}
	for _, event := range events {
		env := &envelope.Envelope{}
		if err := proto.Unmarshal(event.Payload, env); err != nil {
			return err
		}
		switch env.GetType() {
		case envelope.MessageType_CALL_RESPONSE:
			delivery := &callpb.Delivery{}
			if err := proto.Unmarshal(env.GetPayload(), delivery); err != nil {
				return err
			}
			if err := s.publisher.Publish(ctx, event.Topic, delivery); err != nil {
				return err
			}
		case envelope.MessageType_PUSH_REQUEST:
			request := &pushpb.RequestMessage{}
			if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
				return err
			}
			if err := s.publisher.PublishPush(ctx, event.Topic, request); err != nil {
				return err
			}
		}
	}
	return nil
}

type publishedDelivery struct {
	topic    string
	delivery *callpb.Delivery
}

type memoryPublisher struct {
	deliveries  []publishedDelivery
	pushes      []*pushpb.RequestMessage
	pushTopics  []string
	failPublish int
	failPush    int
}

func (p *memoryPublisher) PublishPush(_ context.Context, topic string, request *pushpb.RequestMessage) error {
	if p.failPush > 0 {
		p.failPush--
		return errors.New("temporary push publish failure")
	}
	p.pushes = append(p.pushes, request)
	p.pushTopics = append(p.pushTopics, topic)
	return nil
}

func (p *memoryPublisher) Publish(_ context.Context, topic string, delivery *callpb.Delivery) error {
	if p.failPublish > 0 {
		p.failPublish--
		return errors.New("temporary delivery publish failure")
	}
	p.deliveries = append(p.deliveries, publishedDelivery{topic: topic, delivery: delivery})
	return nil
}

type testICE struct{}

func (testICE) Servers(_ int64, _ time.Time) []*callpb.IceServer {
	return []*callpb.IceServer{{Urls: []string{"turn:test"}, Username: "temporary"}}
}

func newMemoryService(store *memoryStore, publisher *memoryPublisher, ringTTL time.Duration) *Service {
	store.publisher = publisher
	return NewService(store, publisher, testICE{}, ringTTL)
}

func testCallContext(operation string) context.Context {
	return kafkaconsumer.WithOperationKey(context.Background(), operation)
}

func TestCallLifecycle(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, 45*time.Second)

	if err := service.Handle(testCallContext("lifecycle-init"), initiateRequest(1, 2, "df-a")); err != nil {
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
	if err := service.Handle(testCallContext("lifecycle-accept"), accept); err != nil {
		t.Fatalf("accept call: %v", err)
	}
	if len(publisher.deliveries) != 4 || publisher.deliveries[3].delivery.GetTargetUserId() != 1 {
		t.Fatalf("accepted call was not delivered to both participants")
	}

	ice := &callpb.InternalRequest{FromKafkaTopic: "df-a", UserId: 1, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_IceCandidate{IceCandidate: &callpb.SendIceCandidate{
		CallId: callID, Candidate: &callpb.IceCandidate{Candidate: "candidate:1"},
	}}}}
	if err := service.Handle(testCallContext("lifecycle-ice"), ice); err != nil {
		t.Fatalf("forward ICE: %v", err)
	}
	last := publisher.deliveries[len(publisher.deliveries)-1]
	if last.topic != "df-b" || last.delivery.GetTargetUserId() != 2 || last.delivery.GetEvent().GetPeerUserId() != 1 {
		t.Fatalf("ICE routed incorrectly: %+v", last)
	}

	hangup := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Hangup{Hangup: &callpb.HangupCall{CallId: callID}}}}
	if err := service.Handle(testCallContext("lifecycle-hangup"), hangup); err != nil {
		t.Fatalf("hangup call: %v", err)
	}
	requester := publisher.deliveries[len(publisher.deliveries)-2]
	peer := publisher.deliveries[len(publisher.deliveries)-1]
	if requester.topic != "df-b" || requester.delivery.GetTargetUserId() != 2 || peer.topic != "df-a" || peer.delivery.GetTargetUserId() != 1 {
		t.Fatalf("hangup events routed incorrectly: requester=%+v peer=%+v", requester, peer)
	}
}

func TestOfflineCallUsesVoIPPushAndUnauthorizedAcceptIsRejected(t *testing.T) {
	t.Setenv("KAFKA_VOIP_PUSH_TOPIC", "push-service-voip-test")
	store := newMemoryStore()
	store.topics[1] = "df-a"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, time.Minute)

	if err := service.Handle(testCallContext("offline-init"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatalf("offline request should create a push-backed call: %v", err)
	}
	if len(publisher.deliveries) != 1 || publisher.deliveries[0].delivery.GetEvent().GetEventType() != callpb.CallEventType_OUTGOING_CALL {
		t.Fatalf("expected outgoing call acknowledgement, got %+v", publisher.deliveries)
	}
	if len(publisher.pushes) != 1 || publisher.pushes[0].GetVoipCall().GetCalleeUserId() != 2 {
		t.Fatalf("expected VoIP push request, got %+v", publisher.pushes)
	}
	if len(publisher.pushTopics) != 1 || publisher.pushTopics[0] != "push-service-voip-test" {
		t.Fatalf("expected isolated VoIP push topic, got %v", publisher.pushTopics)
	}
	firstCallID := publisher.deliveries[0].delivery.GetEvent().GetCallId()
	if err := service.HandlePushResult(testCallContext("offline-push-failure"), &pushpb.VoIPPushResult{
		CallId: firstCallID, CallerUserId: 1, CalleeUserId: 2, Accepted: false, Reason: "no_active_voip_token", Required: true,
	}); err != nil {
		t.Fatal(err)
	}
	if publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent().GetEventType() != callpb.CallEventType_CALL_ENDED {
		t.Fatal("expected failed push to end the outgoing call")
	}

	store.topics[2] = "df-b"
	store.topics[3] = "df-c"
	if err := service.Handle(testCallContext("offline-redial"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	callID := publisher.deliveries[len(publisher.deliveries)-2].delivery.GetEvent().GetCallId()
	unauthorized := &callpb.InternalRequest{FromKafkaTopic: "df-c", UserId: 3, Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_Accept{Accept: &callpb.AcceptCall{
		CallId: callID, Answer: &callpb.SessionDescription{Type: "answer", Sdp: "answer"},
	}}}}
	if err := service.Handle(testCallContext("offline-unauthorized"), unauthorized); err != nil {
		t.Fatal(err)
	}
	if got := publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent().GetErrorCode(); got != callpb.CallErrorCode_FORBIDDEN {
		t.Fatalf("expected FORBIDDEN, got %v", got)
	}
}

func TestResumeCallReturnsPendingOfferAfterPushWake(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, time.Minute)

	if err := service.Handle(testCallContext("resume-init"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	callID := publisher.deliveries[0].delivery.GetEvent().GetCallId()
	store.topics[2] = "df-b"
	resume := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_ResumeCall{ResumeCall: &callpb.ResumeCall{CallId: callID}},
	}}
	if err := service.Handle(testCallContext("resume-delivery"), resume); err != nil {
		t.Fatal(err)
	}
	event := publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent()
	if event.GetEventType() != callpb.CallEventType_INCOMING_CALL || event.GetSessionDescription().GetSdp() != "offer-sdp" {
		t.Fatalf("unexpected resumed call event: %+v", event)
	}
}

func TestOfflineEarlyICECanResumeAcceptHangupAndRedial(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, time.Minute)
	ctx := context.Background()

	if err := service.Handle(testCallContext("early-ice-init"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatalf("initiate offline call: %v", err)
	}
	if countEvents(publisher, callpb.CallEventType_OUTGOING_CALL) != 1 || len(publisher.pushes) != 1 {
		t.Fatalf("expected outgoing call and VoIP push: deliveries=%+v pushes=%+v", publisher.deliveries, publisher.pushes)
	}
	callID := publisher.deliveries[0].delivery.GetEvent().GetCallId()
	session, err := store.GetSession(ctx, callID)
	if err != nil || session.State != StateRinging {
		t.Fatalf("expected ringing session, session=%+v err=%v", session, err)
	}

	deliveryCount := len(publisher.deliveries)
	ice := &callpb.InternalRequest{FromKafkaTopic: "df-a", UserId: 1, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_IceCandidate{IceCandidate: &callpb.SendIceCandidate{
			CallId: callID, Candidate: &callpb.IceCandidate{Candidate: "candidate:early"},
		}},
	}}
	if err := service.Handle(testCallContext("early-ice"), ice); err != nil {
		t.Fatalf("early ICE should be temporarily tolerated: %v", err)
	}
	if len(publisher.deliveries) != deliveryCount || countEvents(publisher, callpb.CallEventType_CALL_ERROR) != 0 || countEvents(publisher, callpb.CallEventType_CALL_ENDED) != 0 {
		t.Fatalf("early ICE generated a terminal/error event: %+v", publisher.deliveries)
	}
	session, _ = store.GetSession(ctx, callID)
	if session.State != StateRinging {
		t.Fatalf("early ICE changed session state: %s", session.State)
	}

	store.topics[2] = "df-b"
	resume := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_ResumeCall{ResumeCall: &callpb.ResumeCall{CallId: callID}},
	}}
	if err := service.Handle(testCallContext("early-resume"), resume); err != nil {
		t.Fatalf("resume call: %v", err)
	}
	incoming := publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent()
	if incoming.GetEventType() != callpb.CallEventType_INCOMING_CALL || incoming.GetSessionDescription().GetSdp() != "offer-sdp" {
		t.Fatalf("unexpected resumed incoming call: %+v", incoming)
	}
	session, _ = store.GetSession(ctx, callID)
	if session.State != StateRinging {
		t.Fatalf("resume should keep session ringing, got %s", session.State)
	}

	accept := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_Accept{Accept: &callpb.AcceptCall{
			CallId: callID, Answer: &callpb.SessionDescription{Type: "answer", Sdp: "answer-sdp"},
		}},
	}}
	if err := service.Handle(testCallContext("early-accept"), accept); err != nil {
		t.Fatalf("accept resumed call: %v", err)
	}
	session, _ = store.GetSession(ctx, callID)
	if session.State != StateActive {
		t.Fatalf("expected active session, got %s", session.State)
	}
	accepted := findLastEventForUser(publisher, callpb.CallEventType_CALL_ACCEPTED, 1)
	if accepted == nil || accepted.GetSessionDescription().GetSdp() != "answer-sdp" {
		t.Fatalf("caller did not receive answer: %+v", accepted)
	}

	hangup := &callpb.InternalRequest{FromKafkaTopic: "df-a", UserId: 1, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_Hangup{Hangup: &callpb.HangupCall{CallId: callID}},
	}}
	if err := service.Handle(testCallContext("early-hangup"), hangup); err != nil {
		t.Fatalf("hangup call: %v", err)
	}
	if err := service.Handle(testCallContext("early-redial"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatalf("redial after hangup: %v", err)
	}
	last := publisher.deliveries[len(publisher.deliveries)-2].delivery.GetEvent()
	if last.GetEventType() != callpb.CallEventType_OUTGOING_CALL || last.GetErrorCode() == callpb.CallErrorCode_USER_BUSY || last.GetCallId() == callID {
		t.Fatalf("redial should create a new outgoing call: %+v", last)
	}
}

func TestActiveICEToOfflinePeerStillReturnsCallError(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, time.Minute)

	if err := service.Handle(testCallContext("active-ice-init"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	callID := publisher.deliveries[0].delivery.GetEvent().GetCallId()
	accept := &callpb.InternalRequest{FromKafkaTopic: "df-b", UserId: 2, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_Accept{Accept: &callpb.AcceptCall{
			CallId: callID, Answer: &callpb.SessionDescription{Type: "answer", Sdp: "answer-sdp"},
		}},
	}}
	if err := service.Handle(testCallContext("active-ice-accept"), accept); err != nil {
		t.Fatal(err)
	}
	delete(store.topics, 2)
	ice := &callpb.InternalRequest{FromKafkaTopic: "df-a", UserId: 1, Request: &callpb.ClientRequest{
		Payload: &callpb.ClientRequest_IceCandidate{IceCandidate: &callpb.SendIceCandidate{
			CallId: callID, Candidate: &callpb.IceCandidate{Candidate: "candidate:active"},
		}},
	}}
	if err := service.Handle(testCallContext("active-ice-send"), ice); err != nil {
		t.Fatal(err)
	}
	last := publisher.deliveries[len(publisher.deliveries)-1].delivery.GetEvent()
	if last.GetEventType() != callpb.CallEventType_CALL_ERROR || last.GetErrorCode() != callpb.CallErrorCode_USER_OFFLINE {
		t.Fatalf("active ICE offline error was incorrectly swallowed: %+v", last)
	}
}

func TestSweepExpiredNotifiesBothUsers(t *testing.T) {
	store := newMemoryStore()
	store.topics[1] = "df-a"
	store.topics[2] = "df-b"
	publisher := &memoryPublisher{}
	service := newMemoryService(store, publisher, time.Second)
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	if err := service.Handle(testCallContext("sweep-init"), initiateRequest(1, 2, "df-a")); err != nil {
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
	service := newMemoryService(store, publisher, time.Minute)

	if err := service.Handle(testCallContext("busy-first"), initiateRequest(1, 2, "df-a")); err != nil {
		t.Fatal(err)
	}
	if err := service.Handle(testCallContext("busy-second"), initiateRequest(3, 2, "df-c")); err != nil {
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

func countEvents(publisher *memoryPublisher, eventType callpb.CallEventType) int {
	count := 0
	for _, delivery := range publisher.deliveries {
		if delivery.delivery.GetEvent().GetEventType() == eventType {
			count++
		}
	}
	return count
}

func findLastEventForUser(publisher *memoryPublisher, eventType callpb.CallEventType, userID int64) *callpb.CallEvent {
	for index := len(publisher.deliveries) - 1; index >= 0; index-- {
		delivery := publisher.deliveries[index].delivery
		if delivery.GetTargetUserId() == userID && delivery.GetEvent().GetEventType() == eventType {
			return delivery.GetEvent()
		}
	}
	return nil
}

func TestCallDomainHelpers(t *testing.T) {
	session := Session{CallerUserID: 1, CalleeUserID: 2}
	if peer, err := session.Peer(1); err != nil || peer != 2 {
		t.Fatalf("caller peer mismatch: peer=%d err=%v", peer, err)
	}
	if peer, err := session.Peer(2); err != nil || peer != 1 {
		t.Fatalf("callee peer mismatch: peer=%d err=%v", peer, err)
	}
	if _, err := session.Peer(3); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected non-participant rejection, got %v", err)
	}

	if !validDescription(&callpb.SessionDescription{Type: " OFFER ", Sdp: "sdp"}, "offer") {
		t.Fatal("expected case-insensitive trimmed offer to be valid")
	}
	invalidDescriptions := []*callpb.SessionDescription{
		nil,
		{Type: "answer", Sdp: "sdp"},
		{Type: "offer", Sdp: " "},
	}
	for _, description := range invalidDescriptions {
		if validDescription(description, "offer") {
			t.Fatalf("expected invalid description: %+v", description)
		}
	}

	if got := splitCSV(" stun:a, ,turn:b "); !reflect.DeepEqual(got, []string{"stun:a", "turn:b"}) {
		t.Fatalf("unexpected CSV parsing: %#v", got)
	}
}

func TestCallErrorAndStateMappings(t *testing.T) {
	errorTests := []struct {
		err  error
		want callpb.CallErrorCode
	}{
		{ErrInvalidInput, callpb.CallErrorCode_INVALID_ARGUMENT},
		{ErrUserOffline, callpb.CallErrorCode_USER_OFFLINE},
		{ErrUserBusy, callpb.CallErrorCode_USER_BUSY},
		{ErrCallNotFound, callpb.CallErrorCode_CALL_NOT_FOUND},
		{ErrInvalidState, callpb.CallErrorCode_INVALID_STATE},
		{ErrForbidden, callpb.CallErrorCode_FORBIDDEN},
		{errors.New("database unavailable"), callpb.CallErrorCode_INTERNAL_ERROR},
	}
	for _, tt := range errorTests {
		if got := errorCode(tt.err); got != tt.want {
			t.Errorf("errorCode(%v)=%v want %v", tt.err, got, tt.want)
		}
	}

	stateTests := map[string]callpb.CallState{
		StateRinging: callpb.CallState_RINGING,
		StateActive:  callpb.CallState_ACTIVE,
		StateEnded:   callpb.CallState_ENDED,
		"unknown":    callpb.CallState_CALL_STATE_UNSPECIFIED,
	}
	for state, want := range stateTests {
		if got := stateToProto(state); got != want {
			t.Errorf("stateToProto(%q)=%v want %v", state, got, want)
		}
	}
	if callTypeName(callpb.CallType_VIDEO) != "video" || callTypeName(callpb.CallType_AUDIO) != "audio" {
		t.Fatal("call type names are inconsistent")
	}
}
