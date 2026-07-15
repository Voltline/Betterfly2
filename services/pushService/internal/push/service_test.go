package push

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
)

type memoryStore struct {
	tokens              []db.PushDeviceToken
	deactivated         []int64
	messageDeliveries   map[string]memoryDelivery
	notify              map[int64]bool
	audits              []db.PushDebugAudit
	presentation        MessagePresentation
	messageTokenQueries int
}

type memoryDelivery struct {
	status  string
	attempt int
}

func (s *memoryStore) ClaimMessageDeliveries(_ context.Context, messageID int64, tokenIDs []int64, _ time.Time, _ time.Duration) (map[int64]int, bool, error) {
	if s.messageDeliveries == nil {
		s.messageDeliveries = make(map[string]memoryDelivery)
	}
	claims := make(map[int64]int)
	pending := false
	for _, tokenID := range tokenIDs {
		key := fmt.Sprintf("%d:%d", messageID, tokenID)
		delivery, exists := s.messageDeliveries[key]
		if !exists {
			delivery = memoryDelivery{status: DeliveryClaimed, attempt: 1}
			s.messageDeliveries[key] = delivery
			claims[tokenID] = delivery.attempt
			continue
		}
		if delivery.status == DeliveryRetryable {
			delivery.status = DeliveryClaimed
			delivery.attempt++
			s.messageDeliveries[key] = delivery
			claims[tokenID] = delivery.attempt
		} else if delivery.status == DeliveryClaimed {
			pending = true
		}
	}
	return claims, pending, nil
}

func (s *memoryStore) FinalizeMessageDeliveries(ctx context.Context, updates []DeliveryUpdate) error {
	for _, update := range updates {
		key := fmt.Sprintf("%d:%d", update.MessageID, update.TokenID)
		delivery := s.messageDeliveries[key]
		delivery.status = update.Status
		s.messageDeliveries[key] = delivery
		if update.DeactivateToken {
			_ = s.DeactivateToken(ctx, update.TokenID)
		}
	}
	return nil
}

func (s *memoryStore) ListMessageTokens(_ context.Context, targetUserIDs []int64, senderUserID int64, isGroup bool) ([]db.PushDeviceToken, error) {
	s.messageTokenQueries++
	targets := make(map[int64]struct{}, len(targetUserIDs))
	for _, userID := range targetUserIDs {
		targets[userID] = struct{}{}
	}
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if _, selected := targets[token.UserID]; !selected || token.PushType != PushTypeAPNs || !token.IsActive {
			continue
		}
		if !isGroup && s.notify != nil {
			if enabled, configured := s.notify[token.UserID]; configured && !enabled {
				continue
			}
		}
		result = append(result, token)
	}
	return result, nil
}

func (s *memoryStore) MessagePresentation(_ context.Context, senderUserID, conversationID int64, isGroup bool) (MessagePresentation, error) {
	if s.presentation.Title != "" {
		return s.presentation, nil
	}
	if isGroup {
		return MessagePresentation{Title: "测试群", SenderName: "测试用户", SenderAvatar: "user-avatar", GroupName: "测试群", Avatar: "group-avatar", AvatarIsGroup: true, ConversationName: "测试群", ConversationAvatar: "group-avatar"}, nil
	}
	return MessagePresentation{Title: "测试用户", SenderName: "测试用户", SenderAvatar: "user-avatar", Avatar: "user-avatar", ConversationName: "测试用户", ConversationAvatar: "user-avatar"}, nil
}

func (s *memoryStore) FindTokens(_ context.Context, filter TokenFilter) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if filter.UserID > 0 && token.UserID != filter.UserID || filter.PushType != "" && token.PushType != filter.PushType || filter.Environment != "" && token.Environment != filter.Environment || filter.ActiveOnly && !token.IsActive {
			continue
		}
		result = append(result, token)
	}
	return result, nil
}

func (s *memoryStore) BroadcastAudience(_ context.Context, environment string) (int64, int64, error) {
	var count, maxID int64
	for _, token := range s.tokens {
		if token.PushType != PushTypeAPNs || !token.IsActive || environment != "" && token.Environment != environment {
			continue
		}
		count++
		if token.ID > maxID {
			maxID = token.ID
		}
	}
	return count, maxID, nil
}

func (s *memoryStore) ListBroadcastTokens(_ context.Context, environment string, afterID, throughID int64, limit int) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.ID <= afterID || token.ID > throughID || token.PushType != PushTypeAPNs || !token.IsActive || environment != "" && token.Environment != environment {
			continue
		}
		result = append(result, token)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *memoryStore) GetToken(_ context.Context, id int64) (db.PushDeviceToken, error) {
	for _, token := range s.tokens {
		if token.ID == id {
			return token, nil
		}
	}
	return db.PushDeviceToken{}, errors.New("token not found")
}

func (s *memoryStore) CreateDebugAudit(_ context.Context, audit *db.PushDebugAudit) error {
	s.audits = append(s.audits, *audit)
	return nil
}

func (s *memoryStore) ListDebugAudits(_ context.Context, limit int) ([]db.PushDebugAudit, error) {
	if limit <= 0 || limit > len(s.audits) {
		limit = len(s.audits)
	}
	return s.audits[:limit], nil
}

func (s *memoryStore) TokenSummary(context.Context) (TokenSummary, error) {
	var summary TokenSummary
	for _, token := range s.tokens {
		summary.Total++
		if token.IsActive {
			summary.Active++
		}
		if token.PushType == PushTypeAPNs {
			summary.APNs++
		} else if token.PushType == PushTypeVoIP {
			summary.VoIP++
		}
		if token.Environment == "sandbox" {
			summary.Sandbox++
		} else if token.Environment == "production" {
			summary.Production++
		}
	}
	return summary, nil
}

func (s *memoryStore) MessageNotificationsEnabled(_ context.Context, targetUserID, _ int64, _ bool) (bool, error) {
	if s.notify == nil {
		return true, nil
	}
	enabled, configured := s.notify[targetUserID]
	if !configured {
		return true, nil
	}
	return enabled, nil
}

func (s *memoryStore) Ping(context.Context) error { return nil }
func (s *memoryStore) RegisterToken(_ context.Context, userID int64, deviceID, token, environment, pushType, bundleID string) error {
	s.tokens = append(s.tokens, db.PushDeviceToken{ID: int64(len(s.tokens) + 1), UserID: userID, DeviceID: deviceID, Token: token, Environment: environment, PushType: pushType, BundleID: bundleID, IsActive: true})
	return nil
}
func (s *memoryStore) UnregisterToken(_ context.Context, userID int64, deviceID, environment, pushType string) (bool, error) {
	for index, token := range s.tokens {
		if token.UserID == userID && token.DeviceID == deviceID && token.Environment == environment && token.PushType == pushType {
			s.tokens = append(s.tokens[:index], s.tokens[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (s *memoryStore) ListActiveTokens(_ context.Context, userID int64, pushType string) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.UserID == userID && token.PushType == pushType && token.IsActive {
			result = append(result, token)
		}
	}
	return result, nil
}

func TestRegisterStandardTokenAndDispatchMessagePush(t *testing.T) {
	store := &memoryStore{}
	sender := &memorySender{}
	publisher := &memoryPublisher{}
	service := NewService(store, sender, publisher, "com.Voltline.Betterfly2")
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	register := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_ClientCommand{ClientCommand: &pushpb.ClientCommand{
		FromKafkaTopic: "df-a", UserId: 2,
		Request: &pushpb.ClientRequest{Payload: &pushpb.ClientRequest_RegisterApnsToken{RegisterApnsToken: &pushpb.RegisterAPNsToken{
			DeviceId: "iphone", Token: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", Environment: pushpb.PushEnvironment_SANDBOX,
		}}},
	}}}
	if err := service.Handle(context.Background(), register); err != nil {
		t.Fatal(err)
	}
	if len(store.tokens) != 1 || store.tokens[0].PushType != PushTypeAPNs {
		t.Fatalf("standard APNs token was not registered separately: %+v", store.tokens)
	}

	message := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2, 2, 1}, SenderUserId: 1, ConversationId: 1,
		MessageType: "text", SentAt: now.Format(time.RFC3339Nano), MessageId: 101,
	}}}
	if err := service.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if err := service.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one persistent deduplicated message push, got %+v", sender.sent)
	}
	notification := sender.sent[0].notification
	if notification.Kind != NotificationMessage || notification.SenderUserID != 1 || notification.TargetUserID != 2 || notification.ConversationID != 1 {
		t.Fatalf("unexpected message notification: %+v", notification)
	}

	unregister := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_ClientCommand{ClientCommand: &pushpb.ClientCommand{
		FromKafkaTopic: "df-a", UserId: 2,
		Request: &pushpb.ClientRequest{Payload: &pushpb.ClientRequest_UnregisterApnsToken{UnregisterApnsToken: &pushpb.UnregisterAPNsToken{
			DeviceId: "iphone", Environment: pushpb.PushEnvironment_SANDBOX,
		}}},
	}}}
	if err := service.Handle(context.Background(), unregister); err != nil {
		t.Fatal(err)
	}
	if len(store.tokens) != 0 || publisher.responses[len(publisher.responses)-1].response.GetClientDelivery().GetEvent().GetResult() != pushpb.PushResult_PUSH_OK {
		t.Fatalf("standard APNs token was not unregistered: tokens=%+v responses=%+v", store.tokens, publisher.responses)
	}
}

func TestMessagePushBatchLookupDoesNotScaleWithUserCount(t *testing.T) {
	tokens := make([]db.PushDeviceToken, 0, 128)
	targets := make([]int64, 0, 128)
	for index := int64(2); index < 130; index++ {
		tokens = append(tokens, db.PushDeviceToken{ID: index, UserID: index, Token: fmt.Sprintf("token-%d", index), Environment: "production", PushType: PushTypeAPNs, IsActive: true})
		targets = append(targets, index)
	}
	store := &memoryStore{tokens: tokens}
	service := NewService(store, &memorySender{}, &memoryPublisher{}, "com.Voltline.Betterfly2")
	service.maxConcurrency = 8
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: targets, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 9001,
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if store.messageTokenQueries != 1 {
		t.Fatalf("batch target lookup used %d queries for %d users", store.messageTokenQueries, len(targets))
	}
}

func TestMessagePushHonorsConfiguredConcurrencyLimit(t *testing.T) {
	const limit = 4
	tokens := make([]db.PushDeviceToken, 0, 32)
	targets := make([]int64, 0, 32)
	for index := int64(2); index < 34; index++ {
		tokens = append(tokens, db.PushDeviceToken{ID: index, UserID: index, Token: fmt.Sprintf("token-%d", index), Environment: "production", PushType: PushTypeAPNs, IsActive: true})
		targets = append(targets, index)
	}
	sender := &memorySender{sendFunc: func(Notification) (SendResult, error) {
		time.Sleep(5 * time.Millisecond)
		return SendResult{APNSID: "accepted"}, nil
	}}
	service := NewService(&memoryStore{tokens: tokens}, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	service.maxConcurrency = limit
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: targets, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 9002,
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := sender.maxActive.Load(); got > limit || got < 2 {
		t.Fatalf("unexpected APNs concurrency: got=%d limit=%d", got, limit)
	}
}

func TestMessagePushPartialRetryDoesNotResendSuccessfulToken(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 1, UserID: 2, Token: "stable", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
		{ID: 2, UserID: 3, Token: "flaky", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
	}}
	var mu sync.Mutex
	attempts := map[string]int{}
	sender := &memorySender{sendFunc: func(notification Notification) (SendResult, error) {
		mu.Lock()
		attempts[notification.Token]++
		attempt := attempts[notification.Token]
		mu.Unlock()
		if notification.Token == "flaky" && attempt == 1 {
			return SendResult{}, errors.New("temporary network failure")
		}
		return SendResult{APNSID: "accepted-" + notification.Token}, nil
	}}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2, 3}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 9003,
	}}}
	if err := service.Handle(context.Background(), request); err == nil {
		t.Fatal("retryable partial failure was treated as committed success")
	}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatalf("partial retry failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts["stable"] != 1 || attempts["flaky"] != 2 {
		t.Fatalf("successful token was resent or retryable token was skipped: %+v", attempts)
	}
	if store.messageDeliveries["9003:1"].status != DeliverySent || store.messageDeliveries["9003:2"].status != DeliverySent {
		t.Fatalf("unexpected final delivery ledger: %+v", store.messageDeliveries)
	}
}

func TestMessagePushPendingClaimExposesLeaseRetryBoundary(t *testing.T) {
	store := &memoryStore{
		tokens:            []db.PushDeviceToken{{ID: 1, UserID: 2, Token: "leased", PushType: PushTypeAPNs, IsActive: true}},
		messageDeliveries: map[string]memoryDelivery{"9100:1": {status: DeliveryClaimed, attempt: 1}},
	}
	service := NewService(store, &memorySender{}, &memoryPublisher{}, "com.Voltline.Betterfly2")
	service.deliveryLease = 75 * time.Millisecond
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text", MessageId: 9100,
	}}}
	err := service.Handle(context.Background(), request)
	var retryAfter interface{ RetryAfter() time.Duration }
	if !errors.As(err, &retryAfter) || retryAfter.RetryAfter() != service.deliveryLease {
		t.Fatalf("pending claim did not preserve its lease boundary: err=%v", err)
	}
}

func TestMessagePushRequiresStableMessageID(t *testing.T) {
	service := NewService(&memoryStore{}, &memorySender{}, &memoryPublisher{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text",
	}}}
	if err := service.Handle(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("message push without id bypassed delivery ledger: %v", err)
	}
}

func (s *memoryStore) DeactivateToken(_ context.Context, id int64) error {
	s.deactivated = append(s.deactivated, id)
	for index := range s.tokens {
		if s.tokens[index].ID == id {
			s.tokens[index].IsActive = false
		}
	}
	return nil
}

func (s *memoryStore) DeactivateTokens(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		if err := s.DeactivateToken(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

type sentNotification struct{ notification Notification }
type memorySender struct {
	mu        sync.Mutex
	sent      []sentNotification
	err       error
	sendFunc  func(Notification) (SendResult, error)
	active    atomic.Int32
	maxActive atomic.Int32
}

func (s *memorySender) Ready() error { return nil }
func (s *memorySender) Send(_ context.Context, notification Notification) (SendResult, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		maximum := s.maxActive.Load()
		if active <= maximum || s.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	s.mu.Lock()
	s.sent = append(s.sent, sentNotification{notification: notification})
	s.mu.Unlock()
	if s.sendFunc != nil {
		return s.sendFunc(notification)
	}
	return SendResult{APNSID: "test"}, s.err
}

type publishedResponse struct {
	topic    string
	response *pushpb.ResponseMessage
}
type memoryPublisher struct{ responses []publishedResponse }

func (p *memoryPublisher) Publish(_ context.Context, topic string, response *pushpb.ResponseMessage) error {
	p.responses = append(p.responses, publishedResponse{topic: topic, response: response})
	return nil
}

func TestRegisterAndDispatchProductionVoIPPush(t *testing.T) {
	store := &memoryStore{}
	sender := &memorySender{}
	publisher := &memoryPublisher{}
	service := NewService(store, sender, publisher, "com.Voltline.Betterfly2")
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	register := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_ClientCommand{ClientCommand: &pushpb.ClientCommand{
		FromKafkaTopic: "df-a", UserId: 2,
		Request: &pushpb.ClientRequest{Payload: &pushpb.ClientRequest_RegisterVoipToken{RegisterVoipToken: &pushpb.RegisterVoIPToken{
			DeviceId: "iphone", Token: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", Environment: pushpb.PushEnvironment_PRODUCTION,
		}}},
	}}}
	if err := service.Handle(context.Background(), register); err != nil {
		t.Fatal(err)
	}
	if len(store.tokens) != 1 || store.tokens[0].Environment != "production" || publisher.responses[0].response.GetClientDelivery().GetEvent().GetResult() != pushpb.PushResult_PUSH_OK {
		t.Fatalf("unexpected registration result: tokens=%+v responses=%+v", store.tokens, publisher.responses)
	}

	call := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{
		CallId: "call-1", CallerUserId: 1, CalleeUserId: 2, CallType: "video", ResultKafkaTopic: "call-service", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	}}}
	if err := service.Handle(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].notification.Environment != pushpb.PushEnvironment_PRODUCTION {
		t.Fatalf("push used wrong environment: %+v", sender.sent)
	}
	result := publisher.responses[len(publisher.responses)-1]
	if result.topic != "call-service" || !result.response.GetVoipResult().GetAccepted() {
		t.Fatalf("unexpected VoIP push result: %+v", result)
	}
}

func TestNoTokenReturnsRejectedPushResult(t *testing.T) {
	publisher := &memoryPublisher{}
	service := NewService(&memoryStore{}, &memorySender{}, publisher, "com.Voltline.Betterfly2")
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_VoipCall{VoipCall: &pushpb.VoIPCallRequest{
		CallId: "call-2", CallerUserId: 1, CalleeUserId: 2, CallType: "audio", ResultKafkaTopic: "call-service", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result := publisher.responses[0].response.GetVoipResult()
	if result.GetAccepted() || result.GetReason() != "no_active_voip_token" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDirectMessagePushHonorsNotificationPreference(t *testing.T) {
	store := &memoryStore{
		tokens: []db.PushDeviceToken{{ID: 1, UserID: 2, PushType: PushTypeAPNs, IsActive: true}},
		notify: map[int64]bool{2: false},
	}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		MessageId: 9201, TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text",
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("disabled direct message notifications should not be sent: %+v", sender.sent)
	}
}

func TestBusinessGroupMessagePushUsesGroupPresentationAndPreview(t *testing.T) {
	store := &memoryStore{
		tokens:       []db.PushDeviceToken{{ID: 1, UserID: 2, Token: "001122", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true}},
		presentation: MessagePresentation{Title: "开发群", SenderName: "Alice", SenderAvatar: "alice-avatar-hash", GroupName: "开发群", Avatar: "group-avatar-hash", AvatarIsGroup: true, ConversationName: "开发群", ConversationAvatar: "group-avatar-hash"},
	}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		MessageId: 9202, TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 88, IsGroup: true,
		MessageType: "text", Preview: "今晚八点开会",
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one push, got %d", len(sender.sent))
	}
	notification := sender.sent[0].notification
	if notification.Title != "开发群" || notification.Body != "Alice：今晚八点开会" || notification.SenderAvatar != "alice-avatar-hash" || notification.ConversationName != "开发群" || notification.ConversationAvatar != "group-avatar-hash" || !notification.AvatarIsGroup {
		t.Fatalf("unexpected communication notification: %+v", notification)
	}
}

func TestBusinessDirectMessagePushUsesSenderPresentation(t *testing.T) {
	store := &memoryStore{
		tokens:       []db.PushDeviceToken{{ID: 1, UserID: 2, Token: "001122", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true}},
		presentation: MessagePresentation{Title: "Alice", SenderName: "Alice", SenderAvatar: "alice-avatar-hash", Avatar: "alice-avatar-hash", ConversationName: "Alice", ConversationAvatar: "alice-avatar-hash"},
	}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
		MessageId: 9203, TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1,
		MessageType: "text", Preview: "你好",
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	notification := sender.sent[0].notification
	if notification.Title != "Alice" || notification.Body != "你好" || notification.SenderAvatar != "alice-avatar-hash" || notification.ConversationName != "Alice" || notification.ConversationAvatar != "alice-avatar-hash" || notification.AvatarIsGroup {
		t.Fatalf("unexpected direct communication notification: %+v", notification)
	}
}

func TestAdminMessagePushReturnsDetailedReportAndAudit(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 10, UserID: 2, DeviceID: "iphone-a", Token: "00112233445566778899", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		{ID: 11, UserID: 2, DeviceID: "iphone-b", Token: "aabbccddeeff00112233", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
	}}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	report, err := service.AdminSendMessage(context.Background(), AdminMessageRequest{
		TargetUserIDs: []int64{2}, SenderUserID: 1, ConversationID: 1, MessageType: "text",
		Title: "调试", Body: "后台消息", CustomData: map[string]any{"scenario": "smoke"},
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != 2 || report.Failed != 0 || len(report.Results) != 2 || report.Results[0].TokenMasked == store.tokens[0].Token {
		t.Fatalf("unexpected admin report: %+v", report)
	}
	if len(store.audits) != 1 || store.audits[0].Operator != "tester" || store.audits[0].Status != "success" {
		t.Fatalf("unexpected audit: %+v", store.audits)
	}
	if sender.sent[0].notification.Title != "调试" || sender.sent[0].notification.CustomData["scenario"] != "smoke" {
		t.Fatalf("custom notification data was not forwarded: %+v", sender.sent[0])
	}
}

func TestAdminVoIPCanTargetRegisteredToken(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{{ID: 20, UserID: 2, DeviceID: "iphone", Token: "00112233445566778899", Environment: "production", PushType: PushTypeVoIP, IsActive: true}}}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	report, err := service.AdminSendVoIP(context.Background(), AdminVoIPRequest{TokenID: 20, CallerUserID: 1, CallType: "video"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != 1 || len(sender.sent) != 1 || sender.sent[0].notification.CallID == "" || sender.sent[0].notification.Kind != NotificationVoIP {
		t.Fatalf("unexpected VoIP debug push: report=%+v sent=%+v", report, sender.sent)
	}
}

func TestAdminBroadcastSendsAllMatchingAPNsTokens(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 1, UserID: 2, DeviceID: "sandbox-phone", Token: "token-1", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		{ID: 2, UserID: 3, DeviceID: "production-phone", Token: "token-2", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
		{ID: 3, UserID: 4, DeviceID: "voip-phone", Token: "token-3", Environment: "sandbox", PushType: PushTypeVoIP, IsActive: true},
		{ID: 4, UserID: 5, DeviceID: "inactive-phone", Token: "token-4", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: false},
	}}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	report, err := service.AdminSendBroadcast(context.Background(), AdminBroadcastRequest{
		CampaignID: "summer-2026", Title: "夏日活动", Body: "打开 Betterfly 查看详情", DeepLink: "betterfly://campaign/summer-2026",
		CustomData: map[string]any{"placement": "home"},
	}, "marketing")
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 2 || report.Accepted != 2 || report.Failed != 0 || len(sender.sent) != 2 {
		t.Fatalf("unexpected broadcast report: %+v sent=%d", report, len(sender.sent))
	}
	for _, sent := range sender.sent {
		if sent.notification.Kind != NotificationBroadcast || sent.notification.CampaignID != "summer-2026" || sent.notification.SenderUserID != 0 || sent.notification.ConversationID != 0 {
			t.Fatalf("broadcast was encoded as a chat notification: %+v", sent.notification)
		}
	}
	if len(store.audits) != 1 || store.audits[0].TargetSummary != "broadcast:all:summer-2026" {
		t.Fatalf("unexpected broadcast audit: %+v", store.audits)
	}
}

func TestAdminBroadcastDryRunAndEnvironmentFilter(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 1, UserID: 2, Token: "token-1", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		{ID: 2, UserID: 3, Token: "token-2", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
	}}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	report, err := service.AdminSendBroadcast(context.Background(), AdminBroadcastRequest{Title: "预览", Body: "不会发送", Environment: "sandbox", DryRun: true}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 1 || report.Accepted != 0 || report.CampaignID == "" || len(sender.sent) != 0 {
		t.Fatalf("unexpected dry-run report: %+v sent=%d", report, len(sender.sent))
	}
	if len(store.audits) != 1 || store.audits[0].Status != "dry_run" {
		t.Fatalf("dry-run audit was not recorded correctly: %+v", store.audits)
	}
}

func TestAdminBroadcastPaginatesAndBoundsDetailedResults(t *testing.T) {
	tokens := make([]db.PushDeviceToken, 0, broadcastPageSize+5)
	for index := 1; index <= broadcastPageSize+5; index++ {
		tokens = append(tokens, db.PushDeviceToken{
			ID: int64(index), UserID: int64(index), DeviceID: "device", Token: "token",
			Environment: "production", PushType: PushTypeAPNs, IsActive: true,
		})
	}
	store := &memoryStore{tokens: tokens}
	sender := &memorySender{}
	service := NewService(store, sender, &memoryPublisher{}, "com.Voltline.Betterfly2")
	report, err := service.AdminSendBroadcast(context.Background(), AdminBroadcastRequest{CampaignID: "all-users", Title: "通知", Body: "正文"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != len(tokens) || report.Accepted != len(tokens) || len(sender.sent) != len(tokens) {
		t.Fatalf("pagination skipped recipients: report=%+v sent=%d", report, len(sender.sent))
	}
	if len(report.Results) != maxBroadcastAuditResults || !report.ResultsTruncated {
		t.Fatalf("broadcast result details were not bounded: results=%d truncated=%v", len(report.Results), report.ResultsTruncated)
	}
}

func TestDefaultMessagePreviewCoversSupportedMediaTypes(t *testing.T) {
	tests := map[string]string{
		"image":  "发送了一张图片",
		"gif":    "发送了一个 GIF",
		"file":   "发送了一个文件",
		"audio":  "发送了一条语音",
		"video":  "发送了一段视频",
		"link":   "发来一条消息",
		" text ": "发来一条消息",
	}
	for messageType, want := range tests {
		if got := defaultMessagePreview(messageType); got != want {
			t.Errorf("defaultMessagePreview(%q)=%q want %q", messageType, got, want)
		}
	}
}

func TestPushValidationHelpers(t *testing.T) {
	valid := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if !validToken(valid) || validToken("abc") || validToken(strings.Repeat("z", 64)) {
		t.Fatal("token validation returned an unexpected result")
	}
	if !validDeviceID("iphone") || validDeviceID(" ") || validDeviceID(strings.Repeat("x", 129)) {
		t.Fatal("device ID validation returned an unexpected result")
	}
	if !validEnvironment(pushpb.PushEnvironment_SANDBOX) || !validEnvironment(pushpb.PushEnvironment_PRODUCTION) || validEnvironment(pushpb.PushEnvironment_PUSH_ENVIRONMENT_UNSPECIFIED) {
		t.Fatal("environment validation returned an unexpected result")
	}
	if parseEnvironment("PRODUCTION") != pushpb.PushEnvironment_PRODUCTION || parseEnvironment("unknown") != pushpb.PushEnvironment_SANDBOX {
		t.Fatal("environment parsing returned an unexpected result")
	}
}
