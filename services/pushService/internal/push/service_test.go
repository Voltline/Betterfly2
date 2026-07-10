package push

import (
	"context"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
)

type memoryStore struct {
	tokens      []db.PushDeviceToken
	deactivated []int64
}

func (s *memoryStore) Ping(context.Context) error { return nil }
func (s *memoryStore) RegisterVoIPToken(_ context.Context, userID int64, deviceID, token, environment, bundleID string) error {
	s.tokens = append(s.tokens, db.PushDeviceToken{ID: int64(len(s.tokens) + 1), UserID: userID, DeviceID: deviceID, Token: token, Environment: environment, PushType: PushTypeVoIP, BundleID: bundleID, IsActive: true})
	return nil
}
func (s *memoryStore) UnregisterVoIPToken(_ context.Context, userID int64, deviceID, environment string) (bool, error) {
	for index, token := range s.tokens {
		if token.UserID == userID && token.DeviceID == deviceID && token.Environment == environment {
			s.tokens = append(s.tokens[:index], s.tokens[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (s *memoryStore) ListActiveVoIPTokens(_ context.Context, userID int64) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.UserID == userID && token.IsActive {
			result = append(result, token)
		}
	}
	return result, nil
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
