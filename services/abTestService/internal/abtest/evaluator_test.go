package abtest

import (
	"testing"
	"time"
)

type memoryStore struct {
	experiments []Experiment
}

func (s *memoryStore) ListExperiments() ([]Experiment, error) {
	return s.experiments, nil
}

func (s *memoryStore) GetExperiment(id int64) (Experiment, error) {
	for _, experiment := range s.experiments {
		if experiment.ID == id {
			return experiment, nil
		}
	}
	return Experiment{}, nil
}

func (s *memoryStore) CreateExperiment(req CreateExperimentRequest) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) SetExperimentStatus(id int64, status string) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	return Group{}, nil
}

func (s *memoryStore) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	return Override{}, nil
}

func (s *memoryStore) ListEvaluationExperiments() ([]Experiment, error) {
	return s.experiments, nil
}

func TestEvaluateReturnsStableClientConfig(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "new_chat_ui",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		EndTime:         now.Add(time.Hour).Format(time.RFC3339),
		Salt:            "stable",
		Version:         3,
		Targeting: map[string]interface{}{
			"platforms":       []interface{}{"ios"},
			"min_app_version": "1.2.0",
		},
		Groups: []Group{{
			ID:                 1,
			GroupKey:           "variant",
			TrafficBasisPoints: 10000,
			Config: map[string]interface{}{
				"enable_new_chat_ui": true,
			},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
		Context: map[string]string{
			"platform":    "ios",
			"app_version": "1.2.1",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(resp.Experiments))
	}
	if resp.Experiments[0].GroupKey != "variant" {
		t.Fatalf("expected variant group, got %q", resp.Experiments[0].GroupKey)
	}
	if resp.MergedConfig["enable_new_chat_ui"] != true {
		t.Fatalf("expected merged config to enable new chat ui, got %#v", resp.MergedConfig)
	}
}

func TestEvaluateSkipsTargetingMismatch(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "ios_only",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		Targeting: map[string]interface{}{
			"platforms": []interface{}{"ios"},
		},
		Groups: []Group{{
			ID:                 1,
			GroupKey:           "variant",
			TrafficBasisPoints: 10000,
			Config:             map[string]interface{}{"flag": true},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
		Context:     map[string]string{"platform": "android"},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 0 {
		t.Fatalf("expected no assignments, got %#v", resp.Experiments)
	}
}

func TestEvaluateForceGroupOverride(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "override_test",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		Groups: []Group{
			{ID: 1, GroupKey: "control", TrafficBasisPoints: 10000, Config: map[string]interface{}{"flag": false}},
			{ID: 2, GroupKey: "variant", TrafficBasisPoints: 0, Config: map[string]interface{}{"flag": true}},
		},
		Overrides: []Override{{
			SubjectType: SubjectTypeDevice,
			SubjectID:   "device-001",
			Action:      OverrideForceGroup,
			GroupKey:    "variant",
			Config:      map[string]interface{}{"debug": true},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(resp.Experiments))
	}
	assignment := resp.Experiments[0]
	if assignment.GroupKey != "variant" || assignment.Config["flag"] != true || assignment.Config["debug"] != true {
		t.Fatalf("unexpected assignment: %#v", assignment)
	}
	if !assignment.OverrideApplied {
		t.Fatal("expected override to be marked")
	}
}
