package push

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
)

type memoryStore struct {
	tokens          []db.PushDeviceToken
	deactivated     []int64
	notify          map[int64]bool
	audits          []db.PushDebugAudit
	presentation    MessagePresentation
	presentationErr error
}

func (s *memoryStore) EnqueueRequest(context.Context, string, *pushpb.RequestMessage, string) error {
	return nil
}
func (s *memoryStore) ClaimMessageDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}
func (s *memoryStore) ClaimVoIPDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}
func (s *memoryStore) FinalizeMessageDelivery(context.Context, DurableDeliveryUpdate) error {
	return nil
}
func (s *memoryStore) FinalizeVoIPDelivery(context.Context, DurableDeliveryUpdate) error {
	return nil
}

func (s *memoryStore) Ping(context.Context) error { return nil }

func (s *memoryStore) ListActiveTokens(_ context.Context, userID int64, pushType string) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.UserID == userID && token.PushType == pushType && token.IsActive {
			result = append(result, token)
		}
	}
	return result, nil
}

func (s *memoryStore) MessageNotificationsEnabled(_ context.Context, targetUserID, _ int64, _ bool) (bool, error) {
	if s.notify == nil {
		return true, nil
	}
	enabled, configured := s.notify[targetUserID]
	return enabled || !configured, nil
}

func (s *memoryStore) MessagePresentation(_ context.Context, _, _ int64, isGroup bool) (MessagePresentation, error) {
	if s.presentationErr != nil {
		return MessagePresentation{}, s.presentationErr
	}
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

func (s *memoryStore) DeactivateToken(_ context.Context, id int64) error {
	s.deactivated = append(s.deactivated, id)
	for index := range s.tokens {
		if s.tokens[index].ID == id {
			s.tokens[index].IsActive = false
		}
	}
	return nil
}

type memorySender struct {
	mu        sync.Mutex
	sent      []Notification
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
	s.sent = append(s.sent, notification)
	s.mu.Unlock()
	if s.sendFunc != nil {
		return s.sendFunc(notification)
	}
	return SendResult{APNSID: "test"}, s.err
}

type enqueueStore struct {
	*memoryStore
	operationKey string
	request      *pushpb.RequestMessage
}

func (s *enqueueStore) EnqueueRequest(_ context.Context, operationKey string, request *pushpb.RequestMessage, _ string) error {
	s.operationKey, s.request = operationKey, request
	return nil
}
func (s *enqueueStore) ClaimMessageDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}
func (s *enqueueStore) ClaimVoIPDeliveryBatch(context.Context, int, time.Time, time.Duration, int) ([]DurableDeliveryClaim, error) {
	return nil, nil
}
func (s *enqueueStore) FinalizeMessageDelivery(context.Context, DurableDeliveryUpdate) error {
	return nil
}
func (s *enqueueStore) FinalizeVoIPDelivery(context.Context, DurableDeliveryUpdate) error { return nil }

func TestHandleAlwaysUsesDurableEnqueue(t *testing.T) {
	store := &enqueueStore{memoryStore: &memoryStore{}}
	service := NewService(store, &memorySender{}, "com.Voltline.Betterfly2")
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{MessageId: 7}}}
	ctx := kafkaconsumer.WithOperationKey(context.Background(), "push-service/0/7")
	if err := service.Handle(ctx, request); err != nil {
		t.Fatal(err)
	}
	if store.operationKey != "push-service/0/7" || store.request != request {
		t.Fatalf("request bypassed durable enqueue: key=%q request=%p", store.operationKey, store.request)
	}
}

func TestHandleRejectsMissingOperationKeyInsteadOfFallingBack(t *testing.T) {
	store := &enqueueStore{memoryStore: &memoryStore{}}
	service := NewService(store, &memorySender{}, "com.Voltline.Betterfly2")
	if err := service.Handle(context.Background(), &pushpb.RequestMessage{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing operation key did not fail closed: %v", err)
	}
	if store.request != nil {
		t.Fatal("request without operation key reached durable store")
	}
}

func TestAdminMessagePushReturnsDetailedReportAndAudit(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 10, UserID: 2, DeviceID: "iphone-a", Token: "00112233445566778899", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		{ID: 11, UserID: 2, DeviceID: "iphone-b", Token: "aabbccddeeff00112233", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
	}}
	sender := &memorySender{}
	service := NewService(store, sender, "com.Voltline.Betterfly2")
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
	if sender.sent[0].Title != "调试" || sender.sent[0].CustomData["scenario"] != "smoke" {
		t.Fatalf("custom notification data was not forwarded: %+v", sender.sent[0])
	}
}

func TestAdminVoIPCanTargetRegisteredToken(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{{ID: 20, UserID: 2, DeviceID: "iphone", Token: "00112233445566778899", Environment: "production", PushType: PushTypeVoIP, IsActive: true}}}
	sender := &memorySender{}
	service := NewService(store, sender, "com.Voltline.Betterfly2")
	report, err := service.AdminSendVoIP(context.Background(), AdminVoIPRequest{TokenID: 20, CallerUserID: 1, CallType: "video"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != 1 || len(sender.sent) != 1 || sender.sent[0].CallID == "" || sender.sent[0].Kind != NotificationVoIP {
		t.Fatalf("unexpected VoIP debug push: report=%+v sent=%+v", report, sender.sent)
	}
}

func TestAdminBroadcastSendsAllMatchingAPNsTokens(t *testing.T) {
	store := &memoryStore{tokens: []db.PushDeviceToken{
		{ID: 1, UserID: 2, Token: "token-1", Environment: "sandbox", PushType: PushTypeAPNs, IsActive: true},
		{ID: 2, UserID: 3, Token: "token-2", Environment: "production", PushType: PushTypeAPNs, IsActive: true},
		{ID: 3, UserID: 4, Token: "token-3", Environment: "sandbox", PushType: PushTypeVoIP, IsActive: true},
	}}
	sender := &memorySender{}
	service := NewService(store, sender, "com.Voltline.Betterfly2")
	report, err := service.AdminSendBroadcast(context.Background(), AdminBroadcastRequest{CampaignID: "summer-2026", Title: "活动", Body: "详情"}, "marketing")
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 2 || report.Accepted != 2 || len(sender.sent) != 2 {
		t.Fatalf("unexpected broadcast report: %+v sent=%d", report, len(sender.sent))
	}
}

func TestAdminBroadcastPaginatesAndBoundsDetailedResults(t *testing.T) {
	tokens := make([]db.PushDeviceToken, 0, broadcastPageSize+5)
	for index := 1; index <= broadcastPageSize+5; index++ {
		tokens = append(tokens, db.PushDeviceToken{ID: int64(index), UserID: int64(index), Token: "token", Environment: "production", PushType: PushTypeAPNs, IsActive: true})
	}
	store := &memoryStore{tokens: tokens}
	sender := &memorySender{}
	service := NewService(store, sender, "com.Voltline.Betterfly2")
	report, err := service.AdminSendBroadcast(context.Background(), AdminBroadcastRequest{CampaignID: "all-users", Title: "通知", Body: "正文"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if report.Accepted != len(tokens) || len(report.Results) != maxBroadcastAuditResults || !report.ResultsTruncated {
		t.Fatalf("pagination or result bound failed: %+v", report)
	}
}

func TestDefaultMessagePreviewCoversSupportedMediaTypes(t *testing.T) {
	tests := map[string]string{"image": "发送了一张图片", "gif": "发送了一个 GIF", "file": "发送了一个文件", "audio": "发送了一条语音", "video": "发送了一段视频", "link": "发来一条消息"}
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
}
