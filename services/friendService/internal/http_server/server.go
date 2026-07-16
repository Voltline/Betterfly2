package http_server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"friendService/internal/publisher"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type readinessCheck func(context.Context) error

func New(addr string) *http.Server {
	return NewWithReadiness(addr, defaultReadiness)
}

func NewWithReadiness(addr string, ready readinessCheck) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
}

func defaultReadiness(context.Context) error {
	if publisher.KafkaProducer == nil {
		return errors.New("kafka producer not ready")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
