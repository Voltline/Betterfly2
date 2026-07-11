package http_server

import (
	"abTestService/internal/abtest"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

type recordingStore struct {
	experiments []abtest.Experiment
	lastStatus  string
	lastGroupID int64
	groups      []abtest.Group
	overrides   []abtest.Override
}

func (s *recordingStore) ListExperiments() ([]abtest.Experiment, error) { return s.experiments, nil }
func (s *recordingStore) ListEvaluationExperiments() ([]abtest.Experiment, error) {
	return s.experiments, nil
}
func (s *recordingStore) GetExperiment(id int64) (abtest.Experiment, error) {
	for _, experiment := range s.experiments {
		if experiment.ID == id {
			return experiment, nil
		}
	}
	return abtest.Experiment{}, errors.New("not found")
}
func (s *recordingStore) CreateExperiment(req abtest.CreateExperimentRequest) (abtest.Experiment, error) {
	experiment := abtest.Experiment{ID: 2, ExperimentKey: req.ExperimentKey, Name: req.Name}
	s.experiments = append(s.experiments, experiment)
	return experiment, nil
}
func (s *recordingStore) UpdateExperiment(id int64, req abtest.UpdateExperimentRequest) (abtest.Experiment, error) {
	experiment, err := s.GetExperiment(id)
	if err != nil {
		return abtest.Experiment{}, err
	}
	if req.Name != nil {
		experiment.Name = *req.Name
	}
	return experiment, nil
}
func (s *recordingStore) SetExperimentStatus(id int64, status string) (abtest.Experiment, error) {
	s.lastStatus = status
	experiment, err := s.GetExperiment(id)
	experiment.Status = status
	return experiment, err
}
func (s *recordingStore) PushFullGroup(id, groupID int64) (abtest.Experiment, error) {
	s.lastGroupID = groupID
	experiment, err := s.GetExperiment(id)
	experiment.Status = abtest.StatusRolledOut
	return experiment, err
}
func (s *recordingStore) WithdrawExperiment(id int64) (abtest.Experiment, error) {
	return s.SetExperimentStatus(id, abtest.StatusStopped)
}
func (s *recordingStore) AddGroup(experimentID int64, req abtest.GroupInput) (abtest.Group, error) {
	group := abtest.Group{ID: int64(len(s.groups) + 1), ExperimentID: experimentID, GroupKey: req.GroupKey, TrafficBasisPoints: req.TrafficBasisPoints}
	s.groups = append(s.groups, group)
	return group, nil
}
func (s *recordingStore) AddOverride(experimentID int64, req abtest.OverrideInput) (abtest.Override, error) {
	override := abtest.Override{ID: int64(len(s.overrides) + 1), ExperimentID: experimentID, SubjectType: req.SubjectType, SubjectID: req.SubjectID, Action: req.Action, GroupKey: req.GroupKey}
	s.overrides = append(s.overrides, override)
	return override, nil
}

func TestPublicABTestEndpoints(t *testing.T) {
	store := &recordingStore{}
	handler := NewServer(abtest.NewService(store)).Routes()

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{name: "health", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK},
		{name: "missing device", method: http.MethodGet, path: "/abtest/v1/client/config", wantStatus: http.StatusBadRequest},
		{name: "client config", method: http.MethodGet, path: "/abtest/v1/client/config?device_id=phone-1&platform=ios", wantStatus: http.StatusOK},
		{name: "evaluate method", method: http.MethodGet, path: "/abtest/v1/evaluate", wantStatus: http.StatusMethodNotAllowed},
		{name: "evaluate invalid JSON", method: http.MethodPost, path: "/abtest/v1/evaluate", body: "{", wantStatus: http.StatusBadRequest},
		{name: "evaluate invalid identity", method: http.MethodPost, path: "/abtest/v1/evaluate", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "evaluate", method: http.MethodPost, path: "/abtest/v1/evaluate", body: `{"subject_type":"device","subject_id":"phone-1"}`, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := performRequest(handler, tt.method, tt.path, tt.body, "")
			if rec.Code != tt.wantStatus || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("unexpected response: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}

func TestAdminAuthorizationAndExperimentRoutes(t *testing.T) {
	store := &recordingStore{experiments: []abtest.Experiment{{ID: 1, ExperimentKey: "feed", Name: "Feed"}}}
	server := NewServer(abtest.NewService(store))
	server.adminToken = "secret"
	handler := server.Routes()

	if rec := performRequest(handler, http.MethodGet, "/abtest/admin/api/experiments", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing admin token rejection, got %d", rec.Code)
	}
	if rec := performRequest(handler, http.MethodGet, "/abtest/admin/api/experiments", "", "secret"); rec.Code != http.StatusOK {
		t.Fatalf("expected authorized list, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := performRequest(handler, http.MethodPost, "/abtest/admin/api/experiments", `{"experiment_key":"new","name":"New"}`, "secret"); rec.Code != http.StatusCreated {
		t.Fatalf("expected experiment creation, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := performRequest(handler, http.MethodGet, "/abtest/admin/api/experiments/1", "", "secret"); rec.Code != http.StatusOK {
		t.Fatalf("expected experiment detail, got %d", rec.Code)
	}
	if rec := performRequest(handler, http.MethodDelete, "/abtest/admin/api/experiments/1", "", "secret"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected unsupported method rejection, got %d", rec.Code)
	}
	if rec := performRequest(handler, http.MethodGet, "/abtest/admin/api/experiments/not-an-id", "", "secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("expected malformed path rejection, got %d", rec.Code)
	}
}

func TestAdminExperimentActions(t *testing.T) {
	store := &recordingStore{experiments: []abtest.Experiment{{ID: 1, ExperimentKey: "feed", Name: "Feed"}}}
	server := NewServer(abtest.NewService(store))
	handler := server.Routes()

	actions := []struct {
		path       string
		body       string
		wantStatus int
		wantState  string
	}{
		{path: "/abtest/admin/api/experiments/1/start", wantStatus: http.StatusOK, wantState: abtest.StatusRunning},
		{path: "/abtest/admin/api/experiments/1/pause", wantStatus: http.StatusOK, wantState: abtest.StatusPaused},
		{path: "/abtest/admin/api/experiments/1/stop", wantStatus: http.StatusOK, wantState: abtest.StatusStopped},
		{path: "/abtest/admin/api/experiments/1/withdraw", wantStatus: http.StatusOK, wantState: abtest.StatusStopped},
		{path: "/abtest/admin/api/experiments/1/groups", body: `{"group_key":"variant","traffic_basis_points":5000}`, wantStatus: http.StatusCreated},
		{path: "/abtest/admin/api/experiments/1/overrides", body: `{"subject_type":"device","subject_id":"phone-1","action":"force_group","group_key":"variant"}`, wantStatus: http.StatusCreated},
		{path: "/abtest/admin/api/experiments/1/groups/9/push_full", wantStatus: http.StatusOK},
	}
	for _, action := range actions {
		rec := performRequest(handler, http.MethodPost, action.path, action.body, "")
		if rec.Code != action.wantStatus {
			t.Fatalf("action %s returned %d body=%s", action.path, rec.Code, rec.Body.String())
		}
		if action.wantState != "" && store.lastStatus != action.wantState {
			t.Fatalf("action %s set status %q want %q", action.path, store.lastStatus, action.wantState)
		}
	}
	if len(store.groups) != 1 || len(store.overrides) != 1 || store.lastGroupID != 9 {
		t.Fatalf("actions were not routed correctly: groups=%+v overrides=%+v pushed=%d", store.groups, store.overrides, store.lastGroupID)
	}
}

func TestParseExperimentPath(t *testing.T) {
	tests := []struct {
		path string
		want []interface{}
	}{
		{path: "12", want: []interface{}{int64(12), "", int64(0), "", true}},
		{path: "12/start", want: []interface{}{int64(12), "start", int64(0), "", true}},
		{path: "12/groups/9/push_full", want: []interface{}{int64(12), "groups", int64(9), "push_full", true}},
		{path: "bad", want: []interface{}{int64(0), "", int64(0), "", false}},
		{path: "12/groups/bad/push_full", want: []interface{}{int64(0), "", int64(0), "", false}},
	}
	for _, tt := range tests {
		id, action, childID, childAction, ok := parseExperimentPath(tt.path)
		got := []interface{}{id, action, childID, childAction, ok}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseExperimentPath(%q)=%#v want %#v", tt.path, got, tt.want)
		}
	}
}

func performRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("X-Admin-Token", token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
