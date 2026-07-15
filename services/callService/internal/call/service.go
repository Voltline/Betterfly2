package call

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/mq"
)

type Service struct {
	store     Store
	publisher Publisher
	ice       ICEProvider
	ringTTL   time.Duration
	now       func() time.Time
}

func NewService(store Store, publisher Publisher, ice ICEProvider, ringTTL time.Duration) *Service {
	if ringTTL <= 0 {
		ringTTL = 45 * time.Second
	}
	return &Service{store: store, publisher: publisher, ice: ice, ringTTL: ringTTL, now: time.Now}
}

func (s *Service) Ready(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *Service) Handle(ctx context.Context, request *callpb.InternalRequest) error {
	if request.GetUserId() <= 0 || request.GetFromKafkaTopic() == "" || request.GetRequest() == nil {
		return ErrInvalidInput
	}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		completed, err := atomicStore.OperationCompleted(ctx, operationKey(ctx))
		if err != nil {
			return err
		}
		if completed {
			return nil
		}
	}

	var err error
	switch payload := request.GetRequest().Payload.(type) {
	case *callpb.ClientRequest_GetConfig:
		err = s.getConfig(ctx, request)
	case *callpb.ClientRequest_Initiate:
		err = s.initiate(ctx, request, payload.Initiate)
	case *callpb.ClientRequest_Accept:
		err = s.accept(ctx, request, payload.Accept)
	case *callpb.ClientRequest_Reject:
		err = s.reject(ctx, request, payload.Reject)
	case *callpb.ClientRequest_Hangup:
		err = s.hangup(ctx, request, payload.Hangup)
	case *callpb.ClientRequest_IceCandidate:
		err = s.forwardICE(ctx, request, payload.IceCandidate)
	case *callpb.ClientRequest_ResumeCall:
		err = s.resumeCall(ctx, request, payload.ResumeCall)
	default:
		err = ErrInvalidInput
	}
	if err == nil {
		return nil
	}
	var publishErr *deliveryError
	if errors.As(err, &publishErr) || !isCallDomainError(err) {
		return err
	}

	callID := requestCallID(request.GetRequest())
	if publishErr := s.publishToTopic(ctx, request.GetFromKafkaTopic(), request.GetUserId(), s.errorEvent(callID, err)); publishErr != nil {
		return fmt.Errorf("handle call request: %v; publish error response: %w", err, publishErr)
	}
	return nil
}

func (s *Service) SweepExpired(ctx context.Context) error {
	sessions, err := s.store.ExpireRinging(ctx, s.now().UTC(), 100)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		eventForCaller := s.sessionEvent(callpb.CallEventType_CALL_ENDED, session, session.CalleeUserID)
		eventForCallee := s.sessionEvent(callpb.CallEventType_CALL_ENDED, session, session.CallerUserID)
		s.publishBestEffortToUser(ctx, session.CallerUserID, eventForCaller)
		s.publishBestEffortToUser(ctx, session.CalleeUserID, eventForCallee)
	}
	return nil
}

func (s *Service) getConfig(ctx context.Context, request *callpb.InternalRequest) error {
	event := &callpb.CallEvent{
		EventType:  callpb.CallEventType_CALL_CONFIG,
		IceServers: s.ice.Servers(request.GetUserId(), s.now().UTC()),
		Timestamp:  timestamp(s.now()),
	}
	return s.publishToTopic(ctx, request.GetFromKafkaTopic(), request.GetUserId(), event)
}

func (s *Service) initiate(ctx context.Context, request *callpb.InternalRequest, payload *callpb.InitiateCall) error {
	if payload == nil || payload.GetCalleeUserId() <= 0 || payload.GetCalleeUserId() == request.GetUserId() {
		return ErrInvalidInput
	}
	if payload.GetCallType() != callpb.CallType_AUDIO && payload.GetCallType() != callpb.CallType_VIDEO {
		return ErrInvalidInput
	}
	if !validDescription(payload.GetOffer(), "offer") {
		return ErrInvalidInput
	}

	calleeTopic, routeErr := s.store.UserTopic(ctx, payload.GetCalleeUserId())
	calleeOnline := routeErr == nil
	if routeErr != nil && !errors.Is(routeErr, ErrUserOffline) {
		return routeErr
	}
	now := s.now().UTC()
	session := Session{
		ID:           callIDForContext(ctx),
		CallerUserID: request.GetUserId(),
		CalleeUserID: payload.GetCalleeUserId(),
		CallType:     payload.GetCallType(),
		State:        StateRinging,
		Offer:        descriptionFromProto(payload.GetOffer()),
		CreatedAt:    now,
		RingDeadline: now.Add(s.ringTTL),
	}
	callerEvent := s.sessionEvent(callpb.CallEventType_OUTGOING_CALL, session, session.CalleeUserID)
	callerEvent.SessionDescription = descriptionToProto(session.Offer)
	callerEvent.IceServers = s.ice.Servers(session.CallerUserID, now)
	pushRequest := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{
		CallId: session.ID, CallerUserId: session.CallerUserID, CalleeUserId: session.CalleeUserID,
		CallType: callTypeName(session.CallType), ResultKafkaTopic: "call-service",
		ExpiresAt: session.RingDeadline.UTC().Format(time.RFC3339Nano), Required: !calleeOnline,
	}}}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		events, eventErr := s.initiatePendingEvents(ctx, request, session, calleeOnline, calleeTopic, callerEvent, pushRequest)
		if eventErr != nil {
			return eventErr
		}
		replayed, commitErr := atomicStore.CreateSessionWithEvents(ctx, session, operationKey(ctx), events)
		if commitErr != nil {
			return commitErr
		}
		if replayed {
			logger.Sugar().Debugw("重放Call initiate，复用已有Redis事件", "call_id", session.ID, "operation_key", operationKey(ctx))
		}
		return nil
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return err
	}
	if err := s.publishToTopic(ctx, request.GetFromKafkaTopic(), session.CallerUserID, callerEvent); err != nil {
		return fmt.Errorf("publish outgoing call: %w", err)
	}
	if calleeOnline {
		calleeEvent := s.incomingEvent(session, now)
		if err := s.publishToTopic(ctx, calleeTopic, session.CalleeUserID, calleeEvent); err != nil {
			return fmt.Errorf("publish incoming call: %w", err)
		}
	}
	pushTopic := voipPushTopic()
	logger.Sugar().Debugf("发布VoIP Push请求: call_id=%s topic=%s required=%t", session.ID, pushTopic, !calleeOnline)
	if err := s.publisher.PublishPush(ctx, pushTopic, pushRequest); err != nil {
		if calleeOnline {
			logger.Sugar().Warnf("在线通话的冗余VoIP Push请求失败: call_id=%s error=%v", session.ID, err)
			return nil
		}
		logger.Sugar().Warnf("发布VoIP Push请求失败，取消通话: call_id=%s error=%v", session.ID, err)
		return fmt.Errorf("publish required VoIP push request: %w", asDeliveryError(err))
	}
	return nil
}

func voipPushTopic() string {
	if topic := strings.TrimSpace(os.Getenv("KAFKA_VOIP_PUSH_TOPIC")); topic != "" {
		return topic
	}
	return "push-service-voip"
}

func (s *Service) resumeCall(ctx context.Context, request *callpb.InternalRequest, payload *callpb.ResumeCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" {
		return ErrInvalidInput
	}
	session, err := s.store.GetSession(ctx, payload.GetCallId())
	if err != nil {
		return err
	}
	if session.CalleeUserID != request.GetUserId() {
		return ErrForbidden
	}
	if session.State != StateRinging || !s.now().Before(session.RingDeadline) {
		return ErrInvalidState
	}
	return s.publishToTopic(ctx, request.GetFromKafkaTopic(), session.CalleeUserID, s.incomingEvent(session, s.now().UTC()))
}

func (s *Service) HandlePushResult(ctx context.Context, result *pushpb.VoIPPushResult) error {
	if result == nil || strings.TrimSpace(result.GetCallId()) == "" {
		return ErrInvalidInput
	}
	if result.GetAccepted() || !result.GetRequired() {
		return nil
	}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		return s.handlePushResultAtomic(ctx, atomicStore, result)
	}
	session, err := s.store.GetSession(ctx, result.GetCallId())
	if errors.Is(err, ErrCallNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if session.CallerUserID != result.GetCallerUserId() || session.CalleeUserID != result.GetCalleeUserId() || session.State != StateRinging {
		return nil
	}
	ended, err := s.store.EndSession(ctx, session.ID, session.CallerUserID, callpb.CallEndReason_CANCELLED, result.GetReason())
	if err != nil {
		return err
	}
	err = s.publishToUser(ctx, session.CallerUserID, "", s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CalleeUserID))
	if errors.Is(err, ErrUserOffline) {
		return nil
	}
	return err
}

func (s *Service) accept(ctx context.Context, request *callpb.InternalRequest, payload *callpb.AcceptCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" || !validDescription(payload.GetAnswer(), "answer") {
		return ErrInvalidInput
	}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		return s.acceptAtomic(ctx, atomicStore, request, payload)
	}
	session, err := s.store.AcceptSession(ctx, payload.GetCallId(), request.GetUserId(), descriptionFromProto(payload.GetAnswer()))
	if err != nil {
		return err
	}

	calleeEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, session, session.CallerUserID)
	if err := s.publishToTopic(ctx, request.GetFromKafkaTopic(), session.CalleeUserID, calleeEvent); err != nil {
		return err
	}
	callerEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, session, session.CalleeUserID)
	callerEvent.SessionDescription = descriptionToProto(*session.Answer)
	if err := s.publishToUser(ctx, session.CallerUserID, "", callerEvent); err != nil {
		var publishErr *deliveryError
		if errors.As(err, &publishErr) || !errors.Is(err, ErrUserOffline) {
			return err
		}
		logger.Sugar().Warnf("接听后主叫已不可达，结束通话: call_id=%s error=%v", session.ID, err)
		ended, endErr := s.store.EndSession(ctx, session.ID, session.CalleeUserID, callpb.CallEndReason_DISCONNECTED, "caller unavailable after accept")
		if endErr != nil {
			return endErr
		}
		return s.publishToTopic(ctx, request.GetFromKafkaTopic(), session.CalleeUserID, s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CallerUserID))
	}
	return nil
}

func (s *Service) reject(ctx context.Context, request *callpb.InternalRequest, payload *callpb.RejectCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" {
		return ErrInvalidInput
	}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		return s.rejectAtomic(ctx, atomicStore, request, payload)
	}
	session, err := s.store.RejectSession(ctx, payload.GetCallId(), request.GetUserId(), callpb.CallEndReason_REJECTED, payload.GetMessage())
	if err != nil {
		return err
	}
	return s.publishTerminal(ctx, request.GetFromKafkaTopic(), request.GetUserId(), session, callpb.CallEventType_CALL_REJECTED)
}

func (s *Service) hangup(ctx context.Context, request *callpb.InternalRequest, payload *callpb.HangupCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" {
		return ErrInvalidInput
	}
	reason := payload.GetReason()
	if reason == callpb.CallEndReason_CALL_END_REASON_UNSPECIFIED || reason == callpb.CallEndReason_REJECTED || reason == callpb.CallEndReason_TIMEOUT {
		reason = callpb.CallEndReason_HANGUP
	}
	if atomicStore, ok := s.atomicStore(ctx); ok {
		return s.hangupAtomic(ctx, atomicStore, request, payload.GetCallId(), reason)
	}
	session, err := s.store.EndSession(ctx, payload.GetCallId(), request.GetUserId(), reason, "")
	if err != nil {
		return err
	}
	return s.publishTerminal(ctx, request.GetFromKafkaTopic(), request.GetUserId(), session, callpb.CallEventType_CALL_ENDED)
}

func (s *Service) forwardICE(ctx context.Context, request *callpb.InternalRequest, payload *callpb.SendIceCandidate) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" || payload.GetCandidate() == nil || strings.TrimSpace(payload.GetCandidate().GetCandidate()) == "" {
		return ErrInvalidInput
	}
	session, err := s.store.GetSession(ctx, payload.GetCallId())
	if err != nil {
		return err
	}
	peerID, err := session.Peer(request.GetUserId())
	if err != nil {
		return err
	}
	if session.State != StateRinging && session.State != StateActive {
		return ErrInvalidState
	}
	event := s.sessionEvent(callpb.CallEventType_ICE_CANDIDATE_RECEIVED, session, request.GetUserId())
	event.IceCandidate = payload.GetCandidate()
	err = s.publishToUser(ctx, peerID, "", event)
	if errors.Is(err, ErrUserOffline) && session.State == StateRinging {
		logger.Sugar().Debugf(
			"对端仍在等待VoIP Push唤醒，忽略暂时无法投递的ICE: call_id=%s sender_user_id=%d peer_user_id=%d session_state=%s",
			session.ID, request.GetUserId(), peerID, session.State,
		)
		return nil
	}
	return err
}

func (s *Service) publishTerminal(ctx context.Context, requesterTopic string, requesterID int64, session Session, eventType callpb.CallEventType) error {
	peerID, err := session.Peer(requesterID)
	if err != nil {
		return err
	}
	requesterEvent := s.sessionEvent(eventType, session, peerID)
	if err := s.publishToTopic(ctx, requesterTopic, requesterID, requesterEvent); err != nil {
		return err
	}
	peerEvent := s.sessionEvent(eventType, session, requesterID)
	if err := s.publishToUser(ctx, peerID, "", peerEvent); err != nil && !errors.Is(err, ErrUserOffline) {
		return err
	}
	return nil
}

func (s *Service) incomingEvent(session Session, now time.Time) *callpb.CallEvent {
	event := s.sessionEvent(callpb.CallEventType_INCOMING_CALL, session, session.CallerUserID)
	event.SessionDescription = descriptionToProto(session.Offer)
	event.IceServers = s.ice.Servers(session.CalleeUserID, now)
	return event
}

func (s *Service) publishBestEffortToTopic(ctx context.Context, topic string, userID int64, event *callpb.CallEvent) {
	if err := s.publishToTopic(ctx, topic, userID, event); err != nil {
		logger.Sugar().Warnf("通话事件投递失败: user_id=%d event=%v error=%v", userID, event.GetEventType(), err)
	}
}

func (s *Service) publishBestEffortToUser(ctx context.Context, userID int64, event *callpb.CallEvent) {
	if err := s.publishToUser(ctx, userID, "", event); err != nil {
		logger.Sugar().Warnf("通话事件无法路由: user_id=%d event=%v error=%v", userID, event.GetEventType(), err)
	}
}

func (s *Service) publishToUser(ctx context.Context, userID int64, fallbackTopic string, event *callpb.CallEvent) error {
	topic, err := s.store.UserTopic(ctx, userID)
	if err != nil {
		if fallbackTopic == "" {
			return err
		}
		topic = fallbackTopic
	}
	return s.publishToTopic(ctx, topic, userID, event)
}

func (s *Service) publishToTopic(ctx context.Context, topic string, userID int64, event *callpb.CallEvent) error {
	return asDeliveryError(s.publisher.Publish(ctx, topic, &callpb.Delivery{TargetUserId: userID, Event: event}))
}

func (s *Service) atomicStore(ctx context.Context) (AtomicStore, bool) {
	operationKey, hasOperationKey := kafkaconsumer.OperationKeyFromContext(ctx)
	if !hasOperationKey || operationKey == "" {
		return nil, false
	}
	store, ok := s.store.(AtomicStore)
	return store, ok
}

func operationKey(ctx context.Context) string {
	value, _ := kafkaconsumer.OperationKeyFromContext(ctx)
	return value
}

func (s *Service) initiatePendingEvents(
	ctx context.Context,
	request *callpb.InternalRequest,
	session Session,
	calleeOnline bool,
	calleeTopic string,
	callerEvent *callpb.CallEvent,
	pushRequest *pushpb.RequestMessage,
) ([]PendingEvent, error) {
	operation := operationKey(ctx)
	events := make([]PendingEvent, 0, 3)
	caller, err := pendingCallDelivery(operation, "outgoing", request.GetFromKafkaTopic(), session.CallerUserID, callerEvent)
	if err != nil {
		return nil, err
	}
	events = append(events, caller)
	if calleeOnline {
		incoming, err := pendingCallDelivery(operation, "incoming", calleeTopic, session.CalleeUserID, s.incomingEvent(session, s.now().UTC()))
		if err != nil {
			return nil, err
		}
		events = append(events, incoming)
	}
	push, err := pendingPushRequest(operation, "voip-push", voipPushTopic(), pushRequest)
	if err != nil {
		return nil, err
	}
	return append(events, push), nil
}

func (s *Service) acceptAtomic(ctx context.Context, store AtomicStore, request *callpb.InternalRequest, payload *callpb.AcceptCall) error {
	expected, err := s.store.GetSession(ctx, payload.GetCallId())
	if err != nil {
		return err
	}
	if expected.CalleeUserID != request.GetUserId() {
		return ErrForbidden
	}
	if expected.State != StateRinging || !s.now().Before(expected.RingDeadline) {
		return ErrInvalidState
	}
	now := s.now().UTC()
	updated := expected
	answer := descriptionFromProto(payload.GetAnswer())
	updated.State = StateActive
	updated.Answer = &answer
	updated.AcceptedAt = &now
	operation := operationKey(ctx)
	events := make([]PendingEvent, 0, 2)
	callerTopic, routeErr := s.store.UserTopic(ctx, expected.CallerUserID)
	if routeErr != nil && !errors.Is(routeErr, ErrUserOffline) {
		return routeErr
	}
	if errors.Is(routeErr, ErrUserOffline) {
		updated.State = StateEnded
		updated.EndReason = callpb.CallEndReason_DISCONNECTED
		updated.EndMessage = "caller unavailable after accept"
		updated.EndedAt = &now
		ended := s.sessionEvent(callpb.CallEventType_CALL_ENDED, updated, expected.CallerUserID)
		pending, err := pendingCallDelivery(operation, "callee-ended", request.GetFromKafkaTopic(), expected.CalleeUserID, ended)
		if err != nil {
			return err
		}
		events = append(events, pending)
	} else {
		calleeEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, updated, expected.CallerUserID)
		calleePending, err := pendingCallDelivery(operation, "callee-accepted", request.GetFromKafkaTopic(), expected.CalleeUserID, calleeEvent)
		if err != nil {
			return err
		}
		callerEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, updated, expected.CalleeUserID)
		callerEvent.SessionDescription = descriptionToProto(answer)
		callerPending, err := pendingCallDelivery(operation, "caller-accepted", callerTopic, expected.CallerUserID, callerEvent)
		if err != nil {
			return err
		}
		events = append(events, calleePending, callerPending)
	}
	_, err = store.TransitionSessionWithEvents(ctx, expected, updated, updated.State == StateEnded, operation, events)
	return err
}

func (s *Service) rejectAtomic(ctx context.Context, store AtomicStore, request *callpb.InternalRequest, payload *callpb.RejectCall) error {
	expected, err := s.store.GetSession(ctx, payload.GetCallId())
	if err != nil {
		return err
	}
	if expected.CalleeUserID != request.GetUserId() {
		return ErrForbidden
	}
	if expected.State != StateRinging {
		return ErrInvalidState
	}
	updated := expected
	now := s.now().UTC()
	updated.State = StateEnded
	updated.EndReason = callpb.CallEndReason_REJECTED
	updated.EndMessage = payload.GetMessage()
	updated.EndedAt = &now
	operation := operationKey(ctx)
	events, err := s.terminalPendingEvents(ctx, operation, request.GetFromKafkaTopic(), request.GetUserId(), updated, callpb.CallEventType_CALL_REJECTED)
	if err != nil {
		return err
	}
	_, err = store.TransitionSessionWithEvents(ctx, expected, updated, true, operation, events)
	return err
}

func (s *Service) hangupAtomic(ctx context.Context, store AtomicStore, request *callpb.InternalRequest, callID string, reason callpb.CallEndReason) error {
	expected, err := s.store.GetSession(ctx, callID)
	if err != nil {
		return err
	}
	if _, err := expected.Peer(request.GetUserId()); err != nil {
		return err
	}
	if expected.State != StateRinging && expected.State != StateActive {
		return ErrInvalidState
	}
	updated := expected
	now := s.now().UTC()
	updated.State = StateEnded
	updated.EndReason = reason
	updated.EndMessage = ""
	updated.EndedAt = &now
	operation := operationKey(ctx)
	events, err := s.terminalPendingEvents(ctx, operation, request.GetFromKafkaTopic(), request.GetUserId(), updated, callpb.CallEventType_CALL_ENDED)
	if err != nil {
		return err
	}
	_, err = store.TransitionSessionWithEvents(ctx, expected, updated, true, operation, events)
	return err
}

func (s *Service) terminalPendingEvents(ctx context.Context, operation, requesterTopic string, requesterID int64, session Session, eventType callpb.CallEventType) ([]PendingEvent, error) {
	peerID, err := session.Peer(requesterID)
	if err != nil {
		return nil, err
	}
	requesterEvent := s.sessionEvent(eventType, session, peerID)
	requesterPending, err := pendingCallDelivery(operation, "requester-terminal", requesterTopic, requesterID, requesterEvent)
	if err != nil {
		return nil, err
	}
	events := []PendingEvent{requesterPending}
	peerTopic, routeErr := s.store.UserTopic(ctx, peerID)
	if routeErr != nil {
		if errors.Is(routeErr, ErrUserOffline) {
			return events, nil
		}
		return nil, routeErr
	}
	peerEvent := s.sessionEvent(eventType, session, requesterID)
	peerPending, err := pendingCallDelivery(operation, "peer-terminal", peerTopic, peerID, peerEvent)
	if err != nil {
		return nil, err
	}
	return append(events, peerPending), nil
}

func (s *Service) handlePushResultAtomic(ctx context.Context, store AtomicStore, result *pushpb.VoIPPushResult) error {
	expected, err := s.store.GetSession(ctx, result.GetCallId())
	if errors.Is(err, ErrCallNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected.CallerUserID != result.GetCallerUserId() || expected.CalleeUserID != result.GetCalleeUserId() || expected.State != StateRinging {
		return nil
	}
	updated := expected
	now := s.now().UTC()
	updated.State = StateEnded
	updated.EndReason = callpb.CallEndReason_CANCELLED
	updated.EndMessage = result.GetReason()
	updated.EndedAt = &now
	operation := operationKey(ctx)
	events := make([]PendingEvent, 0, 1)
	topic, routeErr := s.store.UserTopic(ctx, expected.CallerUserID)
	if routeErr != nil && !errors.Is(routeErr, ErrUserOffline) {
		return routeErr
	}
	if routeErr == nil {
		event := s.sessionEvent(callpb.CallEventType_CALL_ENDED, updated, expected.CalleeUserID)
		pending, err := pendingCallDelivery(operation, "push-failure-ended", topic, expected.CallerUserID, event)
		if err != nil {
			return err
		}
		events = append(events, pending)
	}
	_, err = store.TransitionSessionWithEvents(ctx, expected, updated, true, operation, events)
	return err
}

func pendingCallDelivery(operation, suffix, topic string, userID int64, event *callpb.CallEvent) (PendingEvent, error) {
	payload, err := mq.MarshalEnvelope(envelope.MessageType_CALL_RESPONSE, &callpb.Delivery{TargetUserId: userID, Event: event})
	if err != nil {
		return PendingEvent{}, err
	}
	return PendingEvent{EventID: callEventID(operation, suffix), OperationKey: operation, Topic: topic, Payload: payload}, nil
}

func pendingPushRequest(operation, suffix, topic string, request *pushpb.RequestMessage) (PendingEvent, error) {
	payload, err := mq.MarshalEnvelope(envelope.MessageType_PUSH_REQUEST, request)
	if err != nil {
		return PendingEvent{}, err
	}
	return PendingEvent{EventID: callEventID(operation, suffix), OperationKey: operation, Topic: topic, Payload: payload}, nil
}

func callEventID(operation, suffix string) string {
	digest := sha256.Sum256([]byte("betterfly-call-event:" + operation + ":" + suffix))
	return "call-" + hex.EncodeToString(digest[:])
}

func (s *Service) sessionEvent(eventType callpb.CallEventType, session Session, peerID int64) *callpb.CallEvent {
	return &callpb.CallEvent{
		EventType:  eventType,
		CallId:     session.ID,
		PeerUserId: peerID,
		CallType:   session.CallType,
		State:      stateToProto(session.State),
		EndReason:  session.EndReason,
		Message:    session.EndMessage,
		Timestamp:  timestamp(s.now()),
	}
}

func (s *Service) errorEvent(callID string, err error) *callpb.CallEvent {
	return &callpb.CallEvent{
		EventType: callpb.CallEventType_CALL_ERROR,
		CallId:    callID,
		ErrorCode: errorCode(err),
		Message:   err.Error(),
		Timestamp: timestamp(s.now()),
	}
}

func errorCode(err error) callpb.CallErrorCode {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return callpb.CallErrorCode_INVALID_ARGUMENT
	case errors.Is(err, ErrUserOffline):
		return callpb.CallErrorCode_USER_OFFLINE
	case errors.Is(err, ErrUserBusy):
		return callpb.CallErrorCode_USER_BUSY
	case errors.Is(err, ErrCallNotFound):
		return callpb.CallErrorCode_CALL_NOT_FOUND
	case errors.Is(err, ErrInvalidState):
		return callpb.CallErrorCode_INVALID_STATE
	case errors.Is(err, ErrForbidden):
		return callpb.CallErrorCode_FORBIDDEN
	default:
		return callpb.CallErrorCode_INTERNAL_ERROR
	}
}

func isCallDomainError(err error) bool {
	return errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrUserOffline) || errors.Is(err, ErrUserBusy) ||
		errors.Is(err, ErrCallNotFound) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrForbidden)
}

func validDescription(description *callpb.SessionDescription, expectedType string) bool {
	return description != nil && strings.EqualFold(strings.TrimSpace(description.GetType()), expectedType) && strings.TrimSpace(description.GetSdp()) != ""
}

func descriptionFromProto(description *callpb.SessionDescription) Description {
	return Description{Type: strings.ToLower(description.GetType()), SDP: description.GetSdp()}
}

func descriptionToProto(description Description) *callpb.SessionDescription {
	return &callpb.SessionDescription{Type: description.Type, Sdp: description.SDP}
}

func stateToProto(state string) callpb.CallState {
	switch state {
	case StateRinging:
		return callpb.CallState_RINGING
	case StateActive:
		return callpb.CallState_ACTIVE
	case StateEnded:
		return callpb.CallState_ENDED
	default:
		return callpb.CallState_CALL_STATE_UNSPECIFIED
	}
}

func callTypeName(callType callpb.CallType) string {
	if callType == callpb.CallType_VIDEO {
		return "video"
	}
	return "audio"
}

func requestCallID(request *callpb.ClientRequest) string {
	switch payload := request.Payload.(type) {
	case *callpb.ClientRequest_Accept:
		return payload.Accept.GetCallId()
	case *callpb.ClientRequest_Reject:
		return payload.Reject.GetCallId()
	case *callpb.ClientRequest_Hangup:
		return payload.Hangup.GetCallId()
	case *callpb.ClientRequest_IceCandidate:
		return payload.IceCandidate.GetCallId()
	case *callpb.ClientRequest_ResumeCall:
		return payload.ResumeCall.GetCallId()
	default:
		return ""
	}
}

func newCallID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("call-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func callIDForContext(ctx context.Context) string {
	operationKey, ok := kafkaconsumer.OperationKeyFromContext(ctx)
	if !ok || operationKey == "" {
		return newCallID()
	}
	digest := sha256.Sum256([]byte("betterfly-call:" + operationKey))
	return hex.EncodeToString(digest[:16])
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
