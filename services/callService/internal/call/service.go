package call

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
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
		ID:           newCallID(),
		CallerUserID: request.GetUserId(),
		CalleeUserID: payload.GetCalleeUserId(),
		CallType:     payload.GetCallType(),
		State:        StateRinging,
		Offer:        descriptionFromProto(payload.GetOffer()),
		CreatedAt:    now,
		RingDeadline: now.Add(s.ringTTL),
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return err
	}

	callerEvent := s.sessionEvent(callpb.CallEventType_OUTGOING_CALL, session, session.CalleeUserID)
	callerEvent.SessionDescription = descriptionToProto(session.Offer)
	callerEvent.IceServers = s.ice.Servers(session.CallerUserID, now)
	if err := s.publishToTopic(ctx, request.GetFromKafkaTopic(), session.CallerUserID, callerEvent); err != nil {
		logger.Sugar().Warnf("主叫事件投递失败，取消通话: call_id=%s error=%v", session.ID, err)
		_, _ = s.store.EndSession(ctx, session.ID, session.CallerUserID, callpb.CallEndReason_CANCELLED, "caller delivery failed")
		return nil
	}

	if calleeOnline {
		calleeEvent := s.incomingEvent(session, now)
		if err := s.publishToTopic(ctx, calleeTopic, session.CalleeUserID, calleeEvent); err != nil {
			logger.Sugar().Warnf("来电事件投递失败，取消通话: call_id=%s error=%v", session.ID, err)
			ended, endErr := s.store.EndSession(ctx, session.ID, session.CallerUserID, callpb.CallEndReason_CANCELLED, "callee delivery failed")
			if endErr == nil {
				s.publishBestEffortToTopic(ctx, request.GetFromKafkaTopic(), session.CallerUserID, s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CalleeUserID))
			}
			return nil
		}
	}

	pushRequest := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{
		CallId: session.ID, CallerUserId: session.CallerUserID, CalleeUserId: session.CalleeUserID,
		CallType: callTypeName(session.CallType), ResultKafkaTopic: "call-service",
		ExpiresAt: session.RingDeadline.UTC().Format(time.RFC3339Nano), Required: !calleeOnline,
	}}}
	if err := s.publisher.PublishPush(ctx, "push-service", pushRequest); err != nil {
		if calleeOnline {
			logger.Sugar().Warnf("在线通话的冗余VoIP Push请求失败: call_id=%s error=%v", session.ID, err)
			return nil
		}
		logger.Sugar().Warnf("发布VoIP Push请求失败，取消通话: call_id=%s error=%v", session.ID, err)
		ended, endErr := s.store.EndSession(ctx, session.ID, session.CallerUserID, callpb.CallEndReason_CANCELLED, "push request failed")
		if endErr == nil {
			s.publishBestEffortToTopic(ctx, request.GetFromKafkaTopic(), session.CallerUserID, s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CalleeUserID))
		}
	}
	return nil
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
	s.publishBestEffortToUser(ctx, session.CallerUserID, s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CalleeUserID))
	return nil
}

func (s *Service) accept(ctx context.Context, request *callpb.InternalRequest, payload *callpb.AcceptCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" || !validDescription(payload.GetAnswer(), "answer") {
		return ErrInvalidInput
	}
	session, err := s.store.AcceptSession(ctx, payload.GetCallId(), request.GetUserId(), descriptionFromProto(payload.GetAnswer()))
	if err != nil {
		return err
	}

	calleeEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, session, session.CallerUserID)
	s.publishBestEffortToTopic(ctx, request.GetFromKafkaTopic(), session.CalleeUserID, calleeEvent)
	callerEvent := s.sessionEvent(callpb.CallEventType_CALL_ACCEPTED, session, session.CalleeUserID)
	callerEvent.SessionDescription = descriptionToProto(*session.Answer)
	if err := s.publishToUser(ctx, session.CallerUserID, "", callerEvent); err != nil {
		logger.Sugar().Warnf("接听后主叫已不可达，结束通话: call_id=%s error=%v", session.ID, err)
		ended, endErr := s.store.EndSession(ctx, session.ID, session.CalleeUserID, callpb.CallEndReason_DISCONNECTED, "caller unavailable after accept")
		if endErr == nil {
			s.publishBestEffortToTopic(ctx, request.GetFromKafkaTopic(), session.CalleeUserID, s.sessionEvent(callpb.CallEventType_CALL_ENDED, ended, session.CallerUserID))
		}
	}
	return nil
}

func (s *Service) reject(ctx context.Context, request *callpb.InternalRequest, payload *callpb.RejectCall) error {
	if payload == nil || strings.TrimSpace(payload.GetCallId()) == "" {
		return ErrInvalidInput
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
	return s.publishToUser(ctx, peerID, "", event)
}

func (s *Service) publishTerminal(ctx context.Context, requesterTopic string, requesterID int64, session Session, eventType callpb.CallEventType) error {
	peerID, err := session.Peer(requesterID)
	if err != nil {
		return err
	}
	requesterEvent := s.sessionEvent(eventType, session, peerID)
	s.publishBestEffortToTopic(ctx, requesterTopic, requesterID, requesterEvent)
	peerEvent := s.sessionEvent(eventType, session, requesterID)
	s.publishBestEffortToUser(ctx, peerID, peerEvent)
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
	return s.publisher.Publish(ctx, topic, &callpb.Delivery{TargetUserId: userID, Event: event})
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

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
