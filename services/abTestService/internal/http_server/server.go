package http_server

import (
	"abTestService/internal/abtest"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const maxJSONBodyBytes = 64 << 10

//go:embed admin.html
var adminHTML string

var adminTemplate = template.Must(template.New("admin").Parse(adminHTML))

type Server struct {
	service    *abtest.Service
	adminToken string
}

func NewServer(service *abtest.Service) *Server {
	return &Server{
		service:    service,
		adminToken: os.Getenv("ABTEST_ADMIN_TOKEN"),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.health)
	mux.HandleFunc("/abtest/v1/client/config", s.clientConfig)
	mux.HandleFunc("/abtest/v1/evaluate", s.evaluate)
	mux.HandleFunc("/abtest/admin", s.adminPanel)
	mux.HandleFunc("/abtest/admin/", s.adminPanel)
	mux.HandleFunc("/abtest/admin/api/experiments", s.requireAdmin(s.experiments))
	mux.HandleFunc("/abtest/admin/api/experiments/", s.requireAdmin(s.experimentByID))
	return securityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) clientConfig(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	deviceID := strings.TrimSpace(query.Get("device_id"))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id is required")
		return
	}

	context := map[string]string{
		"platform":       query.Get("platform"),
		"app_version":    query.Get("app_version"),
		"os":             query.Get("os"),
		"os_version":     query.Get("os_version"),
		"system_version": firstNonEmpty(query.Get("system_version"), query.Get("os_version")),
	}
	resp, err := s.service.Evaluate(abtest.EvaluateRequest{
		SubjectType: abtest.SubjectTypeDevice,
		SubjectID:   deviceID,
		Context:     context,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req abtest.EvaluateRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	resp, err := s.service.Evaluate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) experiments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		experiments, err := s.service.ListExperiments()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiments)
	case http.MethodPost:
		var req abtest.CreateExperimentRequest
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		experiment, err := s.service.CreateExperiment(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, experiment)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) experimentByID(w http.ResponseWriter, r *http.Request) {
	id, action, childID, childAction, ok := parseExperimentPath(strings.TrimPrefix(r.URL.Path, "/abtest/admin/api/experiments/"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if action != "" {
		s.experimentAction(w, r, id, action, childID, childAction)
		return
	}

	switch r.Method {
	case http.MethodGet:
		experiment, err := s.service.GetExperiment(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	case http.MethodPut:
		var req abtest.UpdateExperimentRequest
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		experiment, err := s.service.UpdateExperiment(id, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) experimentAction(w http.ResponseWriter, r *http.Request, id int64, action string, childID int64, childAction string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	switch action {
	case "start":
		experiment, err := s.service.SetExperimentStatus(id, abtest.StatusRunning)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	case "pause":
		experiment, err := s.service.SetExperimentStatus(id, abtest.StatusPaused)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	case "stop":
		experiment, err := s.service.SetExperimentStatus(id, abtest.StatusStopped)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	case "withdraw":
		experiment, err := s.service.WithdrawExperiment(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, experiment)
	case "groups":
		if childAction == "push_full" {
			if childID <= 0 {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			experiment, err := s.service.PushFullGroup(id, childID)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, experiment)
			return
		}
		if childID != 0 || childAction != "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		var req abtest.GroupInput
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		group, err := s.service.AddGroup(id, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, group)
	case "overrides":
		if childID != 0 || childAction != "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		var req abtest.OverrideInput
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		override, err := s.service.AddOverride(id, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, override)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) adminPanel(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || r.URL.Path != "/abtest/admin" && r.URL.Path != "/abtest/admin/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTemplate.Execute(w, map[string]string{
		"TokenHint": adminTokenHint(s.adminToken),
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			http.NotFound(w, r)
			return
		}
		if !s.isAdminAuthorized(r) {
			writeError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) isAdminAuthorized(r *http.Request) bool {
	if s.adminToken == "" {
		return false
	}
	token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		token = strings.TrimSpace(authorization[7:])
	}
	return len(token) == len(s.adminToken) && subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) == 1
}

func parseExperimentPath(path string) (int64, string, int64, string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return 0, "", 0, "", false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", 0, "", false
	}
	if len(parts) == 1 {
		return id, "", 0, "", true
	}
	if len(parts) == 2 {
		return id, parts[1], 0, "", true
	}
	if len(parts) == 4 {
		childID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return 0, "", 0, "", false
		}
		return id, parts[1], childID, parts[3], true
	}
	return 0, "", 0, "", false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, value interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func adminTokenHint(token string) string {
	return "管理接口需要 Header: Authorization: Bearer <ABTEST_ADMIN_TOKEN> 或 X-Admin-Token。"
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
