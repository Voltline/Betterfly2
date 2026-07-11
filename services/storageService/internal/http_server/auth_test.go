package http_server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJWTAuthMiddleware_BypassesHealthEndpoint(t *testing.T) {
	called := false
	handler := JWTAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected health endpoint to bypass auth middleware")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestJWTAuthMiddlewareRejectsMalformedRequestsBeforeRPC(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
		userID        string
		wantStatus    int
		wantBody      string
	}{
		{name: "missing authorization", userID: "1", wantStatus: http.StatusUnauthorized, wantBody: "Missing Authorization header"},
		{name: "malformed authorization", authorization: "Token abc", userID: "1", wantStatus: http.StatusUnauthorized, wantBody: "Invalid Authorization header format"},
		{name: "missing user id", authorization: "Bearer abc", wantStatus: http.StatusBadRequest, wantBody: "Missing user_id"},
		{name: "invalid user id", authorization: "Bearer abc", userID: "not-a-number", wantStatus: http.StatusBadRequest, wantBody: "Invalid user_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			handler := JWTAuthMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true }))
			req := httptest.NewRequest(http.MethodGet, "/storage_service/download", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.userID != "" {
				req.Header.Set("X-User-ID", tt.userID)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("unexpected response: status=%d body=%q", rec.Code, rec.Body.String())
			}
			if nextCalled {
				t.Fatal("rejected request reached the next handler")
			}
		})
	}
}

func TestGetUserInfoReadsTypedContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := GetUserInfo(req); err == nil {
		t.Fatal("expected missing context value to fail")
	}
	want := &UserInfo{UserID: 7, Account: "alice"}
	req = req.WithContext(context.WithValue(req.Context(), UserContextKey{}, want))
	got, err := GetUserInfo(req)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("unexpected user info: got=%+v want=%+v", got, want)
	}
}

func TestJWTAuthMiddleware_BypassesReadyEndpoint(t *testing.T) {
	called := false
	handler := JWTAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected ready endpoint to bypass auth middleware")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}
