package http_server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndReadyRoutes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		readyError error
		wantStatus int
		wantBody   string
	}{
		{name: "health", path: "/health", wantStatus: http.StatusOK, wantBody: `"ok"`},
		{name: "ready", path: "/ready", wantStatus: http.StatusOK, wantBody: `"ready"`},
		{name: "not ready", path: "/ready", readyError: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantBody: "database unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewWithReadiness(":0", func(context.Context) error { return test.readyError })
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("unexpected response: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHealthServerRejectsUnsupportedMethods(t *testing.T) {
	server := NewWithReadiness(":0", func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", response.Code)
	}
}
