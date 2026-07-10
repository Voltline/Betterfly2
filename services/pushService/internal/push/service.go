package push

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/logger"
)

type Service struct {
	store     Store
	sender    Sender
	publisher Publisher
	bundleID  string
	now       func() time.Time
}

func NewService(store Store, sender Sender, publisher Publisher, bundleID string) *Service {
	return &Service{store: store, sender: sender, publisher: publisher, bundleID: strings.TrimSpace(bundleID), now: time.Now}
}

func (s *Service) Ready(ctx context.Context) error {
	if err := s.store.Ping(ctx); err != nil {
		return err
	}
	return s.sender.Ready()
}

func (s *Service) Handle(ctx context.Context, request *pushpb.RequestMessage) error {
	if request == nil {
		return ErrInvalidRequest
	}
	switch payload := request.Payload.(type) {
	case *pushpb.RequestMessage_ClientCommand:
		return s.handleClientCommand(ctx, payload.ClientCommand)
	case *pushpb.RequestMessage_VoipCall:
		return s.handleVoIPCall(ctx, payload.VoipCall)
	default:
		return ErrInvalidRequest
	}
}

func (s *Service) handleClientCommand(ctx context.Context, command *pushpb.ClientCommand) error {
	if command == nil || command.GetUserId() <= 0 || strings.TrimSpace(command.GetFromKafkaTopic()) == "" || command.GetRequest() == nil {
		return ErrInvalidRequest
	}
	var event *pushpb.ClientEvent
	switch payload := command.GetRequest().Payload.(type) {
	case *pushpb.ClientRequest_RegisterVoipToken:
		event = s.register(ctx, command.GetUserId(), payload.RegisterVoipToken)
	case *pushpb.ClientRequest_UnregisterVoipToken:
		event = s.unregister(ctx, command.GetUserId(), payload.UnregisterVoipToken)
	default:
		event = clientEvent("unknown", pushpb.PushResult_INVALID_ARGUMENT, "", pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED, ErrInvalidRequest.Error(), s.now())
	}
	return s.publisher.Publish(ctx, command.GetFromKafkaTopic(), &pushpb.ResponseMessage{
		Payload: &pushpb.ResponseMessage_ClientDelivery{ClientDelivery: &pushpb.ClientDelivery{
			TargetUserId: command.GetUserId(), Event: event,
		}},
	})
}

func (s *Service) register(ctx context.Context, userID int64, request *pushpb.RegisterVoIPToken) *pushpb.ClientEvent {
	if request == nil || !validDeviceID(request.GetDeviceId()) || !validToken(request.GetToken()) || !validEnvironment(request.GetEnvironment()) {
		return clientEvent("register_voip_token", pushpb.PushResult_INVALID_ARGUMENT, valueOrEmpty(request, func(v *pushpb.RegisterVoIPToken) string { return v.GetDeviceId() }), environmentOrUnknown(request), ErrInvalidRequest.Error(), s.now())
	}
	deviceID := strings.TrimSpace(request.GetDeviceId())
	token := strings.ToLower(strings.TrimSpace(request.GetToken()))
	environment := environmentName(request.GetEnvironment())
	if err := s.store.RegisterVoIPToken(ctx, userID, deviceID, token, environment, s.bundleID); err != nil {
		logger.Sugar().Errorf("注册VoIP token失败: user_id=%d device_id=%s error=%v", userID, deviceID, err)
		return clientEvent("register_voip_token", pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, request.GetEnvironment(), "register token failed", s.now())
	}
	return clientEvent("register_voip_token", pushpb.PushResult_PUSH_OK, deviceID, request.GetEnvironment(), "registered", s.now())
}

func (s *Service) unregister(ctx context.Context, userID int64, request *pushpb.UnregisterVoIPToken) *pushpb.ClientEvent {
	if request == nil || !validDeviceID(request.GetDeviceId()) || !validEnvironment(request.GetEnvironment()) {
		return clientEvent("unregister_voip_token", pushpb.PushResult_INVALID_ARGUMENT, valueOrEmpty(request, func(v *pushpb.UnregisterVoIPToken) string { return v.GetDeviceId() }), environmentOrUnknownUnregister(request), ErrInvalidRequest.Error(), s.now())
	}
	deviceID := strings.TrimSpace(request.GetDeviceId())
	found, err := s.store.UnregisterVoIPToken(ctx, userID, deviceID, environmentName(request.GetEnvironment()))
	if err != nil {
		logger.Sugar().Errorf("删除VoIP token失败: user_id=%d device_id=%s error=%v", userID, deviceID, err)
		return clientEvent("unregister_voip_token", pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, request.GetEnvironment(), "unregister token failed", s.now())
	}
	if !found {
		return clientEvent("unregister_voip_token", pushpb.PushResult_TOKEN_NOT_FOUND, deviceID, request.GetEnvironment(), ErrTokenNotFound.Error(), s.now())
	}
	return clientEvent("unregister_voip_token", pushpb.PushResult_PUSH_OK, deviceID, request.GetEnvironment(), "unregistered", s.now())
}

func (s *Service) handleVoIPCall(ctx context.Context, request *pushpb.VoIPCallRequest) error {
	if request == nil || strings.TrimSpace(request.GetCallId()) == "" || request.GetCallerUserId() <= 0 || request.GetCalleeUserId() <= 0 || request.GetCallerUserId() == request.GetCalleeUserId() || strings.TrimSpace(request.GetResultKafkaTopic()) == "" {
		return ErrInvalidRequest
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, request.GetExpiresAt())
	if err != nil || !expiresAt.After(s.now()) {
		return s.publishVoIPResult(ctx, request, false, "call_expired")
	}
	tokens, err := s.store.ListActiveVoIPTokens(ctx, request.GetCalleeUserId())
	if err != nil {
		return s.publishVoIPResult(ctx, request, false, "token_query_failed")
	}
	if len(tokens) == 0 {
		return s.publishVoIPResult(ctx, request, false, "no_active_voip_token")
	}

	accepted := false
	lastReason := "apns_delivery_failed"
	for _, token := range tokens {
		environment := parseEnvironment(token.Environment)
		_, sendErr := s.sender.Send(ctx, Notification{
			Token: token.Token, Environment: environment, CallID: request.GetCallId(),
			CallerUserID: request.GetCallerUserId(), CalleeUserID: request.GetCalleeUserId(),
			CallType: request.GetCallType(), ExpiresAt: expiresAt,
		})
		if sendErr == nil {
			accepted = true
			continue
		}
		lastReason = sendErr.Error()
		var apnsErr *APNSError
		if errors.As(sendErr, &apnsErr) && apnsErr.InvalidatesToken() {
			if deactivateErr := s.store.DeactivateToken(ctx, token.ID); deactivateErr != nil {
				logger.Sugar().Warnf("停用无效APNs token失败: token_id=%d error=%v", token.ID, deactivateErr)
			}
		}
		logger.Sugar().Warnf("VoIP Push发送失败: call_id=%s token_id=%d environment=%s error=%v", request.GetCallId(), token.ID, token.Environment, sendErr)
	}
	if accepted {
		lastReason = "accepted_by_apns"
	}
	return s.publishVoIPResult(ctx, request, accepted, lastReason)
}

func (s *Service) publishVoIPResult(ctx context.Context, request *pushpb.VoIPCallRequest, accepted bool, reason string) error {
	return s.publisher.Publish(ctx, request.GetResultKafkaTopic(), &pushpb.ResponseMessage{
		Payload: &pushpb.ResponseMessage_VoipResult{VoipResult: &pushpb.VoIPPushResult{
			CallId: request.GetCallId(), CallerUserId: request.GetCallerUserId(), CalleeUserId: request.GetCalleeUserId(),
			Accepted: accepted, Reason: reason, Timestamp: s.now().UTC().Format(time.RFC3339Nano), Required: request.GetRequired(),
		}},
	})
}

func validToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) < 32 || len(token) > 256 || len(token)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func validDeviceID(deviceID string) bool {
	length := len(strings.TrimSpace(deviceID))
	return length > 0 && length <= 128
}

func validEnvironment(environment pushpb.PushEnvironment) bool {
	return environment == pushpb.PushEnvironment_SANDBOX || environment == pushpb.PushEnvironment_PRODUCTION
}

func environmentName(environment pushpb.PushEnvironment) string {
	if environment == pushpb.PushEnvironment_PRODUCTION {
		return "production"
	}
	return "sandbox"
}

func parseEnvironment(environment string) pushpb.PushEnvironment {
	if strings.EqualFold(environment, "production") {
		return pushpb.PushEnvironment_PRODUCTION
	}
	return pushpb.PushEnvironment_SANDBOX
}

func clientEvent(operation string, result pushpb.PushResult, deviceID string, environment pushpb.PushEnvironment, message string, now time.Time) *pushpb.ClientEvent {
	return &pushpb.ClientEvent{Operation: operation, Result: result, DeviceId: deviceID, Environment: environment, Message: message, Timestamp: now.UTC().Format(time.RFC3339Nano)}
}

func valueOrEmpty[T any](value *T, getter func(*T) string) string {
	if value == nil {
		return ""
	}
	return getter(value)
}

func environmentOrUnknown(value *pushpb.RegisterVoIPToken) pushpb.PushEnvironment {
	if value == nil {
		return pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED
	}
	return value.GetEnvironment()
}

func environmentOrUnknownUnregister(value *pushpb.UnregisterVoIPToken) pushpb.PushEnvironment {
	if value == nil {
		return pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED
	}
	return value.GetEnvironment()
}
