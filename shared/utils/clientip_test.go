package utils

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsForwardedHeadersOnlyFromProxies(t *testing.T) {
	trusted := ParseTrustedProxies(DefaultTrustedProxyCIDRs + ", not-a-cidr")
	cases := []struct {
		name, remote, xff, realIP, want string
	}{
		{"direct client ignores spoofed headers", "198.51.100.7:5555", "1.2.3.4", "5.6.7.8", "198.51.100.7"},
		{"ingress forwards client", "10.0.0.5:5555", "203.0.113.4", "", "203.0.113.4"},
		{"spoofed left entries are skipped", "10.0.0.5:5555", "1.2.3.4, 203.0.113.4, 10.0.0.9", "", "203.0.113.4"},
		{"real ip fallback", "10.0.0.5:5555", "", "203.0.113.8", "203.0.113.8"},
		{"proxy without usable headers is unknown", "10.0.0.5:5555", "nonsense", "", ""},
		{"ipv4 mapped ipv6", "[::ffff:198.51.100.7]:5555", "", "", "198.51.100.7"},
		{"ipv6 client behind proxy", "[::1]:5555", "2001:db8::1", "", "2001:db8::1"},
	}
	for _, tc := range cases {
		request := httptest.NewRequest("GET", "/ws", nil)
		request.RemoteAddr = tc.remote
		if tc.xff != "" {
			request.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.realIP != "" {
			request.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := ClientIP(request, trusted); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
