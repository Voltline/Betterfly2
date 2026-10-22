package http_server

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	pushservice "pushService/internal/push"
)

//go:embed admin.html
var adminHTML string

const maxAdminBodyBytes = 64 << 10

type Server struct {
	service    *pushservice.Service
	adminToken string
}

func New(service *pushservice.Service) *Server {
	return NewWithAdminToken(service, os.Getenv("PUSH_ADMIN_TOKEN"))
}

func NewWithAdminToken(service *pushservice.Service, token string) *Server {
	return &Server{service: service, adminToken: strings.TrimSpace(token)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", s.ready)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /push/admin", s.adminPanel)
	mux.HandleFunc("GET /push/admin/", s.adminPanel)
	mux.HandleFunc("GET /push/admin/api/summary", s.requireAdmin(s.summary))
	mux.HandleFunc("GET /push/admin/api/tokens", s.requireAdmin(s.tokens))
	mux.HandleFunc("GET /push/admin/api/audits", s.requireAdmin(s.audits))
	mux.HandleFunc("POST /push/admin/api/send/message", s.requireAdmin(s.sendMessage))
	mux.HandleFunc("POST /push/admin/api/send/voip", s.requireAdmin(s.sendVoIP))
	mux.HandleFunc("POST /push/admin/api/send/broadcast", s.requireAdmin(s.sendBroadcast))
	return securityHeaders(mux)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.service.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) adminPanel(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || (r.URL.Path != "/push/admin" && r.URL.Path != "/push/admin/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(adminHTML))
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	summary, err := s.service.AdminSummary(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	userID, _ := strconv.ParseInt(query.Get("user_id"), 10, 64)
	limit, _ := strconv.Atoi(query.Get("limit"))
	filter := pushservice.TokenFilter{
		UserID: userID, DeviceID: strings.TrimSpace(query.Get("device_id")),
		Environment: strings.ToLower(strings.TrimSpace(query.Get("environment"))),
		PushType:    strings.ToLower(strings.TrimSpace(query.Get("push_type"))),
		ActiveOnly:  query.Get("active_only") == "true", Limit: limit,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tokens, err := s.service.AdminTokens(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) audits(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	audits, err := s.service.AdminAudits(ctx, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, audits)
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var request pushservice.AdminMessageRequest
	if err := decodeAdminJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	report, err := s.service.AdminSendMessage(ctx, request, operatorName(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) sendVoIP(w http.ResponseWriter, r *http.Request) {
	var request pushservice.AdminVoIPRequest
	if err := decodeAdminJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	report, err := s.service.AdminSendVoIP(ctx, request, operatorName(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) sendBroadcast(w http.ResponseWriter, r *http.Request) {
	var request pushservice.AdminBroadcastRequest
	if err := decodeAdminJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	report, err := s.service.AdminSendBroadcast(ctx, request, operatorName(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			http.NotFound(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
		if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
			provided = strings.TrimSpace(authorization[7:])
		}
		if len(provided) != len(s.adminToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.adminToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
			return
		}
		next(w, r)
	}
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

// operatorName 读取管理页填写的操作人。页面对其做百分号编码，因为浏览器不允许在请求头里放非Latin-1字符；
// 未编码的值原样使用。结果保证是合法UTF-8且不超过审计表的100个字符，否则审计写入会失败。
func operatorName(r *http.Request) string {
	operator := r.Header.Get("X-Admin-Operator")
	if decoded, err := url.PathUnescape(operator); err == nil {
		operator = decoded
	}
	operator = strings.TrimSpace(strings.ToValidUTF8(operator, ""))
	if runes := []rune(operator); len(runes) > 100 {
		operator = strings.TrimSpace(string(runes[:100]))
	}
	return operator
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
