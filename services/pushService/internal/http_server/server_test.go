package http_server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/db"
	pushservice "pushService/internal/push"
)

type httpTestStore struct {
	tokens []db.PushDeviceToken
	audits []db.PushDebugAudit
}

func (s *httpTestStore) Ping(context.Context) error { return nil }
func (s *httpTestStore) RegisterToken(context.Context, int64, string, string, string, string, string) error {
	return nil
}
func (s *httpTestStore) UnregisterToken(context.Context, int64, string, string, string) (bool, error) {
	return false, nil
}
func (s *httpTestStore) ListActiveTokens(_ context.Context, userID int64, pushType string) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.UserID == userID && token.PushType == pushType && token.IsActive {
			result = append(result, token)
		}
	}
	return result, nil
}
func (s *httpTestStore) ListMessageTokens(context.Context, []int64, int64, bool) ([]db.PushDeviceToken, error) {
	return s.tokens, nil
}
func (s *httpTestStore) ClaimMessageDeliveries(_ context.Context, _ int64, tokenIDs []int64, _ time.Time, _ time.Duration) (map[int64]int, bool, error) {
	claims := make(map[int64]int, len(tokenIDs))
	for _, tokenID := range tokenIDs {
		claims[tokenID] = 1
	}
	return claims, false, nil
}
func (s *httpTestStore) FinalizeMessageDeliveries(context.Context, []pushservice.DeliveryUpdate) error {
	return nil
}
func (s *httpTestStore) MessageNotificationsEnabled(context.Context, int64, int64, bool) (bool, error) {
	return true, nil
}
func (s *httpTestStore) MessagePresentation(context.Context, int64, int64, bool) (pushservice.MessagePresentation, error) {
	return pushservice.MessagePresentation{Title: "Alice", SenderName: "Alice", Avatar: "avatar-hash"}, nil
}
func (s *httpTestStore) FindTokens(context.Context, pushservice.TokenFilter) ([]db.PushDeviceToken, error) {
	return s.tokens, nil
}
func (s *httpTestStore) BroadcastAudience(context.Context, string) (int64, int64, error) {
	return int64(len(s.tokens)), int64(len(s.tokens)), nil
}
func (s *httpTestStore) ListBroadcastTokens(_ context.Context, _ string, afterID, throughID int64, limit int) ([]db.PushDeviceToken, error) {
	var result []db.PushDeviceToken
	for _, token := range s.tokens {
		if token.ID > afterID && token.ID <= throughID && token.IsActive && token.PushType == pushservice.PushTypeAPNs {
			result = append(result, token)
		}
	}
	return result, nil
}
func (s *httpTestStore) GetToken(_ context.Context, id int64) (db.PushDeviceToken, error) {
	for _, token := range s.tokens {
		if token.ID == id {
			return token, nil
		}
	}
	return db.PushDeviceToken{}, pushservice.ErrTokenNotFound
}
func (s *httpTestStore) CreateDebugAudit(_ context.Context, audit *db.PushDebugAudit) error {
	s.audits = append(s.audits, *audit)
	return nil
}
func (s *httpTestStore) ListDebugAudits(context.Context, int) ([]db.PushDebugAudit, error) {
	return s.audits, nil
}
func (s *httpTestStore) TokenSummary(context.Context) (pushservice.TokenSummary, error) {
	return pushservice.TokenSummary{Total: int64(len(s.tokens)), Active: int64(len(s.tokens)), APNs: int64(len(s.tokens))}, nil
}
func (s *httpTestStore) DeactivateToken(context.Context, int64) error    { return nil }
func (s *httpTestStore) DeactivateTokens(context.Context, []int64) error { return nil }

type httpTestSender struct{}

func (httpTestSender) Ready() error { return nil }
func (httpTestSender) Send(context.Context, pushservice.Notification) (pushservice.SendResult, error) {
	return pushservice.SendResult{APNSID: "debug-apns-id"}, nil
}

type httpTestPublisher struct{}

func (httpTestPublisher) Publish(context.Context, string, *pushpb.ResponseMessage) error { return nil }

func newHTTPTestServer(token string) (*Server, *httpTestStore) {
	store := &httpTestStore{tokens: []db.PushDeviceToken{{ID: 1, UserID: 2, DeviceID: "iphone", Token: "00112233445566778899", Environment: "sandbox", PushType: pushservice.PushTypeAPNs, IsActive: true}}}
	service := pushservice.NewService(store, httpTestSender{}, httpTestPublisher{}, "com.Voltline.Betterfly2")
	return NewWithAdminToken(service, token), store
}

func TestAdminRoutesAreDisabledWithoutToken(t *testing.T) {
	server, _ := newHTTPTestServer("")
	request := httptest.NewRequest(http.MethodGet, "/push/admin", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.Code)
	}
}

func TestAdminPanelIsEmbeddedWhenEnabled(t *testing.T) {
	server, _ := newHTTPTestServer("secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/push/admin", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Betterfly Push") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unexpected admin panel: status=%d", response.Code)
	}
}

func TestAdminAPIRequiresTokenAndReturnsMaskedDevices(t *testing.T) {
	server, _ := newHTTPTestServer("secret")
	unauthorized := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/push/admin/api/tokens", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/push/admin/api/tokens", nil)
	request.Header.Set("X-Admin-Token", "secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "00112233445566778899") || !strings.Contains(response.Body.String(), "001122...778899") {
		t.Fatalf("unexpected token response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminMessageEndpointSendsAndAudits(t *testing.T) {
	server, store := newHTTPTestServer("secret")
	body := `{"target_user_ids":[2],"sender_user_id":1,"conversation_id":1,"message_type":"text","title":"Debug"}`
	request := httptest.NewRequest(http.MethodPost, "/push/admin/api/send/message", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-Admin-Operator", "tester")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accepted":1`) {
		t.Fatalf("unexpected send response: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.audits) != 1 || store.audits[0].Operator != "tester" {
		t.Fatalf("audit not stored: %+v", store.audits)
	}
}

func TestAdminMessageEndpointRejectsUnknownFields(t *testing.T) {
	server, _ := newHTTPTestServer("secret")
	request := httptest.NewRequest(http.MethodPost, "/push/admin/api/send/message", strings.NewReader(`{"unexpected":true}`))
	request.Header.Set("X-Admin-Token", "secret")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminBroadcastEndpointSendsAndAudits(t *testing.T) {
	server, store := newHTTPTestServer("secret")
	body := `{"campaign_id":"launch","title":"新功能上线","body":"现在就来体验","deep_link":"betterfly://campaign/launch"}`
	request := httptest.NewRequest(http.MethodPost, "/push/admin/api/send/broadcast", strings.NewReader(body))
	request.Header.Set("X-Admin-Token", "secret")
	request.Header.Set("X-Admin-Operator", "marketing")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"kind":"broadcast"`) || !strings.Contains(response.Body.String(), `"accepted":1`) {
		t.Fatalf("unexpected broadcast response: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.audits) != 1 || store.audits[0].Kind != "broadcast" {
		t.Fatalf("broadcast audit not stored: %+v", store.audits)
	}
}
