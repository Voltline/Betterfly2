package http_server

import (
	"chatbotService/internal/chatbot"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type contextKey string

const principalContextKey contextKey = "bot_principal"

type Server struct {
	service *chatbot.Service
	token   string
	botID   string
	scopes  map[string]bool
}

func NewServer(service *chatbot.Service) *Server {
	token := strings.TrimSpace(os.Getenv("CHATBOT_BOT_TOKEN"))
	botID := strings.TrimSpace(os.Getenv("CHATBOT_BOT_ID"))
	if botID == "" {
		botID = "openclaw"
	}
	return &Server{
		service: service,
		token:   token,
		botID:   botID,
		scopes:  parseScopes(os.Getenv("CHATBOT_BOT_SCOPES")),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.health)
	mux.HandleFunc("/chatbot/v1/users/", s.requireScope(chatbot.ScopeReadUser, s.userByID))
	mux.HandleFunc("/chatbot/v1/groups/", s.groupRoutes)
	mux.HandleFunc("/chatbot/v1/conversations/", s.requireScope(chatbot.ScopeReadMessages, s.recentMessages))
	mux.HandleFunc("/chatbot/v1/messages/send", s.requireScope(chatbot.ScopeSendMessage, s.sendMessage))
	mux.HandleFunc("/chatbot/v1/openclaw/webhook", s.requireScope(chatbot.ScopeOpenClawWebhook, s.openClawWebhook))
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) userByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	userID, ok := parsePathID(strings.TrimPrefix(r.URL.Path, "/chatbot/v1/users/"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	user, err := s.service.GetUser(userID)
	s.service.Audit(principal, "get_user", "user", chatbot.TargetID(userID), err)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) groupRoutes(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/members") {
		s.requireScope(chatbot.ScopeReadGroup, s.groupMembers)(w, r)
		return
	}
	s.requireScope(chatbot.ScopeReadGroup, s.groupByID)(w, r)
}

func (s *Server) groupByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	groupID, ok := parsePathID(strings.TrimPrefix(r.URL.Path, "/chatbot/v1/groups/"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	group, err := s.service.GetGroup(groupID)
	s.service.Audit(principal, "get_group", "group", chatbot.TargetID(groupID), err)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, group)
}

func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/chatbot/v1/groups/"), "/members")
	groupID, ok := parsePathID(path)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	members, err := s.service.GetGroupMembers(groupID)
	s.service.Audit(principal, "get_group_members", "group", chatbot.TargetID(groupID), err)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"members": members})
}

func (s *Server) recentMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	conversationType, conversationID, ok := parseConversationPath(strings.TrimPrefix(r.URL.Path, "/chatbot/v1/conversations/"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	userID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("user_id")), 10, 64)
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	messages, err := s.service.GetRecentMessages(conversationType, conversationID, userID, limit)
	s.service.Audit(principal, "get_recent_messages", conversationType, chatbot.TargetID(conversationID), err)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": messages})
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	var req chatbot.SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	err := s.service.SendMessage(req)
	s.service.Audit(principal, "send_message", "message", "", err)
	if err != nil {
		writeError(w, http.StatusNotImplemented, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) openClawWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal := principalFromRequest(r)
	var req chatbot.OpenClawWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	resp, err := s.service.HandleOpenClawWebhook(req)
	s.service.Audit(principal, "openclaw_webhook", "event", req.EventID, err)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			writeError(w, http.StatusServiceUnavailable, "CHATBOT_BOT_TOKEN is not configured")
			return
		}
		token := bearerToken(r)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid bot token")
			return
		}
		principal := chatbot.BotPrincipal{BotID: s.botID, Scopes: s.scopes}
		if !principal.HasScope(scope) {
			writeError(w, http.StatusForbidden, "missing scope: "+scope)
			return
		}
		ctx := r.Context()
		ctx = withPrincipal(ctx, principal)
		next(w, r.WithContext(ctx))
	}
}

func parseScopes(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		raw = strings.Join([]string{
			chatbot.ScopeReadUser,
			chatbot.ScopeReadGroup,
			chatbot.ScopeReadMessages,
			chatbot.ScopeOpenClawWebhook,
		}, ",")
	}
	scopes := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			scopes[item] = true
		}
	}
	return scopes
}

func bearerToken(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return strings.TrimSpace(r.Header.Get("X-Bot-Token"))
	}
	return strings.TrimPrefix(authHeader, "Bearer ")
}

func parsePathID(path string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	return id, err == nil
}

func parseConversationPath(path string) (string, int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[2] != "recent_messages" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[0], id, true
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
