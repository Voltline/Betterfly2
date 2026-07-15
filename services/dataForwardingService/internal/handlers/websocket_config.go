package handlers

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type websocketConfig struct {
	allowedOrigins     map[string]struct{}
	allowMissingOrigin bool
	maxMessageBytes    int64
	authTimeout        time.Duration
	pongWait           time.Duration
	pingInterval       time.Duration
	writeTimeout       time.Duration
	sessionLeaseTTL    time.Duration
	routeLeaseTTL      time.Duration
	leaseRefresh       time.Duration
	leaseJitter        time.Duration
	redisFailureGrace  int
	readHeaderTimeout  time.Duration
	idleTimeout        time.Duration
	maxHeaderBytes     int
}

func loadWebSocketConfig() websocketConfig {
	pongWait := envDurationValue("WS_PONG_WAIT", 60*time.Second)
	pingInterval := envDurationValue("WS_PING_INTERVAL", 25*time.Second)
	if pingInterval >= pongWait {
		pingInterval = pongWait / 2
	}
	sessionLeaseTTL := envDurationValue("WS_SESSION_LEASE_TTL", 2*time.Minute)
	routeLeaseTTL := envDurationValue("WS_ROUTE_LEASE_TTL", 90*time.Second)
	shortestLease := sessionLeaseTTL
	if routeLeaseTTL < shortestLease {
		shortestLease = routeLeaseTTL
	}
	leaseRefresh := envDurationValue("WS_LEASE_REFRESH_INTERVAL", 30*time.Second)
	if leaseRefresh >= shortestLease/2 {
		leaseRefresh = shortestLease / 3
	}
	leaseJitter := envDurationValue("WS_LEASE_REFRESH_JITTER", 5*time.Second)
	if leaseJitter >= leaseRefresh/2 {
		leaseJitter = leaseRefresh / 4
	}
	return websocketConfig{
		allowedOrigins:     parseAllowedOrigins(os.Getenv("WS_ALLOWED_ORIGINS")),
		allowMissingOrigin: envBoolValue("WS_ALLOW_MISSING_ORIGIN", true),
		maxMessageBytes:    int64(envIntValue("WS_MAX_MESSAGE_BYTES", 4<<20)),
		authTimeout:        envDurationValue("WS_AUTH_TIMEOUT", 15*time.Second),
		pongWait:           pongWait,
		pingInterval:       pingInterval,
		writeTimeout:       envDurationValue("WS_WRITE_TIMEOUT", 10*time.Second),
		sessionLeaseTTL:    sessionLeaseTTL,
		routeLeaseTTL:      routeLeaseTTL,
		leaseRefresh:       leaseRefresh,
		leaseJitter:        leaseJitter,
		redisFailureGrace:  envIntValue("WS_REDIS_FAILURE_GRACE", 3),
		readHeaderTimeout:  envDurationValue("WS_READ_HEADER_TIMEOUT", 5*time.Second),
		idleTimeout:        envDurationValue("WS_IDLE_TIMEOUT", 60*time.Second),
		maxHeaderBytes:     envIntValue("WS_MAX_HEADER_BYTES", 1<<20),
	}
}

func parseAllowedOrigins(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		origin, ok := canonicalOrigin(item)
		if ok {
			result[origin] = struct{}{}
		}
	}
	return result
}

func canonicalOrigin(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return strings.ToLower(parsed.Scheme) + "://" + net.JoinHostPort(hostname, port), true
}

func (c websocketConfig) checkOrigin(r *http.Request) bool {
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return c.allowMissingOrigin
	}
	origin, ok := canonicalOrigin(rawOrigin)
	if !ok {
		return false
	}
	_, allowed := c.allowedOrigins[origin]
	return allowed
}

func envDurationValue(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envIntValue(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envBoolValue(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
