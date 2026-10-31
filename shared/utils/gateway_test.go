package utils

import (
	"net/http/httptest"
	"testing"
)

func TestGatewayOrigin(t *testing.T) {
	cases := []struct {
		host, proto, want string
	}{
		{"chat.example.com:54342", "https", "https://chat.example.com:54342"},
		{"chat.example.com", "https", "https://chat.example.com"},
		{"192.168.1.5:54342", "https, http", "https://192.168.1.5:54342"},
		{"[2001:db8::1]:54342", "HTTPS", "https://[2001:db8::1]:54342"},
		{"[2001:db8::1]", "http", "http://[2001:db8::1]"},
		{"chat.example.com:54342", "", ""},
		{"chat.example.com:54342", "ftp", ""},
		{"evil.com/path:54342", "https", ""},
		{"user@evil.com", "https", ""},
		{"", "https", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/storage_service/upload", nil)
		r.Host = tc.host
		if tc.proto != "" {
			r.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		if got := GatewayOrigin(r); got != tc.want {
			t.Fatalf("host=%q proto=%q: got %q want %q", tc.host, tc.proto, got, tc.want)
		}
	}
	if GatewayOrigin(nil) != "" {
		t.Fatal("nil request")
	}
}
