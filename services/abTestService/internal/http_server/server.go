package http_server

import (
	"abTestService/internal/abtest"
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
)

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
	return mux
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	if r.URL.Path != "/abtest/admin" && r.URL.Path != "/abtest/admin/" {
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
		if !s.isAdminAuthorized(r) {
			writeError(w, http.StatusUnauthorized, "admin token required")
			return
		}
		next(w, r)
	}
}

func (s *Server) isAdminAuthorized(r *http.Request) bool {
	if s.adminToken == "" {
		return true
	}
	token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if token == "" {
		token = strings.TrimPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	}
	return token == s.adminToken
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

func adminTokenHint(token string) string {
	if token == "" {
		return "当前未配置 ABTEST_ADMIN_TOKEN，管理接口处于本地开放模式。"
	}
	return "管理接口需要 Header: Authorization: Bearer <ABTEST_ADMIN_TOKEN> 或 X-Admin-Token。"
}
