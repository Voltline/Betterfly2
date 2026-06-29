package http_server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"paymentService/internal/payment"
	"strconv"
	"strings"
)

//go:embed debug.html
var debugHTML string

//go:embed checkout.html
var checkoutHTML string

type Server struct {
	service    *payment.Service
	adminToken string
}

func NewServer(service *payment.Service) *Server {
	return &Server{
		service:    service,
		adminToken: os.Getenv("PAYMENT_ADMIN_TOKEN"),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.health)
	mux.HandleFunc("/payment/debug", s.debugPanel)
	mux.HandleFunc("/payment/checkout", s.checkoutPage)
	mux.HandleFunc("/payment/v1/orders", s.requireJWT(s.orders))
	mux.HandleFunc("/payment/v1/orders/", s.requireJWT(s.orderByNo))
	mux.HandleFunc("/payment/v1/providers/mock/callback", s.providerCallback)
	mux.HandleFunc("/payment/v1/mock/pay/", s.requireAdmin(s.mockPay))
	mux.HandleFunc("/payment/admin/api/orders", s.requireAdmin(s.adminOrders))
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) debugPanel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(debugHTML))
}

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(checkoutHTML))
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		userInfo, err := currentUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req payment.CreateOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		req.UserID = userInfo.UserID
		if req.IdempotencyKey == "" {
			req.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		}
		order, err := s.service.CreateOrder(req)
		if err != nil {
			writePaymentError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, order)
	case http.MethodGet:
		userInfo, err := currentUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
		orders, err := s.service.ListOrders(userInfo.UserID, limit)
		if err != nil {
			writePaymentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"orders": orders})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) orderByNo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userInfo, err := currentUser(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	orderNo := strings.Trim(strings.TrimPrefix(r.URL.Path, "/payment/v1/orders/"), "/")
	if orderNo == "" || strings.Contains(orderNo, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	order, err := s.service.GetOrder(userInfo.UserID, orderNo)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) providerCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	var req payment.CallbackRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	order, err := s.service.HandleCallback(req, payload, strings.TrimSpace(r.Header.Get("X-Payment-Signature")))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, payment.CallbackResponse{Accepted: true, Order: order})
}

func (s *Server) mockPay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	orderNo := strings.Trim(strings.TrimPrefix(r.URL.Path, "/payment/v1/mock/pay/"), "/")
	req, payload, signature, err := s.service.MockPay(orderNo)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	order, err := s.service.HandleCallback(req, payload, signature)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payment.CallbackResponse{Accepted: true, Order: order})
}

func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("user_id")), 10, 64)
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	orders, err := s.service.ListOrders(userID, limit)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"orders": orders})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken != "" {
			token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
			if token == "" {
				token = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
			}
			if token != s.adminToken {
				writeError(w, http.StatusUnauthorized, "admin token required")
				return
			}
		}
		next(w, r)
	}
}

func writePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payment.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, payment.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, payment.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
