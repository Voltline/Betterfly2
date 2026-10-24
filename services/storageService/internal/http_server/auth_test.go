package http_server

import (
	pb "Betterfly2/proto/server_rpc/auth"
	"errors"
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

func TestJWTAuthMiddlewareSeparatesAuthOutageFromBadCredentials(t *testing.T) {
	original := validateJWT
	t.Cleanup(func() { validateJWT = original })
	tests := []struct {
		name       string
		rsp        *pb.CheckJwtRsp
		err        error
		wantStatus int
		wantNext   bool
	}{
		{name: "rpc failure", err: errors.New("deadline exceeded"), wantStatus: http.StatusServiceUnavailable},
		{name: "auth database failure", rsp: &pb.CheckJwtRsp{Result: pb.AuthResult_SERVICE_ERROR}, wantStatus: http.StatusServiceUnavailable},
		{name: "bad jwt", rsp: &pb.CheckJwtRsp{Result: pb.AuthResult_JWT_ERROR}, wantStatus: http.StatusUnauthorized},
		{name: "valid jwt", rsp: &pb.CheckJwtRsp{Result: pb.AuthResult_OK, UserId: 7}, wantStatus: http.StatusOK, wantNext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validateJWT = func(int64, string) (*pb.CheckJwtRsp, error) { return tt.rsp, tt.err }
			nextCalled := false
			handler := JWTAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				if userID, ok := authenticatedUserID(r); !ok || userID != 7 {
					t.Fatalf("authenticated user id = %d, %t", userID, ok)
				}
			}))
			req := httptest.NewRequest(http.MethodGet, "/storage_service/download", nil)
			req.Header.Set("Authorization", "Bearer abc")
			req.Header.Set("X-User-ID", "7")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus || nextCalled != tt.wantNext {
				t.Fatalf("status=%d next=%t, want %d %t", rec.Code, nextCalled, tt.wantStatus, tt.wantNext)
			}
			if retry := rec.Header().Get("Retry-After"); (tt.wantStatus == http.StatusServiceUnavailable) != (retry != "") {
				t.Fatalf("Retry-After=%q for status %d", retry, rec.Code)
			}
		})
	}
}
