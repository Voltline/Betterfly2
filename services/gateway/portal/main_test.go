package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortalServesOnlyThePage(t *testing.T) {
	h := handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200},
		{"GET", "/index.html", 200},
		{"GET", "/healthz", 200},
		{"GET", "/other", 404},
		{"POST", "/", 405},
	} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != tc.status {
			t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, recorder.Code, tc.status)
		}
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), "Betterfly2 网关") || recorder.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("portal page or security headers missing")
	}
}
