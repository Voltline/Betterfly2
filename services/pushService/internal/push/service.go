package push

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
)

type Service struct {
	store          Store
	sender         Sender
	publisher      Publisher
	bundleID       string
	now            func() time.Time
	maxConcurrency int
	deliveryLease  time.Duration
	workerBatch    int
	workerPoll     time.Duration
	sendTimeout    time.Duration
	maxAttempts    int
	retryInitial   time.Duration
	retryMax       time.Duration
}

type deliveryClaimPendingError struct{ retryAfter time.Duration }

func (e *deliveryClaimPendingError) Error() string             { return "APNs delivery claim is still leased" }
func (e *deliveryClaimPendingError) RetryAfter() time.Duration { return e.retryAfter }

func NewService(store Store, sender Sender, publisher Publisher, bundleID string) *Service {
	service := &Service{
		store: store, sender: sender, publisher: publisher, bundleID: strings.TrimSpace(bundleID), now: time.Now,
		maxConcurrency: envPositiveInt("PUSH_APNS_MAX_CONCURRENCY", 16),
		deliveryLease:  envPositiveDuration("PUSH_DELIVERY_LEASE", 30*time.Second),
		workerBatch:    envPositiveInt("PUSH_WORKER_BATCH_SIZE", 256),
		workerPoll:     envPositiveDuration("PUSH_WORKER_POLL_INTERVAL", 250*time.Millisecond),
		sendTimeout:    envPositiveDuration("PUSH_APNS_SEND_TIMEOUT", 10*time.Second),
		maxAttempts:    envPositiveInt("PUSH_DELIVERY_MAX_ATTEMPTS", 10),
		retryInitial:   envPositiveDuration("PUSH_DELIVERY_RETRY_INITIAL_BACKOFF", time.Second),
		retryMax:       envPositiveDuration("PUSH_DELIVERY_RETRY_MAX_BACKOFF", 15*time.Minute),
	}
	if service.sendTimeout >= service.deliveryLease {
		service.sendTimeout = service.deliveryLease / 2
	}
	if service.retryInitial > service.retryMax {
		service.retryInitial = service.retryMax
	}
	return service
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
	if operationKey, ok := kafkaconsumer.OperationKeyFromContext(ctx); ok {
		if durable, supported := s.store.(DurableStore); supported {
			return durable.EnqueueRequest(ctx, operationKey, request, s.bundleID)
		}
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
	logger.Sugar().Infof("收到VoIP Push请求: call_id=%s caller=%d callee=%d required=%t expires_at=%s", request.GetCallId(), request.GetCallerUserId(), request.GetCalleeUserId(), request.GetRequired(), request.GetExpiresAt())
	expiresAt, err := time.Parse(time.RFC3339Nano, request.GetExpiresAt())
	if err != nil || !expiresAt.After(s.now()) {
		return s.publishVoIPResult(ctx, request, false, "call_expired")
	}
	tokens, err := s.store.ListActiveTokens(ctx, request.GetCalleeUserId(), PushTypeVoIP)
	if err != nil {
		return s.publishVoIPResult(ctx, request, false, "token_query_failed")
	}
	if len(tokens) == 0 {
		logger.Sugar().Warnf("VoIP Push无可用token: call_id=%s callee=%d", request.GetCallId(), request.GetCalleeUserId())
		return s.publishVoIPResult(ctx, request, false, "no_active_voip_token")
	}
	logger.Sugar().Debugf("VoIP Push查询到活跃token: call_id=%s count=%d", request.GetCallId(), len(tokens))

	accepted := false
	lastReason := "apns_delivery_failed"
	for _, token := range tokens {
		environment := parseEnvironment(token.Environment)
		sendResult, sendErr := s.sender.Send(ctx, Notification{
			Kind: NotificationVoIP, Token: token.Token, Environment: environment, CallID: request.GetCallId(),
			CallerUserID: request.GetCallerUserId(), CalleeUserID: request.GetCalleeUserId(),
			CallType: request.GetCallType(), ExpiresAt: expiresAt,
		})
		if sendErr == nil {
			accepted = true
			logger.Sugar().Infof(
				"APNs已接受VoIP Push: call_id=%s token_id=%d device_id=%s environment=%s apns_id=%s",
				request.GetCallId(), token.ID, token.DeviceID, token.Environment, sendResult.APNSID,
			)
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
	logger.Sugar().Infof("VoIP Push处理完成: call_id=%s accepted=%t reason=%s", request.GetCallId(), accepted, lastReason)
	return s.publishVoIPResult(ctx, request, accepted, lastReason)
}

func (s *Service) handleMessagePush(ctx context.Context, request *pushpb.MessagePushRequest) error {
	if request == nil || request.GetMessageId() <= 0 || request.GetSenderUserId() <= 0 || request.GetConversationId() <= 0 || strings.TrimSpace(request.GetMessageType()) == "" || len(request.GetTargetUserIds()) == 0 {
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
	targetUserIDs := uniquePushTargets(request.GetTargetUserIds(), request.GetSenderUserId())
	tokens, err := s.store.ListMessageTokens(ctx, targetUserIDs, request.GetSenderUserId(), request.GetIsGroup())
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}

	selected := tokens
	pendingClaim := false
	claimAttempts := make(map[int64]int, len(tokens))
	if request.GetMessageId() > 0 {
		tokenIDs := make([]int64, 0, len(tokens))
		for _, token := range tokens {
			tokenIDs = append(tokenIDs, token.ID)
		}
		claimAttempts, pendingClaim, err = s.store.ClaimMessageDeliveries(ctx, request.GetMessageId(), tokenIDs, s.now(), s.deliveryLease)
		if err != nil {
			return err
		}
		selected = selected[:0]
		for _, token := range tokens {
			if _, claimed := claimAttempts[token.ID]; claimed {
				selected = append(selected, token)
			}
		}
	}
	metrics.RecordPushBatchSize(len(selected))
	if len(selected) == 0 {
		if pendingClaim {
			return &deliveryClaimPendingError{retryAfter: s.deliveryLease}
		}
		return nil
	}

	type deliveryJob struct {
		token    db.PushDeviceToken
		queuedAt time.Time
	}
	workerCount := s.maxConcurrency
	if workerCount > len(selected) {
		workerCount = len(selected)
	}
	jobs := make(chan deliveryJob, workerCount)
	results := make(chan deliveryResult, len(selected))
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				metrics.RecordPushQueueDelay(time.Since(job.queuedAt))
				start := time.Now()
				sendResult, sendErr := s.sender.Send(ctx, Notification{
					Kind: NotificationMessage, Token: job.token.Token, Environment: parseEnvironment(job.token.Environment),
					SenderUserID: request.GetSenderUserId(), TargetUserID: job.token.UserID,
					ConversationID: request.GetConversationId(), IsGroup: request.GetIsGroup(),
					MessageType: strings.TrimSpace(request.GetMessageType()), SentAt: sentAt,
					MessageID: request.GetMessageId(), ExpiresAt: sentAt.Add(24 * time.Hour),
					Title: presentation.Title, Body: body, SenderName: presentation.SenderName, SenderAvatar: presentation.SenderAvatar,
					GroupName: presentation.GroupName, Avatar: presentation.Avatar, AvatarIsGroup: presentation.AvatarIsGroup,
					ConversationName: presentation.ConversationName, ConversationAvatar: presentation.ConversationAvatar,
				})
				metrics.RecordPushAPNSLatency(start)
				result := classifyMessageDelivery(request.GetMessageId(), job.token.ID, sendResult, sendErr, s.now())
				results <- result
			}
		}()
	}
	for _, token := range selected {
		select {
		case jobs <- deliveryJob{token: token, queuedAt: time.Now()}:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	workers.Wait()
	close(results)

	updates := make([]DeliveryUpdate, 0, len(selected))
	retryableCount := 0
	for result := range results {
		updates = append(updates, result.update)
		if result.retryable {
			retryableCount++
		}
	}
	if request.GetMessageId() > 0 {
		if err := s.store.FinalizeMessageDeliveries(ctx, updates); err != nil {
			return err
		}
	}
	if pendingClaim {
		return &deliveryClaimPendingError{retryAfter: s.deliveryLease}
	}
	if retryableCount > 0 {
		return fmt.Errorf("%d APNs deliveries remain retryable", retryableCount)
	}
	return nil
}

func uniquePushTargets(values []int64, senderUserID int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 || value == senderUserID {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func classifyMessageDelivery(messageID, tokenID int64, sendResult SendResult, sendErr error, now time.Time) deliveryResult {
	update := DeliveryUpdate{MessageID: messageID, TokenID: tokenID, APNSID: sendResult.APNSID}
	if sendErr == nil {
		update.Status = DeliverySent
		metrics.RecordPushDelivery(DeliverySent)
		return deliveryResult{update: update}
	}
	update.LastError = sanitizedPushError(sendErr)
	var apnsErr *APNSError
	if errors.As(sendErr, &apnsErr) && apnsErr.InvalidatesToken() {
		update.Status = DeliveryPermanent
		update.APNSID = apnsErr.APNSID
		update.DeactivateToken = true
		metrics.RecordPushDelivery(DeliveryPermanent)
		return deliveryResult{update: update}
	}
	if errors.As(sendErr, &apnsErr) && !apnsErr.Retryable() || errors.Is(sendErr, ErrInvalidRequest) {
		update.Status = DeliveryPermanent
		metrics.RecordPushDelivery(DeliveryPermanent)
		return deliveryResult{update: update}
	}
	update.Status = DeliveryRetryable
	update.NextRetryAt = now.UTC()
	metrics.RecordPushDelivery(DeliveryRetryable)
	return deliveryResult{update: update, retryable: true}
}

type deliveryResult struct {
	update    DeliveryUpdate
	retryable bool
}

func sanitizedPushError(err error) string {
	var apnsErr *APNSError
	if errors.As(err, &apnsErr) {
		value := fmt.Sprintf("apns_status=%d reason=%s", apnsErr.StatusCode, apnsErr.Reason)
		if len(value) > 255 {
			return value[:255]
		}
		return value
	}
	return "network_or_sender_error"
}

func envPositiveInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envPositiveDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
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
