package http_server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	callservice "callService/internal/call"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Server struct {
	service *callservice.Service
}

func New(service *callservice.Service) *Server {
	return &Server{service: service}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("POST /call/livekit/webhook", s.mediaWebhook)
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}

func (s *Server) mediaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid webhook body"})
		return
	}
	err = s.service.HandleMediaWebhook(r.Context(), body, r.Header.Get("Authorization"))
	if errors.Is(err, callservice.ErrInvalidWebhook) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid webhook signature"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook temporarily unavailable"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.service.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
