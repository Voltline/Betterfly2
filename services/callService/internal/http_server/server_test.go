package http_server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	callservice "callService/internal/call"
)

type readinessStore struct{ err error }

func (s readinessStore) Ping(context.Context) error                             { return s.err }
func (readinessStore) UserTopic(context.Context, int64) (string, error)         { return "", nil }
func (readinessStore) CreateSession(context.Context, callservice.Session) error { return nil }
func (readinessStore) GetSession(context.Context, string) (callservice.Session, error) {
	return callservice.Session{}, nil
}
func (readinessStore) AcceptSession(context.Context, string, int64, callservice.Description) (callservice.Session, error) {
	return callservice.Session{}, nil
}
func (readinessStore) RejectSession(context.Context, string, int64, callpb.CallEndReason, string) (callservice.Session, error) {
	return callservice.Session{}, nil
}
func (readinessStore) EndSession(context.Context, string, int64, callpb.CallEndReason, string) (callservice.Session, error) {
	return callservice.Session{}, nil
}
func (readinessStore) ExpireRinging(context.Context, time.Time, int64) ([]callservice.Session, error) {
	return nil, nil
}

type noopICE struct{}

func (noopICE) Servers(int64, time.Time) []*callpb.IceServer { return nil }

func TestHealthAndReadinessEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		storeErr   error
		wantStatus int
		wantState  string
	}{
		{name: "health", path: "/health", wantStatus: http.StatusOK, wantState: "ok"},
		{name: "ready", path: "/ready", wantStatus: http.StatusOK, wantState: "ready"},
		{name: "not ready", path: "/ready", storeErr: errors.New("redis unavailable"), wantStatus: http.StatusServiceUnavailable, wantState: "not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := callservice.NewService(readinessStore{err: tt.storeErr}, nil, noopICE{}, time.Minute)
			rec := httptest.NewRecorder()
			New(service).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected HTTP response: status=%d headers=%v", rec.Code, rec.Header())
			}
			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["status"] != tt.wantState {
				t.Fatalf("unexpected body: %+v", body)
			}
			if tt.storeErr != nil && body["error"] != tt.storeErr.Error() {
				t.Fatalf("readiness error was not preserved: %+v", body)
			}
		})
	}
}
