package push

import (
	"context"
	"errors"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
)

type memoryStore struct {
	tokens      []db.PushDeviceToken
	deactivated []int64
	notify      map[int64]bool
	audits      []db.PushDebugAudit
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
		MessageType: "text", SentAt: now.Format(time.RFC3339Nano),
	}}}
	if err := service.Handle(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one deduplicated message push, got %+v", sender.sent)
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
func (s *memoryStore) DeactivateToken(_ context.Context, id int64) error {
	s.deactivated = append(s.deactivated, id)
	return nil
}

type sentNotification struct{ notification Notification }
type memorySender struct {
	sent []sentNotification
	err  error
}

func (s *memorySender) Ready() error { return nil }
func (s *memorySender) Send(_ context.Context, notification Notification) (SendResult, error) {
	s.sent = append(s.sent, sentNotification{notification: notification})
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
		TargetUserIds: []int64{2}, SenderUserId: 1, ConversationId: 1, MessageType: "text",
	}}}
	if err := service.Handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("disabled direct message notifications should not be sent: %+v", sender.sent)
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
