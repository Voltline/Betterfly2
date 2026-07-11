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
	case *pushpb.RequestMessage_MessagePush:
		return s.handleMessagePush(ctx, payload.MessagePush)
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
		request := payload.RegisterVoipToken
		event = s.registerToken(ctx, command.GetUserId(), valueOrEmpty(request, func(v *pushpb.RegisterVoIPToken) string { return v.GetDeviceId() }), valueOrEmpty(request, func(v *pushpb.RegisterVoIPToken) string { return v.GetToken() }), environmentOrUnknown(request), PushTypeVoIP, "register_voip_token")
	case *pushpb.ClientRequest_UnregisterVoipToken:
		request := payload.UnregisterVoipToken
		event = s.unregisterToken(ctx, command.GetUserId(), valueOrEmpty(request, func(v *pushpb.UnregisterVoIPToken) string { return v.GetDeviceId() }), environmentOrUnknownUnregister(request), PushTypeVoIP, "unregister_voip_token")
	case *pushpb.ClientRequest_RegisterApnsToken:
		request := payload.RegisterApnsToken
		event = s.registerToken(ctx, command.GetUserId(), valueOrEmpty(request, func(v *pushpb.RegisterAPNsToken) string { return v.GetDeviceId() }), valueOrEmpty(request, func(v *pushpb.RegisterAPNsToken) string { return v.GetToken() }), environmentOrUnknownAPNs(request), PushTypeAPNs, "register_apns_token")
	case *pushpb.ClientRequest_UnregisterApnsToken:
		request := payload.UnregisterApnsToken
		event = s.unregisterToken(ctx, command.GetUserId(), valueOrEmpty(request, func(v *pushpb.UnregisterAPNsToken) string { return v.GetDeviceId() }), environmentOrUnknownUnregisterAPNs(request), PushTypeAPNs, "unregister_apns_token")
	default:
		event = clientEvent("unknown", pushpb.PushResult_INVALID_ARGUMENT, "", pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED, ErrInvalidRequest.Error(), s.now())
	}
	return s.publisher.Publish(ctx, command.GetFromKafkaTopic(), &pushpb.ResponseMessage{
		Payload: &pushpb.ResponseMessage_ClientDelivery{ClientDelivery: &pushpb.ClientDelivery{
			TargetUserId: command.GetUserId(), Event: event,
		}},
	})
}

func (s *Service) registerToken(ctx context.Context, userID int64, rawDeviceID, rawToken string, environment pushpb.PushEnvironment, pushType, operation string) *pushpb.ClientEvent {
	if !validDeviceID(rawDeviceID) || !validToken(rawToken) || !validEnvironment(environment) {
		return clientEvent(operation, pushpb.PushResult_INVALID_ARGUMENT, strings.TrimSpace(rawDeviceID), environment, ErrInvalidRequest.Error(), s.now())
	}
	deviceID := strings.TrimSpace(rawDeviceID)
	token := strings.ToLower(strings.TrimSpace(rawToken))
	if err := s.store.RegisterToken(ctx, userID, deviceID, token, environmentName(environment), pushType, s.bundleID); err != nil {
		logger.Sugar().Errorf("注册APNs token失败: user_id=%d device_id=%s push_type=%s error=%v", userID, deviceID, pushType, err)
		return clientEvent(operation, pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, environment, "register token failed", s.now())
	}
	return clientEvent(operation, pushpb.PushResult_PUSH_OK, deviceID, environment, "registered", s.now())
}

func (s *Service) unregisterToken(ctx context.Context, userID int64, rawDeviceID string, environment pushpb.PushEnvironment, pushType, operation string) *pushpb.ClientEvent {
	if !validDeviceID(rawDeviceID) || !validEnvironment(environment) {
		return clientEvent(operation, pushpb.PushResult_INVALID_ARGUMENT, strings.TrimSpace(rawDeviceID), environment, ErrInvalidRequest.Error(), s.now())
	}
	deviceID := strings.TrimSpace(rawDeviceID)
	found, err := s.store.UnregisterToken(ctx, userID, deviceID, environmentName(environment), pushType)
	if err != nil {
		logger.Sugar().Errorf("删除APNs token失败: user_id=%d device_id=%s push_type=%s error=%v", userID, deviceID, pushType, err)
		return clientEvent(operation, pushpb.PushResult_PUSH_INTERNAL_ERROR, deviceID, environment, "unregister token failed", s.now())
	}
	if !found {
		return clientEvent(operation, pushpb.PushResult_TOKEN_NOT_FOUND, deviceID, environment, ErrTokenNotFound.Error(), s.now())
	}
	return clientEvent(operation, pushpb.PushResult_PUSH_OK, deviceID, environment, "unregistered", s.now())
}

func (s *Service) handleVoIPCall(ctx context.Context, request *pushpb.VoIPCallRequest) error {
	if request == nil || strings.TrimSpace(request.GetCallId()) == "" || request.GetCallerUserId() <= 0 || request.GetCalleeUserId() <= 0 || request.GetCallerUserId() == request.GetCalleeUserId() || strings.TrimSpace(request.GetResultKafkaTopic()) == "" {
		return ErrInvalidRequest
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, request.GetExpiresAt())
	if err != nil || !expiresAt.After(s.now()) {
		return s.publishVoIPResult(ctx, request, false, "call_expired")
	}
	tokens, err := s.store.ListActiveTokens(ctx, request.GetCalleeUserId(), PushTypeVoIP)
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
			Kind: NotificationVoIP, Token: token.Token, Environment: environment, CallID: request.GetCallId(),
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

func (s *Service) handleMessagePush(ctx context.Context, request *pushpb.MessagePushRequest) error {
	if request == nil || request.GetSenderUserId() <= 0 || request.GetConversationId() <= 0 || strings.TrimSpace(request.GetMessageType()) == "" || len(request.GetTargetUserIds()) == 0 {
		return ErrInvalidRequest
	}
	sentAt, err := time.Parse(time.RFC3339Nano, request.GetSentAt())
	if err != nil {
		sentAt = s.now().UTC()
	}
	presentation, err := s.store.MessagePresentation(ctx, request.GetSenderUserId(), request.GetConversationId(), request.GetIsGroup())
	if err != nil {
		return err
	}
	preview := strings.TrimSpace(request.GetPreview())
	if preview == "" {
		preview = defaultMessagePreview(request.GetMessageType())
	}
	body := preview
	if request.GetIsGroup() && strings.TrimSpace(presentation.SenderName) != "" {
		body = presentation.SenderName + "：" + preview
	}
	seen := make(map[int64]struct{}, len(request.GetTargetUserIds()))
	for _, targetUserID := range request.GetTargetUserIds() {
		if targetUserID <= 0 || targetUserID == request.GetSenderUserId() {
			continue
		}
		if _, exists := seen[targetUserID]; exists {
			continue
		}
		seen[targetUserID] = struct{}{}
		enabled, policyErr := s.store.MessageNotificationsEnabled(ctx, targetUserID, request.GetSenderUserId(), request.GetIsGroup())
		if policyErr != nil {
			return policyErr
		}
		if !enabled {
			continue
		}
		tokens, queryErr := s.store.ListActiveTokens(ctx, targetUserID, PushTypeAPNs)
		if queryErr != nil {
			return queryErr
		}
		for _, token := range tokens {
			_, sendErr := s.sender.Send(ctx, Notification{
				Kind: NotificationMessage, Token: token.Token, Environment: parseEnvironment(token.Environment),
				SenderUserID: request.GetSenderUserId(), TargetUserID: targetUserID,
				ConversationID: request.GetConversationId(), IsGroup: request.GetIsGroup(),
				MessageType: strings.TrimSpace(request.GetMessageType()), SentAt: sentAt,
				ExpiresAt: sentAt.Add(24 * time.Hour),
				Title:     presentation.Title, Body: body, SenderName: presentation.SenderName,
				GroupName: presentation.GroupName, Avatar: presentation.Avatar, AvatarIsGroup: presentation.AvatarIsGroup,
			})
			if sendErr == nil {
				continue
			}
			var apnsErr *APNSError
			if errors.As(sendErr, &apnsErr) && apnsErr.InvalidatesToken() {
				if deactivateErr := s.store.DeactivateToken(ctx, token.ID); deactivateErr != nil {
					logger.Sugar().Warnf("停用无效APNs token失败: token_id=%d error=%v", token.ID, deactivateErr)
				}
			}
			logger.Sugar().Warnf("消息APNs推送失败: sender_user_id=%d target_user_id=%d conversation_id=%d token_id=%d error=%v", request.GetSenderUserId(), targetUserID, request.GetConversationId(), token.ID, sendErr)
		}
	}
	return nil
}

func defaultMessagePreview(messageType string) string {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "image":
		return "发送了一张图片"
	case "gif":
		return "发送了一个 GIF"
	case "file":
		return "发送了一个文件"
	case "audio":
		return "发送了一条语音"
	case "video":
		return "发送了一段视频"
	default:
		return "发来一条消息"
	}
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

func environmentOrUnknownAPNs(value *pushpb.RegisterAPNsToken) pushpb.PushEnvironment {
	if value == nil {
		return pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED
	}
	return value.GetEnvironment()
}

func environmentOrUnknownUnregisterAPNs(value *pushpb.UnregisterAPNsToken) pushpb.PushEnvironment {
	if value == nil {
		return pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED
	}
	return value.GetEnvironment()
}
