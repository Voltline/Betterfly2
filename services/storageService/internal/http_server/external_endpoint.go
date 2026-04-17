package http_server

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func resolveRustFSExternalEndpoint(r *http.Request) string {
	if explicit := strings.TrimSpace(os.Getenv("RUSTFS_EXTERNAL_ENDPOINT_URL")); explicit != "" {
		return explicit
	}

	scheme := strings.TrimSpace(os.Getenv("RUSTFS_EXTERNAL_SCHEME"))
	if scheme == "" {
		scheme = forwardedProto(r)
	}
	if scheme == "" {
		if r != nil && r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	host := strings.TrimSpace(os.Getenv("RUSTFS_EXTERNAL_HOST"))
	if host == "" {
		host = requestHostname(r)
	}
	if host == "" {
		host = "localhost"
	}

	port := strings.TrimSpace(os.Getenv("RUSTFS_EXTERNAL_PORT"))
	if port == "" {
		port = "9000"
	}

	return (&url.URL{
		Scheme: scheme,
		Host:   joinHostPort(host, port),
	}).String()
}

func forwardedProto(r *http.Request) string {
	if r == nil {
		return ""
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		parts := strings.Split(proto, ",")
		return strings.TrimSpace(parts[0])
	}
	return ""
}

func requestHostname(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return parsedHost
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.Trim(host, "[]")
	}
	return host
}

func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return net.JoinHostPort(host, port)
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
